package httpapi

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/riba2534/herdrx/internal/herdr"
	pushmessage "github.com/riba2534/herdrx/internal/push"
	"github.com/riba2534/herdrx/internal/store"
)

const pushWorkers = 4

// A finished round is announced only after it stayed finished and unread for
// this long, so a short pause between steps or a glance at the pane stays quiet.
const pushDoneDelay = 60 * time.Second

type pushPollState struct {
	mu       sync.Mutex
	previous map[string]string
	failures map[string]pushFailure
	// pending holds done rounds waiting for pushDoneDelay; every key is also in previous.
	pending map[string]pushPendingDone
	now     func() time.Time
}
type pushPendingDone struct {
	seq int64
	due time.Time
}
type pushFailure struct {
	attempts int
	until    time.Time
}
type pushHostJob struct {
	host          store.Host
	subscriptions []store.PushSubscription
}
type pushDelivery struct {
	job      pushHostJob
	messages []pushmessage.Notification
}

func newPushPollState(previous map[string]string) *pushPollState {
	if previous == nil {
		previous = make(map[string]string)
	}
	return &pushPollState{previous: previous, failures: make(map[string]pushFailure), pending: make(map[string]pushPendingDone), now: time.Now}
}
func pushHostPrefix(host store.Host) string { return host.OwnerID + ":" + host.ID + ":" }

func (a *API) monitorPush(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	state := newPushPollState(nil)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.collectPushHosts(ctx, state)
		}
	}
}
func (a *API) pollPushHosts(ctx context.Context, previous map[string]string) {
	a.collectPushHosts(ctx, newPushPollState(previous))
}
func (a *API) collectPushHosts(ctx context.Context, state *pushPollState) {
	subscriptions, err := a.store.PushSubscriptions(ctx)
	if err != nil {
		return
	}
	byUser := make(map[string][]store.PushSubscription)
	for _, subscription := range subscriptions {
		byUser[subscription.UserID] = append(byUser[subscription.UserID], subscription)
	}
	var jobs []pushHostJob
	live := make(map[string]bool)
	for userID, subscriptions := range byUser {
		hosts, err := a.store.ListHosts(ctx, userID)
		if err != nil {
			return
		} // Preserve history when a database read failed.
		for _, host := range hosts {
			live[pushHostPrefix(host)] = true
			jobs = append(jobs, pushHostJob{host: host, subscriptions: subscriptions})
		}
	}
	state.mu.Lock()
	for key := range state.previous {
		last := strings.LastIndexByte(key, ':')
		// pane IDs contain ':'; host IDs do not. Use the first two separators.
		if first := strings.IndexByte(key, ':'); first >= 0 {
			if next := strings.IndexByte(key[first+1:], ':'); next >= 0 {
				last = first + 1 + next
			}
		}
		if last < 0 || !live[key[:last+1]] {
			delete(state.previous, key)
			delete(state.pending, key)
		}
	}
	for prefix := range state.failures {
		if !live[prefix] {
			delete(state.failures, prefix)
		}
	}
	state.mu.Unlock()
	a.runPushJobs(ctx, jobs, state)
}
func (a *API) pollPushUser(ctx context.Context, userID string, subscriptions []store.PushSubscription, previous map[string]string) {
	hosts, err := a.store.ListHosts(ctx, userID)
	if err != nil {
		return
	}
	jobs := make([]pushHostJob, 0, len(hosts))
	for _, host := range hosts {
		jobs = append(jobs, pushHostJob{host: host, subscriptions: subscriptions})
	}
	a.runPushJobs(ctx, jobs, newPushPollState(previous))
}

// Collection and delivery have separate bounded pools: a slow push provider
// cannot monopolize host snapshot workers. No network operation holds state.mu.
func (a *API) runPushJobs(ctx context.Context, jobs []pushHostJob, state *pushPollState) {
	state.mu.Lock()
	now := state.now()
	eligible := jobs[:0]
	for _, job := range jobs {
		if !now.Before(state.failures[pushHostPrefix(job.host)].until) {
			eligible = append(eligible, job)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return state.failures[pushHostPrefix(eligible[i].host)].attempts < state.failures[pushHostPrefix(eligible[j].host)].attempts
	})
	state.mu.Unlock()
	if len(eligible) == 0 {
		return
	}
	queue := make(chan pushHostJob)
	deliveries := make(chan pushDelivery, pushWorkers*2)
	var collectors, senders sync.WaitGroup
	for range min(pushWorkers, len(eligible)) {
		collectors.Go(func() {
			for job := range queue {
				if ctx.Err() != nil {
					return
				}
				messages := a.collectPushHost(ctx, job, state)
				if len(messages) == 0 {
					continue
				}
				select {
				case deliveries <- pushDelivery{job: job, messages: messages}:
				case <-ctx.Done():
					return
				}
			}
		})
		senders.Go(func() {
			for delivery := range deliveries {
				if ctx.Err() != nil {
					return
				}
				a.deliverPushHost(ctx, delivery)
			}
		})
	}
sendLoop:
	for _, job := range eligible {
		select {
		case queue <- job:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(queue)
	collectors.Wait()
	close(deliveries)
	senders.Wait()
}

func (a *API) livePushSubscriptions(ctx context.Context, userID string, subscriptions []store.PushSubscription) []store.PushSubscription {
	var live []store.PushSubscription
	for _, subscription := range subscriptions {
		if ctx.Err() != nil {
			break
		}
		current, err := a.store.PushSubscriptionByEndpoint(ctx, userID, subscription.Endpoint)
		if err == nil && current.ID == subscription.ID {
			live = append(live, current)
		}
	}
	return live
}
func (a *API) collectPushHost(ctx context.Context, job pushHostJob, state *pushPollState) []pushmessage.Notification {
	lease, err := a.access.attach(ctx, job.host.OwnerID, "")
	if err != nil {
		return nil
	}
	defer lease.release()
	lease.hostID.Store(&job.host.ID)
	ctx = lease.ctx
	if lease.check() != nil || len(a.livePushSubscriptions(ctx, job.host.OwnerID, job.subscriptions)) == 0 {
		return nil
	}
	pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint, err := a.hosts.Open(pollCtx, job.host)
	var snapshot herdr.Snapshot
	if err == nil {
		snapshot, err = endpoint.Snapshot(pollCtx)
		_ = endpoint.Close()
	}
	prefix := pushHostPrefix(job.host)
	if err != nil {
		if ctx.Err() == nil {
			state.mu.Lock()
			failure := state.failures[prefix]
			failure.attempts = min(failure.attempts+1, 4)
			failure.until = state.now().Add(min(time.Duration(1<<failure.attempts)*5*time.Second, time.Minute))
			state.failures[prefix] = failure
			state.mu.Unlock()
		}
		return nil
	}
	if lease.check() != nil {
		return nil
	}
	now := state.now()
	describe := pushDescriber(job.host, snapshot)
	var notifications []pushmessage.Notification
	var due []herdr.Agent
	state.mu.Lock()
	delete(state.failures, prefix)
	seen := make(map[string]bool)
	for _, agent := range snapshot.Agents {
		// Bound history independently of the number of transient panes ever seen.
		if len(seen) >= 8192 {
			break
		}
		key := prefix + agent.PaneID
		seen[key] = true
		old, exists := state.previous[key]
		if exists || len(state.previous) < 65536 {
			state.previous[key] = agent.AgentStatus
		}
		if agent.AgentStatus != "done" {
			// Working again, blocked or gone idle cancels a waiting done round.
			delete(state.pending, key)
		}
		switch {
		case !exists:
		case old != agent.AgentStatus && agent.AgentStatus == "blocked":
			title, body := describe(agent)
			notifications = append(notifications, pushNotification(job.host, agent, title+" 需要你", body))
		case old != agent.AgentStatus && agent.AgentStatus == "done":
			state.pending[key] = pushPendingDone{seq: agent.StateChangeSeq, due: now.Add(pushDoneDelay)}
		case agent.AgentStatus == "done":
			pending, ok := state.pending[key]
			if !ok {
				break
			}
			if pending.seq != agent.StateChangeSeq {
				// Another round finished between polls; its own delay starts now.
				state.pending[key] = pushPendingDone{seq: agent.StateChangeSeq, due: now.Add(pushDoneDelay)}
			} else if !now.Before(pending.due) {
				delete(state.pending, key)
				due = append(due, agent)
			}
		}
	}
	for key := range state.previous {
		if strings.HasPrefix(key, prefix) && !seen[key] {
			delete(state.previous, key)
			delete(state.pending, key)
		}
	}
	state.mu.Unlock()
	if len(due) == 0 {
		return notifications
	}
	// Read markers come from the database, so they are checked outside state.mu.
	markers, err := a.store.AgentSeenForUser(ctx, job.host.OwnerID)
	if err != nil {
		// Keep the rounds for the next poll instead of guessing they were unread.
		state.mu.Lock()
		for _, agent := range due {
			key := prefix + agent.PaneID
			if _, ok := state.pending[key]; !ok && state.previous[key] == "done" {
				state.pending[key] = pushPendingDone{seq: agent.StateChangeSeq, due: now}
			}
		}
		state.mu.Unlock()
		return notifications
	}
	for _, agent := range due {
		if marker, ok := markers[store.AgentSeenKey(job.host.ID, agent.PaneID)]; ok && marker.StateChangeSeq >= agent.StateChangeSeq {
			continue
		}
		title, body := describe(agent)
		notifications = append(notifications, pushNotification(job.host, agent, title+" 已完成", body))
	}
	return notifications
}

// pushDescriber returns the agent name and the "<pane title>\n<host> · <workspace> › <tab>"
// body. Every part is bounded: Web Push payloads are limited to about 4 KiB.
func pushDescriber(host store.Host, snapshot herdr.Snapshot) func(herdr.Agent) (string, string) {
	workspaces := make(map[string]string, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		workspaces[workspace.ID] = workspace.Label
	}
	tabs := make(map[string]string, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		tabs[tab.ID] = tab.Label
	}
	titles := make(map[string]string, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		titles[pane.ID] = strings.TrimSpace(pane.TerminalTitleStripped)
	}
	return func(agent herdr.Agent) (string, string) {
		name := firstNonEmpty(agent.Name, agent.Agent, "Agent")
		location := pushTruncate(host.Name, 60)
		if label := pushTruncate(workspaces[agent.WorkspaceID], 60); label != "" {
			location += " · " + label
			if tab := pushTruncate(tabs[agent.TabID], 60); tab != "" {
				location += " › " + tab
			}
		}
		if title := pushTruncate(titles[agent.PaneID], 120); title != "" {
			return pushTruncate(name, 60), title + "\n" + location
		}
		return pushTruncate(name, 60), location
	}
}

func pushNotification(host store.Host, agent herdr.Agent, title, body string) pushmessage.Notification {
	return pushmessage.Notification{Title: title, Body: body, Tag: pushHostPrefix(host) + agent.PaneID, Data: map[string]any{
		"url": "/h/" + host.ID + "?pane=" + url.QueryEscape(agent.PaneID), "host_id": host.ID, "pane_id": agent.PaneID, "status": agent.AgentStatus,
	}}
}

func pushTruncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
func (a *API) deliverPushHost(ctx context.Context, delivery pushDelivery) {
	job := delivery.job
	lease, err := a.access.attach(ctx, job.host.OwnerID, "")
	if err != nil {
		return
	}
	defer lease.release()
	lease.hostID.Store(&job.host.ID)
	ctx = lease.ctx
	for _, message := range delivery.messages {
		for _, subscription := range job.subscriptions {
			if lease.check() != nil {
				return
			}
			current, err := a.store.PushSubscriptionByEndpoint(ctx, job.host.OwnerID, subscription.Endpoint)
			if err != nil || current.ID != subscription.ID {
				continue
			}
			notifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			expired, notifyErr := a.push.Notify(notifyCtx, current, message)
			cancel()
			if expired {
				_ = a.store.DeletePushSubscriptionIfCurrent(ctx, current)
			} else if notifyErr != nil {
				a.logger.Debug("send Web Push", "error", notifyErr, "host_id", job.host.ID)
			}
		}
	}
}
