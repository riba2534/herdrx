package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/riba2534/herdrx/internal/herdr"
)

// 画面视图：工作台按 pane 轮询 Herdr 已渲染好的文本（pane.read，format=ansi），
// 只把变化的行推给浏览器。它只读画面，不打开观察流，不申请尺寸控制，也不改变
// 远端 PTY；输入仍走既有的 pane.send_input / 终端流。
//
// 固定使用 format=ansi：Herdr 只在 format=text 读取全屏 Agent 历史时才会向 PTY
// 注入滚轮来抓取更早内容，ansi 读取没有这种副作用。
const (
	screenDefaultLines = 200
	screenMinLines     = 20
	screenMaxLines     = 1000
	screenMaxWatches   = 4
	screenMaxLineRunes = 8192
	screenReadTimeout  = 5 * time.Second
	screenKickDelay    = 80 * time.Millisecond
	screenShareWindow  = 150 * time.Millisecond
)

type screenWatch struct {
	paneID string
	lines  int
	gen    uint64
	kick   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

type screenFrame struct {
	Type      string   `json:"t"`
	PaneID    string   `json:"pane_id"`
	Gen       uint64   `json:"gen"`
	Seq       uint64   `json:"seq"`
	Full      bool     `json:"full,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Drop      int      `json:"drop,omitempty"`
	Total     int      `json:"total"`
	Set       [][2]any `json:"set,omitempty"`
	Truncated bool     `json:"truncated"`
	Requested int      `json:"requested"`
}

// screenInterval 在画面持续变化时保持快速刷新，静止后逐步放慢。
func screenInterval(unchanged int) time.Duration {
	switch {
	case unchanged < 4:
		return 300 * time.Millisecond
	case unchanged < 12:
		return 700 * time.Millisecond
	default:
		return 1500 * time.Millisecond
	}
}

func screenBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return time.Second
	case failures <= 3:
		return 2 * time.Second
	default:
		return 5 * time.Second
	}
}

func clampScreenLines(lines int) int {
	if lines <= 0 {
		return screenDefaultLines
	}
	return min(max(lines, screenMinLines), screenMaxLines)
}

func (s *workbenchSession) watchScreen(message workbenchMessage) {
	if !validWorkbenchRef(message.PaneID, 1, 128) {
		s.writeRequestError(message.RequestID, "invalid_pane", "Pane 标识无效")
		return
	}
	lines := clampScreenLines(message.Lines)
	s.screenMu.Lock()
	if s.screenClosed {
		s.screenMu.Unlock()
		return
	}
	if s.screens == nil {
		s.screens = make(map[string]*screenWatch)
	}
	previous := s.screens[message.PaneID]
	if previous == nil && len(s.screens) >= screenMaxWatches {
		s.screenMu.Unlock()
		s.writeRequestError(message.RequestID, "screen_limit", "同一窗口最多同时查看 4 个画面，请先关闭其他画面视图")
		return
	}
	s.screenGen++
	ctx, cancel := context.WithCancel(s.ctx)
	watch := &screenWatch{paneID: message.PaneID, lines: lines, gen: s.screenGen, kick: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	s.screens[message.PaneID] = watch
	s.screenMu.Unlock()
	if previous != nil {
		// A replaced watcher may still finish one read; its frames carry the old
		// generation and the browser drops them, so the read loop never waits here.
		previous.cancel()
	}
	// The reply precedes the first frame, so the browser installs its view
	// before any lines of this generation arrive.
	_ = s.writer.JSON(s.ctx, map[string]any{"t": "screen.watching", "id": message.RequestID, "pane_id": message.PaneID, "lines": lines, "gen": watch.gen})
	go s.runScreen(ctx, watch)
}

func (s *workbenchSession) unwatchScreen(paneID string) {
	s.screenMu.Lock()
	watch := s.screens[paneID]
	delete(s.screens, paneID)
	s.screenMu.Unlock()
	if watch != nil {
		watch.cancel()
	}
}

func (s *workbenchSession) closeScreens() {
	s.screenMu.Lock()
	watches := s.screens
	s.screens = nil
	s.screenClosed = true
	s.screenMu.Unlock()
	for _, watch := range watches {
		watch.cancel()
	}
	for _, watch := range watches {
		<-watch.done
	}
}

// kickScreen asks a watched pane for a prompt reread after input reached it,
// so typed text and the program's reply appear without waiting a full period.
func (s *workbenchSession) kickScreen(paneID string) {
	s.screenMu.Lock()
	watch := s.screens[paneID]
	s.screenMu.Unlock()
	if watch == nil {
		return
	}
	select {
	case watch.kick <- struct{}{}:
	default:
	}
}

func (s *workbenchSession) runScreen(ctx context.Context, watch *screenWatch) {
	defer close(watch.done)
	var previous []string
	var seq uint64
	first, unchanged, failures := true, 0, 0
	lastError := ""
	var kickedAt time.Time
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch.kick:
			unchanged, kickedAt = 0, time.Now()
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(screenKickDelay)
			continue
		case <-timer.C:
		}
		if s.access.check() != nil {
			return
		}
		text, truncated, err := s.api.readScreen(ctx, s.hostID, s.endpoint, watch.paneID, watch.lines, kickedAt)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			code, message := screenErrorDetail(err)
			// Report each distinct failure once; a lasting outage must not flood the socket.
			if message != lastError {
				lastError = message
				_ = s.writer.JSON(s.ctx, map[string]any{"t": "screen.error", "pane_id": watch.paneID, "gen": watch.gen, "code": code, "message": message})
			}
			timer.Reset(screenBackoff(failures))
			continue
		}
		failures, lastError = 0, ""
		next := splitScreenLines(text)
		frame := screenFrame{Type: "screen", PaneID: watch.paneID, Gen: watch.gen, Truncated: truncated, Requested: watch.lines, Total: len(next)}
		changed := first
		if first {
			frame.Full, frame.Lines = true, next
		} else {
			frame.Drop, frame.Set, changed = diffScreenLines(previous, next)
			if changed && len(frame.Set) > len(next)*4/5 {
				frame.Full, frame.Lines, frame.Drop, frame.Set = true, next, 0, nil
			}
		}
		if changed {
			seq++
			frame.Seq = seq
			if err := s.writer.JSON(s.ctx, frame); err != nil {
				return
			}
			previous, unchanged = next, 0
		} else {
			unchanged++
		}
		first = false
		timer.Reset(screenInterval(unchanged))
	}
}

func screenErrorDetail(err error) (string, string) {
	var apiError *herdr.APIError
	if errors.As(err, &apiError) && strings.Contains(apiError.Code, "not_found") {
		return "pane_unavailable", "这个 Pane 已不存在，请切换到其他终端"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "screen_timeout", "读取画面超时，正在重试"
	}
	return "screen_unavailable", "暂时读不到画面，正在重试：" + err.Error()
}

// splitScreenLines turns Herdr's CRLF-separated ANSI text into lines. Herdr
// resets SGR at the start of every styled line, so each line renders alone and
// line-level diffs stay correct.
func splitScreenLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if utf8.RuneCountInString(line) > screenMaxLineRunes {
			runes := []rune(line)
			line = string(runes[:screenMaxLineRunes]) + "\x1b[0m…"
		}
		lines[index] = line
	}
	return lines
}

// diffScreenLines describes next relative to previous as "drop k lines from
// the top, then replace these indices". Appended output scrolls the window,
// so the best top offset is searched instead of comparing row by row.
func diffScreenLines(previous, next []string) (int, [][2]any, bool) {
	drop := bestScreenShift(previous, next)
	base := previous[drop:]
	var set [][2]any
	for index, line := range next {
		if index >= len(base) || base[index] != line {
			set = append(set, [2]any{index, line})
		}
	}
	changed := drop > 0 || len(set) > 0 || len(base) != len(next)
	return drop, set, changed
}

func bestScreenShift(previous, next []string) int {
	if len(next) == 0 {
		return 0
	}
	best, bestCost := 0, screenShiftCost(previous, next, 0)
	candidates := 0
	for shift := 1; shift < len(previous) && candidates < 64; shift++ {
		if previous[shift] != next[0] {
			continue
		}
		candidates++
		if cost := screenShiftCost(previous, next, shift); cost < bestCost {
			best, bestCost = shift, cost
		}
	}
	if cost := len(next); cost < bestCost {
		best = len(previous)
	}
	return best
}

func screenShiftCost(previous, next []string, shift int) int {
	cost := 0
	for index, line := range next {
		if shift+index >= len(previous) || previous[shift+index] != line {
			cost++
		}
	}
	return cost
}

// screenReads lets several windows watching one pane share a single read. A
// read is reused only if it started no earlier than the caller allows: normal
// polls accept anything younger than screenShareWindow, while a reread after
// input only accepts a read that began after that input.
type screenReads struct {
	mu      sync.Mutex
	entries map[screenReadKey]*screenReadEntry
}

type screenReadKey struct {
	hostID string
	paneID string
	lines  int
}

type screenReadEntry struct {
	done    chan struct{}
	started time.Time
	settled time.Time
	result  screenReadResult
}

type screenReadResult struct {
	text      string
	truncated bool
	err       error
	// shared marks a result produced by another window's read.
	shared bool
}

func (a *API) readScreen(ctx context.Context, hostID string, endpoint herdr.Endpoint, paneID string, lines int, notBefore time.Time) (string, bool, error) {
	key := screenReadKey{hostID: hostID, paneID: paneID, lines: lines}
	fetch := func(readCtx context.Context) screenReadResult {
		text, truncated, err := callScreenRead(readCtx, endpoint, paneID, lines)
		return screenReadResult{text: text, truncated: truncated, err: err}
	}
	if floor := time.Now().Add(-screenShareWindow); notBefore.Before(floor) {
		notBefore = floor
	}
	result := a.screenReads.read(ctx, key, notBefore, fetch)
	// Another window's canceled read must not become this window's failure.
	if result.shared && ctx.Err() == nil && errors.Is(result.err, context.Canceled) {
		result = a.screenReads.read(ctx, key, time.Now(), fetch)
	}
	return result.text, result.truncated, result.err
}

func (r *screenReads) read(ctx context.Context, key screenReadKey, notBefore time.Time, fetch func(context.Context) screenReadResult) screenReadResult {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[screenReadKey]*screenReadEntry)
	}
	now := time.Now()
	for other, entry := range r.entries {
		if !entry.settled.IsZero() && now.Sub(entry.settled) > time.Second {
			delete(r.entries, other)
		}
	}
	if entry := r.entries[key]; entry != nil && !entry.started.Before(notBefore) {
		r.mu.Unlock()
		select {
		case <-entry.done:
			result := entry.result
			result.shared = true
			return result
		case <-ctx.Done():
			return screenReadResult{err: ctx.Err(), shared: true}
		}
	}
	entry := &screenReadEntry{done: make(chan struct{}), started: now}
	r.entries[key] = entry
	r.mu.Unlock()

	readCtx, cancel := context.WithTimeout(ctx, screenReadTimeout)
	result := fetch(readCtx)
	cancel()
	r.mu.Lock()
	entry.result, entry.settled = result, time.Now()
	if ctx.Err() != nil && r.entries[key] == entry {
		delete(r.entries, key)
	}
	r.mu.Unlock()
	close(entry.done)
	return result
}

func callScreenRead(ctx context.Context, endpoint herdr.Endpoint, paneID string, lines int) (string, bool, error) {
	raw, err := endpoint.Call(ctx, "pane.read", map[string]any{"pane_id": paneID, "source": "recent_unwrapped", "format": "ansi", "lines": lines})
	if err != nil {
		return "", false, err
	}
	var result struct {
		Read struct {
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		} `json:"read"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", false, err
	}
	return result.Read.Text, result.Read.Truncated, nil
}

// screenInputPane extracts the target pane of an input call so the watcher can
// reread right after the keys land.
func screenInputPane(method string, params any) string {
	switch method {
	case "pane.send_input", "pane.send_text", "pane.send_keys", "agent.prompt", "agent.send_keys":
	default:
		return ""
	}
	values, ok := params.(map[string]any)
	if !ok {
		return ""
	}
	paneID, _ := values["pane_id"].(string)
	return paneID
}
