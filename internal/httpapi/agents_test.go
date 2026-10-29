package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

// Agent 总览的测试全部使用假的主机池，不连接任何真实主机或 Herdr。

type agentsTestEndpoint struct {
	snapshot    herdr.Snapshot
	snapshotErr error
	block       bool // Snapshot 一直等到 ctx 结束，模拟卡住的主机。
	calls       *int
	mu          *sync.Mutex
}

func (e agentsTestEndpoint) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	if e.block {
		<-ctx.Done()
		return herdr.Snapshot{}, ctx.Err()
	}
	return e.snapshot, e.snapshotErr
}

func (e agentsTestEndpoint) Call(context.Context, string, any) (json.RawMessage, error) {
	e.mu.Lock()
	*e.calls++
	e.mu.Unlock()
	return nil, errors.New("overview must not call Herdr methods")
}

func (agentsTestEndpoint) OpenTerminal(context.Context, herdr.TerminalOpen) (herdr.TerminalProcess, error) {
	return nil, errors.New("unexpected terminal")
}

func (agentsTestEndpoint) StageImage(context.Context, string, io.Reader) (string, error) {
	return "", errors.New("unexpected image")
}

func (agentsTestEndpoint) Close() error { return nil }

type agentsTestPool struct {
	mu        sync.Mutex
	endpoints map[string]agentsTestEndpoint
	openErr   map[string]error
	opened    map[string]int
	calls     int
}

func (p *agentsTestPool) Open(_ context.Context, host store.Host) (herdr.Endpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened[host.ID]++
	if err := p.openErr[host.ID]; err != nil {
		return nil, err
	}
	endpoint, ok := p.endpoints[host.ID]
	if !ok {
		return nil, errors.New("unexpected host " + host.ID)
	}
	endpoint.calls, endpoint.mu = &p.calls, &p.mu
	return endpoint, nil
}
func (*agentsTestPool) CloseHost(string) {}
func (*agentsTestPool) Close()           {}

type agentsFixture struct {
	db     *store.Store
	server string
	client *http.Client
	csrf   string
	owner  string
	pool   *agentsTestPool
}

func newAgentsFixture(t *testing.T) *agentsFixture {
	t.Helper()
	value, db, server, client, info := authFixture(t)
	value.stopBackground()
	value.hosts.Close()
	pool := &agentsTestPool{endpoints: map[string]agentsTestEndpoint{}, openErr: map[string]error{}, opened: map[string]int{}}
	value.hosts = pool
	return &agentsFixture{db: db, server: server.URL, client: client, csrf: info["csrf_token"].(string), owner: info["user"].(map[string]any)["id"].(string), pool: pool}
}

func (f *agentsFixture) addHost(t *testing.T, owner, id, name string) {
	t.Helper()
	if err := f.db.CreateHost(context.Background(), store.Host{ID: id, OwnerID: owner, Name: name, Transport: "ssh", Port: 22}); err != nil {
		t.Fatal(err)
	}
}

// member 用邀请码注册一个普通用户，返回其客户端与 CSRF。
func (f *agentsFixture) member(t *testing.T) (*http.Client, string, string) {
	t.Helper()
	invite := postJSON(t, f.client, f.server+"/api/admin/invites", f.csrf, nil, 201)
	member := newTestClient(t)
	result := postJSON(t, member, f.server+"/api/register", "", map[string]any{"email": "member@example.test", "password": "fixture-member-password", "display_name": "Member", "invite_code": invite["code"]}, 200)
	return member, result["csrf_token"].(string), result["user"].(map[string]any)["id"].(string)
}

type overviewResponse struct {
	Agents []struct {
		HostID, HostName, PaneID, WorkspaceLabel, TabLabel, Agent, Title, Attention, CWD string
		StateChangeSeq                                                                   int64
	}
	Hosts []struct {
		HostID, Status, Error string
	}
	AttentionCount int
}

func (f *agentsFixture) overview(t *testing.T, client *http.Client) overviewResponse {
	t.Helper()
	response, err := client.Get(f.server + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("GET /api/agents status=%d", response.StatusCode)
	}
	var raw struct {
		Agents []struct {
			HostID         string `json:"host_id"`
			HostName       string `json:"host_name"`
			PaneID         string `json:"pane_id"`
			WorkspaceLabel string `json:"workspace_label"`
			TabLabel       string `json:"tab_label"`
			Agent          string `json:"agent"`
			Title          string `json:"title"`
			Attention      string `json:"attention"`
			CWD            string `json:"cwd"`
			StateChangeSeq int64  `json:"state_change_seq"`
		} `json:"agents"`
		Hosts []struct {
			HostID string `json:"host_id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"hosts"`
		AttentionCount *int `json:"attention_count"`
	}
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw.Agents == nil || raw.Hosts == nil || raw.AttentionCount == nil {
		t.Fatalf("overview must always return agents, hosts and attention_count: %+v", raw)
	}
	var out overviewResponse
	for _, agent := range raw.Agents {
		out.Agents = append(out.Agents, struct {
			HostID, HostName, PaneID, WorkspaceLabel, TabLabel, Agent, Title, Attention, CWD string
			StateChangeSeq                                                                   int64
		}{agent.HostID, agent.HostName, agent.PaneID, agent.WorkspaceLabel, agent.TabLabel, agent.Agent, agent.Title, agent.Attention, agent.CWD, agent.StateChangeSeq})
	}
	for _, host := range raw.Hosts {
		out.Hosts = append(out.Hosts, struct{ HostID, Status, Error string }{host.HostID, host.Status, host.Error})
	}
	out.AttentionCount = *raw.AttentionCount
	return out
}

func agentSnapshot(agents ...herdr.Agent) herdr.Snapshot {
	snapshot := herdr.Snapshot{
		Workspaces: []herdr.Workspace{{ID: "w1", Label: "后端", Number: 1}, {ID: "w2", Label: "前端", Number: 2}},
		Tabs:       []herdr.Tab{{ID: "w1:t1", WorkspaceID: "w1", Label: "修复", Number: 1}, {ID: "w2:t1", WorkspaceID: "w2", Label: "页面", Number: 1}},
	}
	for _, agent := range agents {
		snapshot.Panes = append(snapshot.Panes, herdr.Pane{ID: agent.PaneID, WorkspaceID: agent.WorkspaceID, TabID: agent.TabID, CWD: "/srv/pane", TerminalTitleStripped: "title " + agent.PaneID})
	}
	snapshot.Agents = agents
	return snapshot
}

func TestAgentOverviewSortsAndComputesAttention(t *testing.T) {
	f := newAgentsFixture(t)
	f.addHost(t, f.owner, "host-b", "Beta")
	f.addHost(t, f.owner, "host-a", "Alpha")
	f.pool.endpoints["host-a"] = agentsTestEndpoint{snapshot: agentSnapshot(
		herdr.Agent{Agent: "codex", AgentStatus: "idle", PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 2},
		herdr.Agent{Agent: "claude", AgentStatus: "working", PaneID: "w2:p1", WorkspaceID: "w2", TabID: "w2:t1", StateChangeSeq: 3},
		herdr.Agent{Agent: "claude", AgentStatus: "done", PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 7, CWD: "/srv/agent"},
		herdr.Agent{Agent: "claude", AgentStatus: "done", PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 5},
	)}
	f.pool.endpoints["host-b"] = agentsTestEndpoint{snapshot: agentSnapshot(
		herdr.Agent{Agent: "codex", AgentStatus: "blocked", PaneID: "w2:p9", WorkspaceID: "w2", TabID: "w2:t1", StateChangeSeq: 1},
	)}
	ctx := context.Background()
	// w1:p3 已看到当前这一轮；w1:p2 只看过上一轮，仍是完成未看。
	if err := f.db.MarkAgentSeen(ctx, f.owner, "host-a", "w1:p3", 5); err != nil {
		t.Fatal(err)
	}
	if err := f.db.MarkAgentSeen(ctx, f.owner, "host-a", "w1:p2", 6); err != nil {
		t.Fatal(err)
	}

	got := f.overview(t, f.client)
	var order []string
	for _, agent := range got.Agents {
		order = append(order, agent.HostID+"/"+agent.PaneID+"="+agent.Attention)
	}
	want := []string{"host-b/w2:p9=needs_you", "host-a/w1:p2=unseen_done", "host-a/w2:p1=working", "host-a/w1:p1=seen", "host-a/w1:p3=seen"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("order\n got %v\nwant %v", order, want)
	}
	if got.AttentionCount != 2 {
		t.Fatalf("attention_count=%d want 2", got.AttentionCount)
	}
	first := got.Agents[1]
	if first.HostName != "Alpha" || first.WorkspaceLabel != "后端" || first.TabLabel != "修复" || first.CWD != "/srv/agent" || first.Title != "title w1:p2" || first.StateChangeSeq != 7 {
		t.Fatalf("labels not taken from the snapshot: %+v", first)
	}
	if got.Agents[3].CWD != "/srv/pane" {
		t.Fatalf("cwd should fall back to the pane: %+v", got.Agents[3])
	}
	if f.pool.calls != 0 {
		t.Fatalf("overview called %d Herdr methods", f.pool.calls)
	}
}

func TestAgentOverviewHostFailureIsIsolated(t *testing.T) {
	previous := agentOverviewHostTimeout
	agentOverviewHostTimeout = 200 * time.Millisecond
	t.Cleanup(func() { agentOverviewHostTimeout = previous })
	f := newAgentsFixture(t)
	f.addHost(t, f.owner, "host-ok", "OK")
	f.addHost(t, f.owner, "host-down", "Down")
	f.addHost(t, f.owner, "host-herdr", "NoHerdr")
	f.addHost(t, f.owner, "host-slow", "Slow")
	f.pool.endpoints["host-ok"] = agentsTestEndpoint{snapshot: agentSnapshot(herdr.Agent{Agent: "claude", AgentStatus: "blocked", PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1"})}
	f.pool.openErr["host-down"] = errors.New("dial tcp: connection refused")
	f.pool.endpoints["host-herdr"] = agentsTestEndpoint{snapshotErr: errors.New("herdr socket missing")}
	f.pool.endpoints["host-slow"] = agentsTestEndpoint{block: true}

	started := time.Now()
	got := f.overview(t, f.client)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a slow host held the overview for %s", elapsed)
	}
	if len(got.Agents) != 1 || got.Agents[0].HostID != "host-ok" || got.AttentionCount != 1 {
		t.Fatalf("healthy host lost: %+v", got)
	}
	status := map[string]string{}
	for _, host := range got.Hosts {
		status[host.HostID] = host.Status + ":" + host.Error
	}
	if len(status) != 4 || status["host-ok"] != "ok:" {
		t.Fatalf("hosts=%v", status)
	}
	for id, fragment := range map[string]string{"host-down": "无法连接主机", "host-herdr": "无法读取 Herdr 状态", "host-slow": "超时"} {
		if !strings.HasPrefix(status[id], "error:") || !strings.Contains(status[id], fragment) {
			t.Fatalf("%s: %q should be an error containing %q", id, status[id], fragment)
		}
	}
}

func TestAgentOverviewOwnerIsolation(t *testing.T) {
	f := newAgentsFixture(t)
	member, memberCSRF, memberID := f.member(t)
	f.addHost(t, f.owner, "admin-host", "Admin")
	f.addHost(t, memberID, "member-host", "Member")
	f.pool.endpoints["admin-host"] = agentsTestEndpoint{snapshot: agentSnapshot(herdr.Agent{Agent: "claude", AgentStatus: "done", PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 4})}
	f.pool.endpoints["member-host"] = agentsTestEndpoint{snapshot: agentSnapshot(herdr.Agent{Agent: "codex", AgentStatus: "done", PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 4})}

	got := f.overview(t, member)
	if len(got.Hosts) != 1 || got.Hosts[0].HostID != "member-host" || len(got.Agents) != 1 || got.Agents[0].HostID != "member-host" {
		t.Fatalf("member saw another user's hosts: %+v", got)
	}
	if f.pool.opened["admin-host"] != 0 {
		t.Fatal("member overview connected to the admin's host")
	}
	// 越权标记返回 404，且不会改变主机主人的已读状态。
	requestJSON(t, member, "POST", f.server+"/api/agents/seen", memberCSRF, map[string]any{"host_id": "admin-host", "pane_id": "w1:p1", "state_change_seq": 4}, 404)
	requestJSON(t, member, "POST", f.server+"/api/agents/seen", memberCSRF, map[string]any{"host_id": "missing", "pane_id": "w1:p1", "state_change_seq": 4}, 404)
	if admin := f.overview(t, f.client); admin.Agents[0].Attention != "unseen_done" {
		t.Fatalf("foreign mark changed the owner's marker: %+v", admin.Agents[0])
	}
	// 主人自己的标记只影响自己。
	requestJSON(t, member, "POST", f.server+"/api/agents/seen", memberCSRF, map[string]any{"host_id": "member-host", "pane_id": "w1:p1", "state_change_seq": 4}, 200)
	if got := f.overview(t, member); got.Agents[0].Attention != "seen" || got.AttentionCount != 0 {
		t.Fatalf("mark did not apply: %+v", got)
	}
}

func TestAgentSeenValidationAndPruning(t *testing.T) {
	f := newAgentsFixture(t)
	f.addHost(t, f.owner, "host-a", "Alpha")
	url := f.server + "/api/agents/seen"
	for _, pane := range []string{"", "../etc", "w1 p1", strings.Repeat("a", 200)} {
		requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": pane, "state_change_seq": 1}, 400)
	}
	requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": "w1:p1", "state_change_seq": -1}, 400)
	requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": "w1:p1", "state_change_seq": 1, "extra": true}, 400)
	// 写操作需要 CSRF。
	requestJSON(t, f.client, "POST", url, "", map[string]any{"host_id": "host-a", "pane_id": "w1:p1", "state_change_seq": 1}, 403)
	requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": "w1:gone", "state_change_seq": 3}, 200)
	requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": "w1:p1", "state_change_seq": 9}, 200)

	// Herdr 重启后计数从 0 开始、pane id 复用：旧标记大于当前序号不能让这一轮显示为已看。
	f.pool.endpoints["host-a"] = agentsTestEndpoint{snapshot: agentSnapshot(herdr.Agent{Agent: "claude", AgentStatus: "done", PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", StateChangeSeq: 3})}
	if got := f.overview(t, f.client); got.Agents[0].Attention != "unseen_done" {
		t.Fatalf("marker from a previous Herdr run hid the round: %+v", got.Agents[0])
	}
	requestJSON(t, f.client, "POST", url, f.csrf, map[string]any{"host_id": "host-a", "pane_id": "w1:p1", "state_change_seq": 3}, 200)
	if got := f.overview(t, f.client); got.Agents[0].Attention != "seen" {
		t.Fatalf("lower mark after a restart did not apply: %+v", got.Agents[0])
	}
	seen, err := f.db.AgentSeenForUser(context.Background(), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen[store.AgentSeenKey("host-a", "w1:gone")]; ok {
		t.Fatal("marker of a vanished pane was not pruned")
	}
	if _, ok := seen[store.AgentSeenKey("host-a", "w1:p1")]; !ok {
		t.Fatal("marker of a live pane was pruned")
	}
}

func TestAgentOverviewRequiresLogin(t *testing.T) {
	f := newAgentsFixture(t)
	anon := newTestClient(t)
	requestJSON(t, anon, "GET", f.server+"/api/agents", "", nil, 401)
	requestJSON(t, anon, "POST", f.server+"/api/agents/seen", "", map[string]any{"host_id": "x", "pane_id": "w1:p1", "state_change_seq": 1}, 401)
}
