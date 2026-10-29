package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"

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
	promptTTL         = 10 * time.Minute
	promptLogCapacity = 4096
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
	outcome promptOutcome
}

// promptLog 是内存里的幂等表，容量有界；只在增删查时持锁，网络调用不持锁。
type promptLog struct {
	mu      sync.Mutex
	entries map[promptKey]*promptEntry
}

// 每个 API 实例一张表（测试里会有多个实例）。
var promptLogs sync.Map

func (a *API) promptLog() *promptLog {
	value, _ := promptLogs.LoadOrStore(a, &promptLog{})
	return value.(*promptLog)
}

// begin 登记一次发送；同一个 key 已有记录时返回那条记录与 false。
func (l *promptLog) begin(key promptKey, now time.Time) (*promptEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[promptKey]*promptEntry)
	}
	for other, entry := range l.entries {
		if now.Sub(entry.at) > promptTTL {
			delete(l.entries, other)
		}
	}
	if entry := l.entries[key]; entry != nil {
		return entry, false
	}
	if len(l.entries) >= promptLogCapacity {
		var oldest promptKey
		var oldestAt time.Time
		for other, entry := range l.entries {
			if oldestAt.IsZero() || entry.at.Before(oldestAt) {
				oldest, oldestAt = other, entry.at
			}
		}
		delete(l.entries, oldest)
	}
	entry := &promptEntry{at: now, done: make(chan struct{})}
	l.entries[key] = entry
	return entry, true
}

func (l *promptLog) finish(key promptKey, entry *promptEntry, outcome promptOutcome) {
	l.mu.Lock()
	entry.outcome = outcome
	if outcome == promptForgotten && l.entries[key] == entry {
		delete(l.entries, key)
	}
	l.mu.Unlock()
	close(entry.done)
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

	log := a.promptLog()
	key := promptKey{user: userFromContext(request.Context()).ID, host: host.ID, pane: paneID, id: input.ClientMsgID}
	var entry *promptEntry
	for {
		current, fresh := log.begin(key, time.Now())
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
	if dialog := screenparse.ParseDialog(text); dialog != nil {
		writeJSON(writer, http.StatusConflict, map[string]any{"code": "pane_dialog", "error": "终端正弹着一个选择对话框，这段文字没有发出去，免得被当成选项。请先回答对话框再发送", "dialog": dialog})
		return
	}
	if !input.Force && screenparse.HasDraft(text) {
		writeError(writer, http.StatusConflict, "pane_draft", "电脑上的输入框里有未发送的内容，这段文字没有发出去，免得两段拼在一起")
		return
	}

	// 从这里起开始往终端里打字：脱离浏览器请求的取消，两腿按自己的预算走完。
	sendCtx, cancelSend := context.WithTimeout(context.WithoutCancel(request.Context()), promptSendTimeout)
	defer cancelSend()
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
	writeJSON(writer, http.StatusOK, map[string]any{"delivered": "sent"})
}
