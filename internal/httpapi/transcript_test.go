package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/agentlog"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

// 结构化会话记录端点的测试：全部用假的 hostPool 与假的 Endpoint，
// 不连任何真实主机，也不读任何真实日志。

type transcriptTestEndpoint struct {
	snapshot    herdr.Snapshot
	snapshotErr error
	page        herdr.TranscriptPage
	pageErr     error
	diagnostic  string
	// 记录调用参数，供断言。
	calls     int
	scope     herdr.TranscriptScope
	request   herdr.TranscriptRequest
	snapshots int
}

func (e *transcriptTestEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	e.snapshots++
	return e.snapshot, e.snapshotErr
}

func (e *transcriptTestEndpoint) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("unexpected call")
}

func (e *transcriptTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}

func (e *transcriptTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected image")
}

func (e *transcriptTestEndpoint) Close() error { return nil }

func (e *transcriptTestEndpoint) Transcript(_ context.Context, scope herdr.TranscriptScope, request herdr.TranscriptRequest) (herdr.TranscriptPage, error) {
	e.calls++
	e.scope = scope
	e.request = request
	if e.pageErr != nil {
		return herdr.TranscriptPage{}, e.pageErr
	}
	return e.page, nil
}

func (e *transcriptTestEndpoint) TranscriptUnavailableReason() string { return e.diagnostic }

// transcriptPlainEndpoint 实现 Endpoint 但**没有** Transcript 能力。
type transcriptPlainEndpoint struct {
	snapshot herdr.Snapshot
}

func (e *transcriptPlainEndpoint) Snapshot(context.Context) (herdr.Snapshot, error) {
	return e.snapshot, nil
}
func (*transcriptPlainEndpoint) Call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("unexpected call")
}
func (*transcriptPlainEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}
func (*transcriptPlainEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected image")
}
func (*transcriptPlainEndpoint) Close() error { return nil }

type transcriptTestPool struct {
	endpoint herdr.Endpoint
	opened   int
}

func (p *transcriptTestPool) Open(context.Context, store.Host) (herdr.Endpoint, error) {
	p.opened++
	return p.endpoint, nil
}
func (*transcriptTestPool) CloseHost(string) {}
func (*transcriptTestPool) Close()           {}

type transcriptFixture struct {
	api    *API
	db     *store.Store
	server string
	client *http.Client
	owner  string
	host   store.Host
	pool   *transcriptTestPool
}

// newTranscriptFixture 搭好「管理员 + 一台自己的主机 + 假端点」。
func newTranscriptFixture(t *testing.T, endpoint herdr.Endpoint) *transcriptFixture {
	t.Helper()
	value, db, server, client, info := authFixture(t)
	value.stopBackground()
	value.hosts.Close()
	owner := info["user"].(map[string]any)["id"].(string)
	host := store.Host{ID: "transcript-host", OwnerID: owner, Name: "Transcript", Transport: "ssh", Port: 22}
	if err := db.CreateHost(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	pool := &transcriptTestPool{endpoint: endpoint}
	value.hosts = pool
	return &transcriptFixture{api: value, db: db, server: server.URL, client: client, owner: owner, host: host, pool: pool}
}

// get 发一次读取请求并返回原始响应；不需要 CSRF（只读 GET）。
func (f *transcriptFixture) get(t *testing.T, paneID, query string) *http.Response {
	t.Helper()
	response, err := f.client.Get(f.url(paneID, query))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func (f *transcriptFixture) url(paneID, query string) string {
	target := f.server + "/api/hosts/" + f.host.ID + "/panes/" + url.PathEscape(paneID) + "/transcript"
	if query != "" {
		target += "?" + query
	}
	return target
}

func agentPane(agent, cwd, foreground string) herdr.Snapshot {
	return herdr.Snapshot{Panes: []herdr.Pane{{
		ID: "w1:p1", Agent: agent, CWD: cwd, ForegroundCWD: foreground,
	}}}
}

func TestTranscriptExplicitDSHSource(t *testing.T) {
	for _, agent := range []string{"", "unrecognized-program", "dsh"} {
		t.Run("agent="+agent, func(t *testing.T) {
			endpoint := &transcriptTestEndpoint{
				snapshot: agentPane(agent, "/srv/fallback", "/srv/dsh-project"),
				page:     herdr.TranscriptPage{Supported: true, Agent: "dsh"},
			}
			fixture := newTranscriptFixture(t, endpoint)
			requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "source=dsh&cwd=/forged&agent=claude&path=/etc/passwd"), "", nil, 200)
			if endpoint.calls != 1 || endpoint.scope.Agent != "dsh" || endpoint.scope.CWD != "/srv/dsh-project" || endpoint.scope.PaneID != "w1:p1" {
				t.Fatalf("selected reader escaped server-owned scope: %+v", endpoint.scope)
			}
			endpoint.snapshot = agentPane(agent, "/srv/fallback", "")
			requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "source=dsh"), "", nil, 200)
			if endpoint.scope.CWD != "/srv/fallback" {
				t.Fatalf("selected reader lost cwd fallback: %+v", endpoint.scope)
			}
		})
	}
}

func TestTranscriptSourceRejectsConflictsAndArbitraryValues(t *testing.T) {
	for _, agent := range []string{agentlog.AgentClaude, agentlog.AgentCodex} {
		t.Run(agent, func(t *testing.T) {
			endpoint := &transcriptTestEndpoint{snapshot: agentPane(agent, "/srv/project", "")}
			fixture := newTranscriptFixture(t, endpoint)
			requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "source=dsh"), "", nil, 400)
			if endpoint.calls != 0 {
				t.Fatal("conflicting source reached the log reader")
			}
		})
	}
	for _, source := range []string{"claude", "codex", "../../etc", "DSH", "dsh&source=dsh"} {
		t.Run(source, func(t *testing.T) {
			endpoint := &transcriptTestEndpoint{snapshot: agentPane("", "/srv/project", "")}
			fixture := newTranscriptFixture(t, endpoint)
			requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "source="+source), "", nil, 400)
			if fixture.pool.opened != 0 || endpoint.calls != 0 {
				t.Fatal("invalid source reached host connection")
			}
		})
	}
}

func TestTranscriptScopeComesFromServerSidePaneFacts(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/fallback", "/srv/foreground"),
		page: herdr.TranscriptPage{
			Supported: true, Agent: agentlog.AgentClaude,
			Candidates: []agentlog.Candidate{}, Messages: []agentlog.Record{}, Binding: herdr.ChatBindingSelected,
		},
	}
	fixture := newTranscriptFixture(t, endpoint)
	if fixture.pool.opened != 0 {
		t.Fatal("pool opened before any request")
	}
	payload := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if endpoint.scope.CWD != "/srv/foreground" {
		t.Fatalf("scope cwd = %q, want the foreground cwd", endpoint.scope.CWD)
	}
	if endpoint.scope.Agent != agentlog.AgentClaude || endpoint.scope.PaneID != "w1:p1" {
		t.Fatalf("scope = %+v", endpoint.scope)
	}
	if payload["supported"] != true {
		t.Fatalf("payload = %v", payload)
	}
	// 候选与消息必须是数组而不是 null：前端直接遍历它们。
	if _, ok := payload["candidates"].([]any); !ok {
		t.Fatalf("candidates is not an array: %v", payload["candidates"])
	}
	if _, ok := payload["messages"].([]any); !ok {
		t.Fatalf("messages is not an array: %v", payload["messages"])
	}

	// 没有 foreground_cwd 时回退到 cwd。
	endpoint.snapshot = agentPane(agentlog.AgentClaude, "/srv/plain", "")
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if endpoint.scope.CWD != "/srv/plain" {
		t.Fatalf("scope cwd = %q, want the pane cwd fallback", endpoint.scope.CWD)
	}

	// 两者都空时端点必须看到空 cwd —— 服务端不猜路径，由能力层判 cwd_unavailable。
	endpoint.snapshot = agentPane(agentlog.AgentClaude, "", "")
	endpoint.page = herdr.TranscriptPage{Supported: false, Reason: herdr.ChatReasonCWDUnavailable, Messages: []agentlog.Record{}}
	missing := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if endpoint.scope.CWD != "" {
		t.Fatalf("scope invented a cwd: %q", endpoint.scope.CWD)
	}
	if missing["supported"] != false || missing["reason"] != herdr.ChatReasonCWDUnavailable {
		t.Fatalf("payload = %v", missing)
	}
	if _, ok := missing["messages"].([]any); !ok {
		t.Fatalf("degraded payload messages is not an array: %v", missing["messages"])
	}
}

// 三个不透明参数原样透传给能力层，形状之外的值在进入能力层之前就被拒绝。
func TestTranscriptPassesOpaqueParametersAndRejectsMalformedOnes(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page:     herdr.TranscriptPage{Supported: true, Messages: []agentlog.Record{}, Candidates: []agentlog.Candidate{}},
	}
	fixture := newTranscriptFixture(t, endpoint)

	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=abc-DEF_123&cursor=cur-1&before=bef-1"), "", nil, 200)
	if endpoint.request.Session != "abc-DEF_123" || endpoint.request.Cursor != "cur-1" || endpoint.request.Before != "bef-1" {
		t.Fatalf("request = %+v", endpoint.request)
	}

	before := endpoint.calls
	for _, broken := range []string{"session=a%2Fb", "session=x%20y", "cursor=%21%21", "before=..%2F..%2Fetc"} {
		requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", broken), "", nil, 400)
	}
	if endpoint.calls != before {
		t.Fatal("malformed token reached the capability layer")
	}
}

func TestTranscriptErrorsAndFallbacks(t *testing.T) {
	endpoint := &transcriptTestEndpoint{snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", "")}
	fixture := newTranscriptFixture(t, endpoint)

	// pane 不存在 → 404，且不去读日志。
	requestJSON(t, fixture.client, "GET", fixture.url("w9:p9", ""), "", nil, 404)
	if endpoint.calls != 0 {
		t.Fatal("capability layer called for a missing pane")
	}
	// pane id 形状不合法 → 400。
	requestJSON(t, fixture.client, "GET", fixture.url("-bad", ""), "", nil, 400)
	if endpoint.calls != 0 {
		t.Fatal("capability layer called for an invalid pane id")
	}
	// snapshot 失败 → 502。
	endpoint.snapshotErr = errors.New("dial failed")
	response := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 502)
	if response["code"] != "transcript_unavailable" {
		t.Fatalf("snapshot failure payload = %v", response)
	}
	endpoint.snapshotErr = nil
	// 能力层返回错误 → 502。
	endpoint.pageErr = errors.New("reader exploded")
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 502)
	endpoint.pageErr = nil
	if fixture.pool.opened == 0 {
		t.Fatal("pool was never opened")
	}
}

// 端点没有 Transcript 能力时，响应必须是 supported:false + unsupported_transport，
// 而不是假装支持或报 5xx —— 共享连接对其他操作仍然有效。
func TestTranscriptMissingCapabilityDegradesHonestly(t *testing.T) {
	endpoint := &transcriptPlainEndpoint{snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", "")}
	fixture := newTranscriptFixture(t, endpoint)

	payload := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if payload["supported"] != false || payload["reason"] != herdr.ChatReasonUnsupportedTransport {
		t.Fatalf("payload = %v", payload)
	}
	if _, ok := payload["messages"].([]any); !ok {
		t.Fatalf("messages is not an array: %v", payload["messages"])
	}
	if _, ok := payload["candidates"].([]any); !ok {
		t.Fatalf("candidates is not an array: %v", payload["candidates"])
	}
}

// 非 owner 一律 404，且不打开任何连接。
func TestTranscriptIsOwnerScoped(t *testing.T) {
	endpoint := &transcriptTestEndpoint{snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", "")}
	fixture := newTranscriptFixture(t, endpoint)

	member, err := fixture.api.createUserRecord(credentialsRequest{Email: "member@example.test", Password: "fixture-member-password", DisplayName: "Member"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.db.CreateUser(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if err = fixture.db.CreateHost(context.Background(), store.Host{ID: "member-host", OwnerID: member.ID, Name: "Member", Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}

	other := strings.Replace(fixture.url("w1:p1", ""), fixture.host.ID, "member-host", 1)
	missing := strings.Replace(fixture.url("w1:p1", ""), fixture.host.ID, "no-such-host", 1)
	requestJSON(t, fixture.client, "GET", other, "", nil, 404)
	requestJSON(t, fixture.client, "GET", missing, "", nil, 404)
	requestJSON(t, fixture.client, "GET", other+"?source=dsh", "", nil, 404)
	requestJSON(t, fixture.client, "GET", missing+"?source=dsh", "", nil, 404)
	if fixture.pool.opened != 0 {
		t.Fatal("non-owner request opened a host connection")
	}
	if endpoint.calls != 0 {
		t.Fatal("non-owner request reached the capability layer")
	}
}

// 审计只记身份与原因，不记任何正文；同时覆盖三种审计分支。
func TestTranscriptAuditRecordsIdentityWithoutContent(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page: herdr.TranscriptPage{
			Supported: true, Agent: agentlog.AgentClaude, SessionID: "sess-secret",
			Binding:  herdr.ChatBindingSelected,
			Messages: []agentlog.Record{{ID: "u-1", Role: agentlog.RoleUser, Blocks: []agentlog.Block{{Type: agentlog.BlockText, Text: "绝密的正文"}}}},
		},
		diagnostic: "remote python3 missing",
	}
	fixture := newTranscriptFixture(t, endpoint)

	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=abc"), "", nil, 200)
	opened, err := fixture.db.ListAudit(context.Background(), store.AuditFilter{Action: "transcript.opened"})
	if err != nil || len(opened) != 1 {
		t.Fatalf("opened audit = %v %v", opened, err)
	}
	if opened[0].UserID != fixture.owner || opened[0].TargetID != fixture.host.ID {
		t.Fatalf("opened audit = %+v", opened[0])
	}
	// 正文绝不能进审计；身份（pane / agent / session id）必须进。
	if strings.Contains(string(opened[0].Details), "绝密的正文") {
		t.Fatalf("audit leaked the message text: %s", opened[0].Details)
	}
	for _, expected := range []string{`"session_id"`, `"sess-secret"`, `"pane_id"`, agentlog.AgentClaude} {
		if !strings.Contains(string(opened[0].Details), expected) {
			t.Fatalf("audit lost %s: %s", expected, opened[0].Details)
		}
	}

	endpoint.page = herdr.TranscriptPage{Supported: false, Reason: herdr.ChatReasonReadDenied, Agent: agentlog.AgentClaude, Messages: []agentlog.Record{}}
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	denied, err := fixture.db.ListAudit(context.Background(), store.AuditFilter{Action: "transcript.denied"})
	if err != nil || len(denied) != 1 {
		t.Fatalf("denied audit = %v %v", denied, err)
	}

	endpoint.page = herdr.TranscriptPage{Supported: false, Reason: herdr.ChatReasonUnsupportedTransport, Messages: []agentlog.Record{}}
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	unavailable, err := fixture.db.ListAudit(context.Background(), store.AuditFilter{Action: "transcript.unavailable"})
	if err != nil || len(unavailable) != 1 {
		t.Fatalf("unavailable audit = %v %v", unavailable, err)
	}
	if !strings.Contains(string(unavailable[0].Details), "remote python3 missing") {
		t.Fatalf("diagnostic detail was dropped: %s", unavailable[0].Details)
	}
	// 能力缺失与拒绝都不会额外记 opened。
	opened, err = fixture.db.ListAudit(context.Background(), store.AuditFilter{Action: "transcript.opened"})
	if err != nil || len(opened) != 1 {
		t.Fatalf("unexpected opened rows: %v %v", opened, err)
	}
}

// 响应必须 no-store：会话正文不得被任何中间层缓存。
func TestTranscriptResponseIsNotCacheable(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page: herdr.TranscriptPage{
			Supported: true, Agent: agentlog.AgentClaude, SessionID: "sess-1", Binding: herdr.ChatBindingSelected,
			Messages:   []agentlog.Record{{ID: "u-1", Role: agentlog.RoleUser, Blocks: []agentlog.Block{{Type: agentlog.BlockText, Text: "正文"}}}},
			NextCursor: "next-token",
		},
	}
	fixture := newTranscriptFixture(t, endpoint)

	response := fixture.get(t, "w1:p1", "session=abc")
	if response.StatusCode != 200 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	var payload struct {
		Messages   []agentlog.Record `json:"messages"`
		NextCursor string            `json:"next_cursor"`
		Binding    string            `json:"binding"`
		Reset      bool              `json:"reset"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 1 || payload.Messages[0].ID != "u-1" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Binding != herdr.ChatBindingSelected {
		t.Fatalf("binding = %q", payload.Binding)
	}
	if payload.NextCursor != "next-token" {
		t.Fatalf("next_cursor = %q", payload.NextCursor)
	}
}

// 增量轮询：第二次请求只带 cursor，能力层只应看到游标，不重复任何内容。
func TestTranscriptIncrementalPollCarriesOnlyTheCursor(t *testing.T) {
	endpoint := &transcriptTestEndpoint{snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", "")}
	fixture := newTranscriptFixture(t, endpoint)

	endpoint.page = herdr.TranscriptPage{
		Supported: true, Agent: agentlog.AgentClaude, SessionID: "sess-1", Binding: herdr.ChatBindingSelected,
		Messages:   []agentlog.Record{{ID: "u-1", Role: agentlog.RoleUser, Blocks: []agentlog.Block{{Type: agentlog.BlockText, Text: "第一问"}}}},
		NextCursor: "cursor-1",
	}
	first := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=sess-token"), "", nil, 200)
	if endpoint.request.Session != "sess-token" || endpoint.request.Cursor != "" {
		t.Fatalf("first request = %+v", endpoint.request)
	}
	if first["next_cursor"] != "cursor-1" {
		t.Fatalf("first payload = %v", first)
	}

	endpoint.page = herdr.TranscriptPage{
		Supported: true, Agent: agentlog.AgentClaude, SessionID: "sess-1", Binding: herdr.ChatBindingSelected,
		Messages:   []agentlog.Record{{ID: "u-2", Role: agentlog.RoleUser, Blocks: []agentlog.Block{{Type: agentlog.BlockText, Text: "第二问"}}}},
		NextCursor: "cursor-2",
	}
	second := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=sess-token&cursor=cursor-1"), "", nil, 200)
	if endpoint.request.Cursor != "cursor-1" || endpoint.request.Before != "" {
		t.Fatalf("second request = %+v", endpoint.request)
	}
	messages := second["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["id"] != "u-2" {
		t.Fatalf("second payload = %v", second)
	}

	// 轮转：能力层置 reset，响应必须把它带出去（客户端据此整体清空重建）。
	endpoint.page = herdr.TranscriptPage{
		Supported: true, Agent: agentlog.AgentClaude, SessionID: "sess-1", Reset: true, Binding: herdr.ChatBindingSelected,
		Messages: []agentlog.Record{{ID: "u-9", Role: agentlog.RoleUser, Blocks: []agentlog.Block{{Type: agentlog.BlockText, Text: "新会话"}}}},
	}
	rotated := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=sess-token&cursor=cursor-2"), "", nil, 200)
	if rotated["reset"] != true {
		t.Fatalf("reset flag lost: %v", rotated)
	}
	// 反向翻页同样只是把不透明值带回去。
	endpoint.page = herdr.TranscriptPage{Supported: true, Messages: []agentlog.Record{}, Candidates: []agentlog.Candidate{}, Binding: herdr.ChatBindingSelected}
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "session=sess-token&before=prev-1"), "", nil, 200)
	if endpoint.request.Before != "prev-1" || endpoint.request.Cursor != "" {
		t.Fatalf("reverse request = %+v", endpoint.request)
	}
}

// 端点返回 nil 切片时响应里也必须是空数组，前端才能直接渲染。
func TestTranscriptEmptyPageSerializesEmptyArrays(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page:     herdr.TranscriptPage{Supported: false, Reason: herdr.ChatReasonUnsupportedAgent},
	}
	fixture := newTranscriptFixture(t, endpoint)
	payload := requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", ""), "", nil, 200)
	if payload["supported"] != false || payload["reason"] != herdr.ChatReasonUnsupportedAgent {
		t.Fatalf("payload = %v", payload)
	}
	if _, ok := payload["candidates"].([]any); !ok {
		t.Fatalf("candidates = %v", payload["candidates"])
	}
	if _, ok := payload["messages"].([]any); !ok {
		t.Fatalf("messages = %v", payload["messages"])
	}
	if _, present := payload["agent"]; present {
		t.Fatalf("unsupported agent leaked the agent field: %v", payload)
	}
}

// 查询参数永远不会变成路径：能力层收到的只有 pane 事实与三个不透明值。
func TestTranscriptNeverAcceptsAClientPath(t *testing.T) {
	endpoint := &transcriptTestEndpoint{
		snapshot: agentPane(agentlog.AgentClaude, "/srv/proj", ""),
		page:     herdr.TranscriptPage{Supported: true, Messages: []agentlog.Record{}, Candidates: []agentlog.Candidate{}},
	}
	fixture := newTranscriptFixture(t, endpoint)

	// 路径穿越写成 pane id 会被形状校验挡掉。
	requestJSON(t, fixture.client, "GET", fixture.url("..%2F..%2Fetc%2Fpasswd", ""), "", nil, 400)
	if endpoint.calls != 0 {
		t.Fatal("traversal-shaped pane id reached the capability layer")
	}
	// 额外的查询参数不会被当成路径或 cwd 使用。
	requestJSON(t, fixture.client, "GET", fixture.url("w1:p1", "path=%2Fetc%2Fpasswd&cwd=%2Froot"), "", nil, 200)
	if endpoint.scope.CWD != "/srv/proj" {
		t.Fatalf("client-supplied cwd reached the scope: %q", endpoint.scope.CWD)
	}
	if endpoint.request.Session != "" || endpoint.request.Cursor != "" || endpoint.request.Before != "" {
		t.Fatalf("unknown query parameters leaked into the request: %+v", endpoint.request)
	}
}
