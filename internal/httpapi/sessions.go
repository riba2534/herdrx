package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

// 新建 Claude 会话：在 cwd 新开 workspace（或在已有 workspace 新开 tab），等 shell 提示符出来，
// 往新 pane 输入一行 `<启动器> --session-id <uuid>` 并回车，然后立即返回，不等 Claude 就绪。
//
// 输入进 pane 的只有这一行：启动器名字过了正则且在这台主机发现到的列表里（或者就是 claude），
// uuid 由服务端生成；cwd 和 label 只作为 Herdr 参数传递，从不进入 shell。
// 任何失败都不关闭已经建好的 pane：远程任务的生命周期不归网站管。
//
// request_id 在 10 分钟内（每用户 + 主机）只执行一次，重复请求拿到第一次的结果，并发的重复
// 请求等第一次结束。确定什么都没创建的失败不记账，同一个 id 可以再试。

var sessionRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

const (
	sessionStartTTL = 10 * time.Minute
	// 每个用户最多记住这么多次启动；满了只淘汰最早结束的，进行中的不淘汰。
	sessionStartUserCapacity = 64
	// 整个启动的预算：连接、取启动器列表、建 pane、等提示符、输入。不随浏览器断开取消。
	sessionStartTimeout = 45 * time.Second
	// 等新 pane 的 shell 打出提示符；超时也照常输入，shell 会读到排队的输入。
	sessionShellWait    = 3 * time.Second
	sessionShellPoll    = 100 * time.Millisecond
	sessionCWDMaxBytes  = 4096
	sessionLabelMaxRune = 64
)

type startSessionInput struct {
	RequestID string `json:"request_id"`
	CWD       string `json:"cwd"`
	Launcher  string `json:"launcher"`
	Target    struct {
		Kind        string `json:"kind"`
		WorkspaceID string `json:"workspace_id"`
	} `json:"target"`
	Label string `json:"label"`
}

// StartSessionResult in web/src/types.ts.
type startSessionResult struct {
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	PaneID      string `json:"pane_id"`
	SessionID   string `json:"session_id"`
	Launcher    string `json:"launcher"`
}

// sessionReply 是记下来的完整响应，重复的 request_id 原样拿到它。
type sessionReply struct {
	status int
	body   any
}

func sessionError(status int, code, message string) *sessionReply {
	return &sessionReply{status: status, body: apiError{Error: message, Code: code}}
}

type sessionStartKey struct{ user, host, id string }

type sessionStartEntry struct {
	at    time.Time
	done  chan struct{}
	reply *sessionReply // 进行中时为 nil；忘记的记录直接删掉
}

// sessionStarts 是内存里的幂等表，按用户分开、容量有界；只在增删查时持锁。
type sessionStarts struct {
	mu    sync.Mutex
	users map[string]map[sessionStartKey]*sessionStartEntry
}

var errSessionStartBusy = errors.New("too many session starts in flight")

func (s *sessionStarts) begin(key sessionStartKey, now time.Time) (*sessionStartEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = make(map[string]map[sessionStartKey]*sessionStartEntry)
	}
	entries := s.users[key.user]
	if entries == nil {
		entries = make(map[sessionStartKey]*sessionStartEntry)
		s.users[key.user] = entries
	}
	for other, entry := range entries {
		if entry.reply != nil && now.Sub(entry.at) > sessionStartTTL {
			delete(entries, other)
		}
	}
	if entry := entries[key]; entry != nil {
		return entry, false, nil
	}
	if len(entries) >= sessionStartUserCapacity {
		var oldestKey sessionStartKey
		var oldest *sessionStartEntry
		for other, entry := range entries {
			if entry.reply != nil && (oldest == nil || entry.at.Before(oldest.at)) {
				oldestKey, oldest = other, entry
			}
		}
		if oldest == nil {
			return nil, false, errSessionStartBusy
		}
		delete(entries, oldestKey)
	}
	entry := &sessionStartEntry{at: now, done: make(chan struct{})}
	entries[key] = entry
	return entry, true, nil
}

// finish 记下 reply；reply 为 nil 表示确定什么都没创建，删掉记录让同一个 id 可以再试。
func (s *sessionStarts) finish(key sessionStartKey, entry *sessionStartEntry, reply *sessionReply) {
	s.mu.Lock()
	entry.reply = reply
	if entries := s.users[key.user]; reply == nil && entries[key] == entry {
		delete(entries, key)
		if len(entries) == 0 {
			delete(s.users, key.user)
		}
	}
	s.mu.Unlock()
	close(entry.done)
}

func hasControl(text string) bool { return strings.ContainsFunc(text, unicode.IsControl) }

// validateStartSession 检查格式；失败时返回要写的错误。
func validateStartSession(input startSessionInput) *sessionReply {
	switch {
	case !sessionRequestID.MatchString(input.RequestID):
		return sessionError(http.StatusBadRequest, "invalid_request_id", "请求编号无效，请刷新页面后重试")
	case !strings.HasPrefix(input.CWD, "/") || len(input.CWD) > sessionCWDMaxBytes || hasControl(input.CWD):
		return sessionError(http.StatusBadRequest, "invalid_cwd", "目录须是远程主机上以 / 开头的绝对路径，不超过 4096 字节，且不能包含换行或控制字符")
	case !launchers.ValidName(input.Launcher):
		return sessionError(http.StatusBadRequest, "invalid_launcher", "启动器名称无效，请重新选择启动器")
	case utf8.RuneCountInString(input.Label) > sessionLabelMaxRune || hasControl(input.Label):
		return sessionError(http.StatusBadRequest, "invalid_label", "名称不能超过 64 个字符，也不能包含换行或控制字符")
	}
	switch input.Target.Kind {
	case "workspace":
		if input.Target.WorkspaceID == "" {
			return nil
		}
	case "tab":
		if safePaneID.MatchString(input.Target.WorkspaceID) {
			return nil
		}
	}
	return sessionError(http.StatusBadRequest, "invalid_target", "请选择新建工作区，或选择一个已有工作区新建标签页")
}

func (a *API) startSession(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	var input startSessionInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "请求内容无效，请刷新页面后重试")
		return
	}
	if reply := validateStartSession(input); reply != nil {
		writeJSON(writer, reply.status, reply.body)
		return
	}
	key := sessionStartKey{user: userFromContext(request.Context()).ID, host: host.ID, id: input.RequestID}
	var entry *sessionStartEntry
	for {
		current, fresh, err := a.sessionStarts.begin(key, time.Now())
		if err != nil {
			writeError(writer, http.StatusTooManyRequests, "session_start_busy", "同时进行中的新建会话太多，什么都没有创建。请等前面的完成后再试")
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
		if current.reply != nil {
			writeJSON(writer, current.reply.status, current.reply.body)
			return
		}
		// 第一次确定什么都没创建，记录已删除：这次照常执行。
	}
	// 从这里起可能在远程主机上建东西：不随浏览器断开截断，但随访问授权撤销停止。
	ctx, release, err := a.inputContext(request, host.ID, sessionStartTimeout)
	if err != nil {
		a.sessionStarts.finish(key, entry, nil)
		a.authenticationError(writer, err)
		return
	}
	defer release()
	reply, record := a.runSessionStart(ctx, request, host, input)
	stored := reply
	if !record {
		stored = nil
	}
	a.sessionStarts.finish(key, entry, stored)
	writeJSON(writer, reply.status, reply.body)
}

// runSessionStart 执行一次启动。record 为 false 表示确定什么都没创建。
func (a *API) runSessionStart(ctx context.Context, request *http.Request, host store.Host, input startSessionInput) (*sessionReply, bool) {
	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		return sessionError(http.StatusBadGateway, "herdr_unavailable", "无法连接远程主机上的 Herdr，什么都没有创建："+hostruntime.DescribeError(err).Error()), false
	}
	defer endpoint.Close()
	if input.Launcher != "claude" {
		known := a.discoverLaunchers(ctx, host, endpoint)
		if !slices.ContainsFunc(known.Launchers, func(launcher launchers.Launcher) bool { return launcher.Name == input.Launcher }) {
			return sessionError(http.StatusConflict, "launcher_unknown", "远程主机上没有找到这个启动器，什么都没有创建。请刷新启动器列表后重新选择"), false
		}
	}
	method := "workspace.create"
	params := map[string]any{"cwd": input.CWD, "focus": false}
	if input.Target.Kind == "tab" {
		snapshot, err := endpoint.Snapshot(ctx)
		if err != nil {
			return sessionError(http.StatusBadGateway, "herdr_unavailable", "读取远程主机上的 Herdr 状态失败，什么都没有创建。请检查主机连接后重试"), false
		}
		if !slices.ContainsFunc(snapshot.Workspaces, func(workspace herdr.Workspace) bool { return workspace.ID == input.Target.WorkspaceID }) {
			return sessionError(http.StatusNotFound, "workspace_not_found", "这个工作区已不存在，什么都没有创建。请刷新后重新选择"), false
		}
		method = "tab.create"
		params["workspace_id"] = input.Target.WorkspaceID
	}
	if input.Label != "" {
		params["label"] = input.Label
	}
	raw, err := endpoint.Call(ctx, method, params)
	if err != nil {
		var rejected *herdr.APIError
		if errors.As(err, &rejected) {
			return sessionError(http.StatusBadGateway, "start_failed", "Herdr 没有创建新终端（"+rejected.Message+"），请确认目录存在后重试"), false
		}
		return sessionError(http.StatusBadGateway, "start_failed", "创建结果不确定，不会自动重试。请先在工作台查看是否已出现新的工作区或标签页"), true
	}
	var created struct {
		Workspace herdr.Workspace `json:"workspace"`
		Tab       herdr.Tab       `json:"tab"`
		RootPane  herdr.Pane      `json:"root_pane"`
	}
	_ = json.Unmarshal(raw, &created)
	result := startSessionResult{WorkspaceID: created.Tab.WorkspaceID, TabID: created.Tab.ID, PaneID: created.RootPane.ID, Launcher: input.Launcher}
	if result.WorkspaceID == "" {
		result.WorkspaceID = created.Workspace.ID
	}
	if !safePaneID.MatchString(result.PaneID) {
		return sessionError(http.StatusBadGateway, "start_failed", "Herdr 的回应里没有新终端，不会自动重试。请先在工作台查看是否已出现新的工作区或标签页"), true
	}
	waitForShell(ctx, endpoint, result.PaneID)
	result.SessionID = uuid.NewV4().String()
	line := input.Launcher + " --session-id " + result.SessionID
	_, sendErr := endpoint.Call(ctx, "pane.send_input", map[string]any{"pane_id": result.PaneID, "text": line, "keys": []string{"Enter"}})
	outcome := "started"
	if sendErr != nil {
		outcome = "input_failed"
	}
	a.audit(request.WithContext(ctx), "session.start", "host", host.ID, map[string]any{"pane_id": result.PaneID, "launcher": input.Launcher, "cwd": input.CWD, "result": outcome})
	if sendErr != nil {
		return &sessionReply{status: http.StatusBadGateway, body: map[string]any{
			"code":    "start_failed",
			"error":   "新终端已建好，但启动命令没有确认送达，不会自动重试。终端保持打开，请到这个终端里查看后手动启动",
			"details": map[string]string{"workspace_id": result.WorkspaceID, "tab_id": result.TabID, "pane_id": result.PaneID},
		}}, true
	}
	return &sessionReply{status: http.StatusOK, body: result}, true
}

// waitForShell 等新 pane 的可见画面非空（shell 打出了提示符），最多 sessionShellWait。
func waitForShell(ctx context.Context, endpoint herdr.Endpoint, paneID string) {
	ctx, cancel := context.WithTimeout(ctx, sessionShellWait)
	defer cancel()
	for {
		raw, err := endpoint.Call(ctx, "pane.read", map[string]any{"pane_id": paneID, "source": "visible", "format": "text"})
		if err == nil {
			var read struct {
				Read struct {
					Text string `json:"text"`
				} `json:"read"`
			}
			if json.Unmarshal(raw, &read) == nil && strings.TrimSpace(read.Read.Text) != "" {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sessionShellPoll):
		}
	}
}
