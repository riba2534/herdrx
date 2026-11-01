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
// （见 internal/launchers）。成功的结果按主机缓存 60 秒，新建会话校验启动器时复用；
// 失败不缓存，更新 CLI、装上 zsh 后下一次请求就能拿到。

const (
	launcherCacheTTL = 60 * time.Second
	// 远端 5 秒超时加往返余量；不随浏览器断开取消，同一主机的并发请求共用这一次结果。
	launcherFetchTimeout = launchers.Timeout + 5*time.Second
)

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

// get 返回 hostID 的缓存结果；没有新鲜结果时由第一个请求执行 fetch，其余请求等它。
func (c *launcherCache) get(ctx context.Context, hostID string, fetch func() launchers.Result) launchers.Result {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*launcherEntry)
	}
	now := time.Now()
	for id, entry := range c.entries {
		if !entry.settled.IsZero() && now.Sub(entry.settled) > launcherCacheTTL {
			delete(c.entries, id)
		}
	}
	if entry := c.entries[hostID]; entry != nil {
		c.mu.Unlock()
		select {
		case <-entry.done:
			return entry.result
		case <-ctx.Done():
			return launchers.Unsupported(launchers.ReasonReadFailed)
		}
	}
	entry := &launcherEntry{done: make(chan struct{})}
	c.entries[hostID] = entry
	c.mu.Unlock()

	entry.result = fetch()
	c.mu.Lock()
	entry.settled = time.Now()
	if !entry.result.Supported && c.entries[hostID] == entry {
		delete(c.entries, hostID)
	}
	c.mu.Unlock()
	close(entry.done)
	return entry.result
}

// discoverLaunchers 取 host 的启动器列表（优先用缓存）。endpoint 已按 owner 打开。
func (a *API) discoverLaunchers(ctx context.Context, host store.Host, endpoint herdr.Endpoint) launchers.Result {
	return a.launcherLists.get(ctx, host.ID, func() launchers.Result {
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
	writeJSON(writer, http.StatusOK, a.discoverLaunchers(request.Context(), host, endpoint))
}
