package httpapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/riba2534/herdrx/internal/agentstate"
	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/store"
)

// The overview reads every host of the user on each refresh. Bound both the
// fan-out and each host, so one slow or dead host only costs its own row.
var agentOverviewHostTimeout = 8 * time.Second

const agentOverviewWorkers = 4

type agentOverviewItem struct {
	HostID         string `json:"host_id"`
	HostName       string `json:"host_name"`
	PaneID         string `json:"pane_id"`
	WorkspaceID    string `json:"workspace_id"`
	TabID          string `json:"tab_id"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	TabLabel       string `json:"tab_label,omitempty"`
	Agent          string `json:"agent"`
	Name           string `json:"name,omitempty"`
	Title          string `json:"title,omitempty"`
	AgentStatus    string `json:"agent_status"`
	Attention      string `json:"attention"`
	StateChangeSeq int64  `json:"state_change_seq"`
	CWD            string `json:"cwd,omitempty"`

	workspaceNumber, tabNumber int
}

type agentOverviewHost struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func (a *API) agentRoutes(router chi.Router) {
	router.Get("/", a.listAgents)
	router.With(a.requireCSRF).Post("/seen", a.markAgentSeen)
}

// listAgents only reads Herdr snapshots. It never changes a Herdr session; the
// only write is pruning the user's own read markers of panes that are gone.
func (a *API) listAgents(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	userID := userFromContext(ctx).ID
	hosts, err := a.store.ListHosts(ctx, userID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "无法读取主机列表，请稍后重试")
		return
	}
	seen, err := a.store.AgentSeenForUser(ctx, userID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "无法读取已读标记，请稍后重试")
		return
	}
	results := make([]agentOverviewHost, len(hosts))
	agents := make([][]agentOverviewItem, len(hosts))
	indexes := make(chan int)
	var workers sync.WaitGroup
	for range min(agentOverviewWorkers, len(hosts)) {
		workers.Go(func() {
			for index := range indexes {
				results[index], agents[index] = a.collectHostAgents(ctx, userID, hosts[index], seen)
			}
		})
	}
	for index := range hosts {
		indexes <- index
	}
	close(indexes)
	workers.Wait()

	items := []agentOverviewItem{}
	for _, hostAgents := range agents {
		items = append(items, hostAgents...)
	}
	sortAgentOverview(items)
	attention := 0
	for _, item := range items {
		if agentstate.NeedsAttention(item.Attention) {
			attention++
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"agents": items, "hosts": results, "attention_count": attention})
}

func (a *API) collectHostAgents(ctx context.Context, userID string, host store.Host, seen map[string]store.AgentSeen) (agentOverviewHost, []agentOverviewItem) {
	result := agentOverviewHost{HostID: host.ID, HostName: host.Name, Status: "ok"}
	ctx, cancel := context.WithTimeout(ctx, agentOverviewHostTimeout)
	defer cancel()
	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		result.Status, result.Error = "error", agentOverviewConnectError(ctx, err)
		return result, nil
	}
	snapshot, err := endpoint.Snapshot(ctx)
	_ = endpoint.Close()
	if err != nil {
		result.Status = "error"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Error = "读取 Herdr 状态超时，请打开主机查看连接状态"
		} else {
			result.Error = "无法读取 Herdr 状态，请打开主机确认 Herdr 正在运行"
		}
		return result, nil
	}
	panes := make(map[string]herdr.Pane, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		panes[pane.ID] = pane
	}
	// Markers were loaded already; only touch the database when one is stale.
	// Failing to prune only leaves stale rows behind; the overview is still right.
	for _, marker := range seen {
		if _, ok := panes[marker.PaneID]; marker.HostID == host.ID && !ok {
			live := make(map[string]bool, len(panes))
			for paneID := range panes {
				live[paneID] = true
			}
			_ = a.store.PruneAgentSeen(ctx, userID, host.ID, live)
			break
		}
	}
	return result, hostAgentItems(host, snapshot, panes, seen)
}

func agentOverviewConnectError(ctx context.Context, err error) string {
	detail := hostruntime.DescribeError(err)
	switch {
	case detail.Code == "host_key_unknown":
		return "需要确认 SSH 主机指纹，请打开主机核对后再查看"
	case detail.Permanent || detail.Code == "host_capacity":
		return detail.Error()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "连接主机超时，请打开主机查看连接状态"
	default:
		return "无法连接主机，请打开主机查看连接状态"
	}
}

func hostAgentItems(host store.Host, snapshot herdr.Snapshot, panes map[string]herdr.Pane, seen map[string]store.AgentSeen) []agentOverviewItem {
	workspaces := make(map[string]herdr.Workspace, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		workspaces[workspace.ID] = workspace
	}
	tabs := make(map[string]herdr.Tab, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		tabs[tab.ID] = tab
	}
	items := make([]agentOverviewItem, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		seenSeq := int64(-1)
		if marker, ok := seen[store.AgentSeenKey(host.ID, agent.PaneID)]; ok {
			seenSeq = marker.StateChangeSeq
		}
		pane := panes[agent.PaneID]
		cwd := agent.CWD
		if cwd == "" {
			cwd = pane.CWD
		}
		workspace, tab := workspaces[agent.WorkspaceID], tabs[agent.TabID]
		items = append(items, agentOverviewItem{
			HostID: host.ID, HostName: host.Name, PaneID: agent.PaneID,
			WorkspaceID: agent.WorkspaceID, TabID: agent.TabID,
			WorkspaceLabel: workspace.Label, TabLabel: tab.Label,
			Agent: agent.Agent, Name: agent.Name, Title: pane.TerminalTitleStripped,
			AgentStatus: agent.AgentStatus, StateChangeSeq: agent.StateChangeSeq, CWD: cwd,
			Attention:       agentstate.Classify(agent.AgentStatus, agent.StateChangeSeq, seenSeq),
			workspaceNumber: workspace.Number, tabNumber: tab.Number,
		})
	}
	return items
}

// sortAgentOverview orders by attention rank, then host name, then the
// workspace and tab order Herdr shows.
func sortAgentOverview(items []agentOverviewItem) {
	slices.SortStableFunc(items, func(left, right agentOverviewItem) int {
		return cmp.Or(
			cmp.Compare(agentstate.Rank(left.Attention), agentstate.Rank(right.Attention)),
			cmp.Compare(left.HostName, right.HostName),
			cmp.Compare(left.HostID, right.HostID),
			cmp.Compare(left.workspaceNumber, right.workspaceNumber),
			cmp.Compare(left.tabNumber, right.tabNumber),
			cmp.Compare(left.PaneID, right.PaneID),
		)
	})
}

// markAgentSeen records a Web read marker only; Herdr is never contacted.
func (a *API) markAgentSeen(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		HostID         string `json:"host_id"`
		PaneID         string `json:"pane_id"`
		StateChangeSeq int64  `json:"state_change_seq"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_input", "请求格式无效，请刷新页面后重试")
		return
	}
	if !safePaneID.MatchString(input.PaneID) {
		writeError(writer, http.StatusBadRequest, "invalid_pane_id", "终端标识无效，请刷新页面后重试")
		return
	}
	if input.StateChangeSeq < 0 {
		writeError(writer, http.StatusBadRequest, "invalid_input", "状态序号无效，请刷新页面后重试")
		return
	}
	err := a.store.MarkAgentSeen(request.Context(), userFromContext(request.Context()).ID, input.HostID, input.PaneID, input.StateChangeSeq)
	if errors.Is(err, store.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "database_error", "无法保存已读标记，请稍后重试")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}
