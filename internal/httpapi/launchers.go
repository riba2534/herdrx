package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/launchers"
	"github.com/riba2534/herdrx/internal/store"
)

// 启动器列表：远程主机上 zsh alias 里的 Claude 启动器，只有名字和三个开关会到工作台
// （见 internal/launchers）。每台主机记住最近一次发现结果：列表接口只复用 60 秒内的
// 成功结果（失败时下一次请求重新发现，更新 CLI、装上 zsh 后立刻生效）；新建会话校验
// 启动器时复用 10 分钟内的任何结果。

const (
	launcherListTTL       = 60 * time.Second
	launcherValidationTTL = 10 * time.Minute
	// 远端 5 秒超时加往返余量；不随浏览器断开取消，同一主机的并发请求共用这一次结果。
	launcherFetchTimeout = launchers.Timeout + 5*time.Second
)

func listUsable(result launchers.Result, age time.Duration) bool {
	return result.Supported && age <= launcherListTTL
}

func validationUsable(_ launchers.Result, age time.Duration) bool {
	return age <= launcherValidationTTL
}

type launcherEntry struct {
	done    chan struct{}
	result  launchers.Result
	settled time.Time
}

// launcherCache 只在查表和登记时持锁；远端执行不持锁。
type launcherCache struct {
	mu      sync.Mutex
	entries map[string]*launcherEntry
}

// get 返回 hostID 的结果：进行中的发现一律等它；已结束的结果由 usable 判断能否复用，
// 不能复用时由这个请求重新发现，其余请求等它。
func (c *launcherCache) get(ctx context.Context, hostID string, usable func(launchers.Result, time.Duration) bool, fetch func() launchers.Result) launchers.Result {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*launcherEntry)
	}
	now := time.Now()
	for id, entry := range c.entries {
		if !entry.settled.IsZero() && now.Sub(entry.settled) > launcherValidationTTL {
			delete(c.entries, id)
		}
	}
	if entry := c.entries[hostID]; entry != nil {
		if entry.settled.IsZero() {
			c.mu.Unlock()
			select {
			case <-entry.done:
				return entry.result
			case <-ctx.Done():
				return launchers.Unsupported(launchers.ReasonReadFailed)
			}
		}
		if usable(entry.result, now.Sub(entry.settled)) {
			c.mu.Unlock()
			return entry.result
		}
	}
	entry := &launcherEntry{done: make(chan struct{})}
	c.entries[hostID] = entry
	c.mu.Unlock()

	// fetch panicking still settles the entry (as a failed read), so nobody waits on it forever.
	result := launchers.Unsupported(launchers.ReasonReadFailed)
	defer func() {
		c.mu.Lock()
		entry.result, entry.settled = result, time.Now()
		c.mu.Unlock()
		close(entry.done)
	}()
	result = fetch()
	return result
}

// discoverLaunchers 取 host 的启动器列表（usable 允许时用缓存）。endpoint 已按 owner 打开。
func (a *API) discoverLaunchers(ctx context.Context, host store.Host, endpoint herdr.Endpoint, usable func(launchers.Result, time.Duration) bool) launchers.Result {
	return a.launcherLists.get(ctx, host.ID, usable, func() launchers.Result {
		discover, ok := endpoint.(herdr.LauncherEndpoint)
		if !ok {
			return launchers.Unsupported(launchers.ReasonUnsupportedAgent)
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launcherFetchTimeout)
		defer cancel()
		result, err := discover.Launchers(fetchCtx)
		if err != nil {
			return launchers.Unsupported(launchers.ReasonReadFailed)
		}
		return result
	})
}

// hostLaunchers 虽是 GET，路由上也要求 CSRF 头：它会在远程主机上启动用户的交互 zsh，
// 不能由别的站点用一个跨站请求触发。
func (a *API) hostLaunchers(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "主机不存在或无权访问")
		return
	}
	connectCtx, cancel := context.WithTimeout(request.Context(), a.config.HostDialTimeout)
	endpoint, err := a.hosts.Open(connectCtx, host)
	cancel()
	if err != nil {
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()
	writeJSON(writer, http.StatusOK, a.discoverLaunchers(request.Context(), host, endpoint, listUsable))
}
