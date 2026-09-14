package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/agentlog"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

// 不透明令牌的字面形状：base64url。长度上限单独判（Go 的 regexp 重复次数上限是 1000，
// 这里用显式长度检查表达「有界」），避免把超大参数喂给解密与远端调用。
var transcriptToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// transcriptTokenMaxBytes 是一个令牌能有多长。真实游标是 AES-GCM 密文的 base64，
// 几十字节量级；这个上限只是拒绝异常输入。
const transcriptTokenMaxBytes = 4096

// 一次读取的总预算。本轮 RPC 不含实时输入，超时即可安全放弃。
const transcriptRequestTimeout = 15 * time.Second

// transcriptResponse 是冻结契约（`web/src/lib/structuredChatTypes.ts` 的
// `StructuredChatResponse`）的序列化形状。字段名与可空性就是契约本身，不要改动。
//
// 所有降级情形同样返回 HTTP 200 —— 只有这样前端才能渲染诚实的空态而不是网络错误。
// HTTP 错误码只留给真正的传输/权限失败（主机不存在、pane 不存在、能力层不可达）。
type transcriptResponse struct {
	Supported      bool                 `json:"supported"`
	Reason         string               `json:"reason,omitempty"`
	Agent          string               `json:"agent,omitempty"`
	Candidates     []agentlog.Candidate `json:"candidates"`
	SessionID      string               `json:"session_id,omitempty"`
	Messages       []agentlog.Record    `json:"messages"`
	NextCursor     string               `json:"next_cursor,omitempty"`
	PreviousCursor string               `json:"previous_cursor,omitempty"`
	Reset          bool                 `json:"reset,omitempty"`
	HasMore        bool                 `json:"has_more,omitempty"`
	Skipped        int                  `json:"skipped,omitempty"`
	Binding        string               `json:"binding,omitempty"`
}

// readTranscript 是结构化会话记录的读取端点。
//
// 客户端只传 hostID / paneID / 三个不透明参数，可显式选择白名单 source=dsh。
// cwd 一律由服务端 `endpoint.Snapshot` 现取；默认 reader 来自 pane.agent，
// 未识别的程序允许用户选择 DSH 日志源，但不宣称验证了终端程序或会话绑定。
// 路径永远由「reader 白名单 + 服务端自己取到的 cwd」推导，不接受客户端路径。
func (a *API) readTranscript(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}
	// chi 会保留转义后的路径参数（真实 pane id 含 ':'）。
	paneID := chi.URLParam(request, "paneID")
	if request.URL.RawPath != "" {
		paneID, err = url.PathUnescape(paneID)
	}
	if err != nil || !safePaneID.MatchString(paneID) {
		writeError(writer, http.StatusBadRequest, "invalid_pane_id", "invalid pane id")
		return
	}

	query := request.URL.Query()
	source := query.Get("source")
	if len(query["source"]) > 1 || (source != "" && source != "dsh") {
		writeError(writer, http.StatusBadRequest, "invalid_source", "unsupported transcript source")
		return
	}
	read := herdr.TranscriptRequest{Session: query.Get("session"), Cursor: query.Get("cursor"), Before: query.Get("before")}
	for name, value := range map[string]string{"session": read.Session, "cursor": read.Cursor, "before": read.Before} {
		if value != "" && (len(value) > transcriptTokenMaxBytes || !transcriptToken.MatchString(value)) {
			writeError(writer, http.StatusBadRequest, "invalid_"+name, "invalid "+name+" cursor")
			return
		}
	}

	ctx, cancel := context.WithTimeout(request.Context(), transcriptRequestTimeout)
	defer cancel()

	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()

	snapshot, err := endpoint.Snapshot(ctx)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "transcript_unavailable", err.Error())
		return
	}
	pane := findPane(snapshot.Panes, paneID)
	if pane == nil {
		writeError(writer, http.StatusNotFound, "pane_not_found", "pane not found")
		return
	}

	agent := pane.Agent
	if source != "" {
		if agentlog.Supported(agent) && agent != source {
			writeError(writer, http.StatusBadRequest, "invalid_source", "transcript source conflicts with detected agent")
			return
		}
		agent = source
	}

	page := herdr.TranscriptPage{Supported: false, Reason: herdr.ChatReasonUnsupportedTransport, Messages: []agentlog.Record{}}
	detail := ""
	if reader, ok := herdr.AsTranscriptEndpoint(endpoint); ok {
		// 前台 cwd 优先；为空回退普通 cwd；两者都空就原样传空串，由能力层判 cwd_unavailable。
		// 这里**不能**复用 tailcat.go 的 firstNonEmpty —— 它的兜底值是「Tailcat host」这个
		// 主机名占位串，用在 cwd 上会凭空造出一个不存在的路径。
		cwd := strings.TrimSpace(pane.ForegroundCWD)
		if cwd == "" {
			cwd = strings.TrimSpace(pane.CWD)
		}
		scope := herdr.TranscriptScope{PaneID: paneID, Agent: agent, CWD: cwd}
		value, callErr := reader.Transcript(ctx, scope, read)
		if callErr != nil {
			writeError(writer, http.StatusBadGateway, "transcript_unavailable", callErr.Error())
			return
		}
		page = value
		if page.Reason == herdr.ChatReasonUnsupportedTransport {
			if diagnostics, ok := endpoint.(herdr.TranscriptDiagnostics); ok {
				detail = diagnostics.TranscriptUnavailableReason()
			}
		}
	}

	a.recordTranscriptAccess(request, host, page, paneID, read.Session != "", detail)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, transcriptResponse{
		Supported:      page.Supported,
		Reason:         page.Reason,
		Agent:          page.Agent,
		Candidates:     nonNilCandidates(page.Candidates),
		SessionID:      page.SessionID,
		Messages:       nonNilRecords(page.Messages),
		NextCursor:     page.NextCursor,
		PreviousCursor: page.PrevCursor,
		Reset:          page.Reset,
		HasMore:        page.HasMore,
		Skipped:        page.Skipped,
		Binding:        page.Binding,
	})
}

// recordTranscriptAccess 写审计：越界拒绝记 denied，能力缺失记 unavailable，
// 真正绑定并读取会话记 opened。
//
// 审计只记身份（pane / agent / session id）与失败原因，**绝不写任何正文**，
// 所以审计表不会变成第二份会话内容副本。
func (a *API) recordTranscriptAccess(request *http.Request, host store.Host, page herdr.TranscriptPage, paneID string, requested bool, detail string) {
	switch {
	case page.Reason == herdr.ChatReasonReadDenied:
		a.audit(request, "transcript.denied", "host", host.ID, map[string]any{"pane_id": paneID, "agent": page.Agent})
	case page.Reason == herdr.ChatReasonUnsupportedTransport:
		// 诊断文本是「远端缺 python3」这类可执行信息，只进审计与日志，不进冻结的响应字段。
		a.audit(request, "transcript.unavailable", "host", host.ID, map[string]any{"pane_id": paneID, "transport": host.Transport, "detail": detail})
	case page.Binding == herdr.ChatBindingSelected && requested:
		a.audit(request, "transcript.opened", "host", host.ID, map[string]any{
			"pane_id":    paneID,
			"agent":      page.Agent,
			"session_id": page.SessionID,
		})
	}
}

func findPane(panes []herdr.Pane, paneID string) *herdr.Pane {
	for index := range panes {
		if panes[index].ID == paneID {
			return &panes[index]
		}
	}
	return nil
}

func nonNilCandidates(candidates []agentlog.Candidate) []agentlog.Candidate {
	if candidates == nil {
		return []agentlog.Candidate{}
	}
	return candidates
}

func nonNilRecords(records []agentlog.Record) []agentlog.Record {
	if records == nil {
		return []agentlog.Record{}
	}
	return records
}
