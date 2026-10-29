package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/screenparse"
)

// 带防护的发送：把 Composer / 对话视图的一段文字送进 pane 之前先读一屏。
//   - 底部弹着选择对话框（权限、计划确认、提问、信任目录）时拒绝：文字会被当成选项。
//   - Claude 输入框里有电脑上打了没发的字时拒绝，除非用户确认 force：两段会拼在一起发出。
//
// client_msg_id 在 10 分钟内（每用户 + 主机 + pane）只发一次：已送达的重复请求直接确认，
// 已开始打字但结果不确定的重复请求如实告知，绝不自动重放。读屏失败、被拒绝或确定没送进去
// 的请求不记账，同一个 id 可以再试。

var clientMessageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	promptTTL = 10 * time.Minute
	// 每个用户最多记住这么多条；满了只淘汰该用户最早的已结束记录，进行中的不淘汰。
	promptUserCapacity = 256
	// 读屏与连接的预算；开始打字后改用独立的发送预算，浏览器断开也不会把发送截在半路。
	promptCheckTimeout = 15 * time.Second
	promptSendTimeout  = 10 * time.Second
	// 正文与回车之间留一次 settle，给目标一次 read 正文的机会（与 Composer 两腿发送一致）。
	promptSubmitSettle = 50 * time.Millisecond
)

type promptOutcome int

const (
	promptForgotten promptOutcome = iota + 1
	promptSent
	promptUnknown
)

type promptKey struct{ user, host, pane, id string }

type promptEntry struct {
	at      time.Time
	done    chan struct{}
	outcome promptOutcome // 0 表示进行中
}

// promptLog 是内存里的幂等表，按用户分开、每个用户容量有界；只在增删查时持锁，网络调用不持锁。
type promptLog struct {
	mu    sync.Mutex
	users map[string]map[promptKey]*promptEntry
}

var errPromptBusy = errors.New("too many prompts in flight")

// begin 登记一次发送；同一个 key 已有记录时返回那条记录与 false。该用户的记录满了且全在进行中时
// 返回 errPromptBusy。
func (l *promptLog) begin(key promptKey, now time.Time) (*promptEntry, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.users == nil {
		l.users = make(map[string]map[promptKey]*promptEntry)
	}
	entries := l.users[key.user]
	if entries == nil {
		entries = make(map[promptKey]*promptEntry)
		l.users[key.user] = entries
	}
	for other, entry := range entries {
		if entry.outcome != 0 && now.Sub(entry.at) > promptTTL {
			delete(entries, other)
		}
	}
	if entry := entries[key]; entry != nil {
		return entry, false, nil
	}
	if len(entries) >= promptUserCapacity {
		var oldest *promptKey
		var oldestAt time.Time
		for other, entry := range entries {
			if entry.outcome != 0 && (oldest == nil || entry.at.Before(oldestAt)) {
				other := other
				oldest, oldestAt = &other, entry.at
			}
		}
		if oldest == nil {
			return nil, false, errPromptBusy
		}
		delete(entries, *oldest)
	}
	entry := &promptEntry{at: now, done: make(chan struct{})}
	entries[key] = entry
	return entry, true, nil
}

func (l *promptLog) finish(key promptKey, entry *promptEntry, outcome promptOutcome) {
	l.mu.Lock()
	entry.outcome = outcome
	if entries := l.users[key.user]; outcome == promptForgotten && entries[key] == entry {
		delete(entries, key)
		if len(entries) == 0 {
			delete(l.users, key.user)
		}
	}
	l.mu.Unlock()
	close(entry.done)
}

// inputContext 是往终端里打字用的 context：不随浏览器断开而取消（免得把按键序列截在半路），
// 但随这次登录的访问授权撤销（退出、过期、禁用、主机移除）而取消。
func (a *API) inputContext(request *http.Request, hostID string, timeout time.Duration) (context.Context, func(), error) {
	ctx := request.Context()
	lease, err := a.access.attach(context.WithoutCancel(ctx), userFromContext(ctx).ID, sessionFromContext(ctx).ID)
	if err != nil {
		return nil, nil, err
	}
	lease.hostID.Store(&hostID)
	inputCtx, cancel := context.WithTimeout(lease.ctx, timeout)
	return inputCtx, func() { cancel(); lease.release() }, nil
}

func writeDeliveryUnknown(writer http.ResponseWriter, status int) {
	writeError(writer, status, "delivery_unknown", "上一次发送结果不确定，请先查看终端再决定是否重发")
}

func (a *API) panePrompt(writer http.ResponseWriter, request *http.Request) {
	host, paneID, ok := a.paneTarget(writer, request)
	if !ok {
		return
	}
	var input struct {
		Text        string `json:"text"`
		Submit      bool   `json:"submit"`
		ClientMsgID string `json:"client_msg_id"`
		Force       bool   `json:"force"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "发送内容无效，请检查后重试")
		return
	}
	if !clientMessageID.MatchString(input.ClientMsgID) {
		writeError(writer, http.StatusBadRequest, "invalid_client_msg_id", "消息编号无效，请刷新页面后重试")
		return
	}
	if input.Text == "" && !input.Submit {
		writeError(writer, http.StatusBadRequest, "empty_prompt", "没有要发送的内容")
		return
	}

	log := &a.prompts
	key := promptKey{user: userFromContext(request.Context()).ID, host: host.ID, pane: paneID, id: input.ClientMsgID}
	var entry *promptEntry
	for {
		current, fresh, err := log.begin(key, time.Now())
		if err != nil {
			writeError(writer, http.StatusTooManyRequests, "prompt_busy", "同时进行中的发送太多，什么都没有发送。请等前面的发送结束后再试")
			return
		}
		if fresh {
			entry = current
			break
		}
		select {
		case <-current.done:
		case <-request.Context().Done():
			return
		}
		switch current.outcome {
		case promptSent:
			writeJSON(writer, http.StatusOK, map[string]any{"delivered": "duplicate"})
			return
		case promptUnknown:
			writeDeliveryUnknown(writer, http.StatusConflict)
			return
		}
		// 上一次被拒绝或确定没送进去：记录已删除，这次照常检查并发送。
	}
	outcome := promptForgotten
	defer func() { log.finish(key, entry, outcome) }()

	ctx, cancel := context.WithTimeout(request.Context(), promptCheckTimeout)
	defer cancel()
	endpoint, text, ok := a.openPaneScreen(ctx, writer, host, paneID)
	if !ok {
		return
	}
	defer endpoint.Close()
	// 光标停在“Type something”之类的自由输入项上时，文字正是要进对话框自己的输入框，照常发送。
	if dialog := screenparse.ParseDialog(text); dialog != nil && !dialog.FreeText {
		payload := map[string]any{"code": "pane_dialog", "error": "终端正弹着一个选择对话框，这段文字没有发出去，免得被当成选项。请先回答对话框再发送"}
		if dialog.Multiple {
			payload["error"] = "终端正弹着一个多选对话框，这段文字没有发出去。请在终端里操作"
		} else {
			payload["dialog"] = dialog
		}
		writeJSON(writer, http.StatusConflict, payload)
		return
	}
	if !input.Force && screenparse.HasDraft(text) {
		writeError(writer, http.StatusConflict, "pane_draft", "电脑上的输入框里有未发送的内容，这段文字没有发出去，免得两段拼在一起")
		return
	}

	// 从这里起开始往终端里打字：两腿不随浏览器断开截断，但随访问授权撤销停止。
	sendCtx, releaseSend, err := a.inputContext(request, host.ID, promptSendTimeout)
	if err != nil {
		a.authenticationError(writer, err)
		return
	}
	defer releaseSend()
	if input.Text != "" {
		if _, err := endpoint.Call(sendCtx, "pane.send_input", map[string]any{"pane_id": paneID, "text": input.Text, "keys": []string{}}); err != nil {
			var rejected *herdr.APIError
			if errors.As(err, &rejected) {
				// Herdr 明确拒绝（例如 pane 已不存在）：正文没有送进去，可以安全重试。
				writeError(writer, http.StatusBadGateway, "pane_send_failed", "发送失败（"+rejected.Message+"），内容没有送入终端，请检查后重试")
				return
			}
			outcome = promptUnknown
			writeDeliveryUnknown(writer, http.StatusBadGateway)
			return
		}
		if input.Submit {
			time.Sleep(promptSubmitSettle)
		}
	}
	if input.Submit {
		if _, err := endpoint.Call(sendCtx, "pane.send_input", map[string]any{"pane_id": paneID, "text": "", "keys": []string{"Enter"}}); err != nil {
			outcome = promptUnknown
			writeError(writer, http.StatusBadGateway, "delivery_unknown", "正文已送入终端，但提交回车未确认。请先查看终端再决定是否重发")
			return
		}
	}
	outcome = promptSent
	a.audit(request.WithContext(sendCtx), "pane.prompt", "host", host.ID, map[string]any{"pane_id": paneID, "chars": utf8.RuneCountInString(input.Text), "submit": input.Submit, "force": input.Force})
	writeJSON(writer, http.StatusOK, map[string]any{"delivered": "sent"})
}
