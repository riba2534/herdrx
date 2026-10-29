package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/screenparse"
	"github.com/riba2534/herdrx/internal/store"
)

// 对话框卡片：服务端读一屏 pane（visible + ANSI），由 screenparse 认出底部的选择对话框。
// 回答前再读一次，指纹不等或对话框没了就拒绝，什么都不按；按键结果不确定也不重试。
// 认不出的对话框不出卡片，用户回终端操作。

// 读屏与回答的总预算。
const paneDialogTimeout = 15 * time.Second

// paneTarget 校验主机归属与 pane id；失败时已写好响应。
func (a *API) paneTarget(writer http.ResponseWriter, request *http.Request) (store.Host, string, bool) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return store.Host{}, "", false
	}
	// chi 会保留转义后的路径参数（真实 pane id 含 ':'）。
	paneID := chi.URLParam(request, "paneID")
	if request.URL.RawPath != "" {
		paneID, err = url.PathUnescape(paneID)
	}
	if err != nil || !safePaneID.MatchString(paneID) {
		writeError(writer, http.StatusBadRequest, "invalid_pane_id", "终端编号无效，请刷新工作台后重试")
		return store.Host{}, "", false
	}
	return host, paneID, true
}

// readVisibleScreen 读 pane 当前可见的一屏，保留颜色（dim 用来区分灰字建议与草稿）。
func readVisibleScreen(ctx context.Context, endpoint herdr.Endpoint, paneID string) (string, error) {
	raw, err := endpoint.Call(ctx, "pane.read", map[string]any{"pane_id": paneID, "source": "visible", "format": "ansi", "strip_ansi": false})
	if err != nil {
		return "", err
	}
	var result struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	return result.Read.Text, nil
}

// openPaneScreen 打开主机连接并读一屏；失败时已写好响应。成功时调用方负责关闭 endpoint。
func (a *API) openPaneScreen(ctx context.Context, writer http.ResponseWriter, host store.Host, paneID string) (herdr.Endpoint, string, bool) {
	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		writeHostConnectionError(writer, err)
		return nil, "", false
	}
	text, err := readVisibleScreen(ctx, endpoint, paneID)
	if err != nil {
		endpoint.Close()
		writeError(writer, http.StatusBadGateway, "pane_read_failed", "读取终端画面失败，什么都没有发送。请检查主机连接后重试，或直接在终端里操作")
		return nil, "", false
	}
	return endpoint, text, true
}

func (a *API) paneDialog(writer http.ResponseWriter, request *http.Request) {
	host, paneID, ok := a.paneTarget(writer, request)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), paneDialogTimeout)
	defer cancel()
	endpoint, text, ok := a.openPaneScreen(ctx, writer, host, paneID)
	if !ok {
		return
	}
	defer endpoint.Close()
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"dialog": screenparse.ParseDialog(text)})
}

// dialogKeys 是选中编号为 index 的选项要按的键：编号对话框直接按数字；否则从当前光标
// 位置按若干次上/下再回车。编号超过一位数时同样走方向键。没有这个选项时返回 nil。
func dialogKeys(dialog *screenparse.Dialog, index int) []string {
	target, current := dialog.Find(index), dialog.Selected()
	if target < 0 {
		return nil
	}
	if dialog.Numbered && index <= 9 {
		return []string{strconv.Itoa(index)}
	}
	if current < 0 {
		return nil
	}
	var keys []string
	for ; current < target; current++ {
		keys = append(keys, "Down")
	}
	for ; current > target; current-- {
		keys = append(keys, "Up")
	}
	return append(keys, "Enter")
}

func (a *API) answerPaneDialog(writer http.ResponseWriter, request *http.Request) {
	host, paneID, ok := a.paneTarget(writer, request)
	if !ok {
		return
	}
	var input struct {
		Fingerprint string `json:"fingerprint"`
		OptionIndex int    `json:"option_index"`
	}
	if err := decodeJSON(request, &input); err != nil || input.Fingerprint == "" || len(input.Fingerprint) > 64 || input.OptionIndex < 1 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "回答内容无效，请刷新卡片后重新选择")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), paneDialogTimeout)
	defer cancel()
	endpoint, text, ok := a.openPaneScreen(ctx, writer, host, paneID)
	if !ok {
		return
	}
	defer endpoint.Close()
	dialog := screenparse.ParseDialog(text)
	if dialog == nil {
		writeError(writer, http.StatusConflict, "dialog_gone", "对话框已经关闭（可能已在电脑上回答），这次什么都没有按")
		return
	}
	if dialog.Fingerprint != input.Fingerprint {
		writeError(writer, http.StatusConflict, "dialog_changed", "对话框已变化，请重新选择")
		return
	}
	keys := dialogKeys(dialog, input.OptionIndex)
	if keys == nil {
		writeError(writer, http.StatusBadRequest, "invalid_option", "没有这个选项，请刷新卡片后重新选择")
		return
	}
	a.audit(request, "pane.dialog_answer", "host", host.ID, map[string]any{"pane_id": paneID, "option_index": input.OptionIndex})
	if _, err := endpoint.Call(ctx, "pane.send_keys", map[string]any{"pane_id": paneID, "keys": keys}); err != nil {
		writeError(writer, http.StatusBadGateway, "answer_unknown", "按键结果不确定，不会自动重试。请先查看终端再决定下一步")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}
