package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// ScrollController reuses Herdr's native wheel router for one Web terminal
// stream. It connects only on a gesture and relinquishes control after a short
// idle gap. The observer and remote task have independent lifetimes.
type ScrollController struct {
	ctx         context.Context
	cancel      context.CancelFunc
	endpoint    Endpoint
	paneID      string
	mu          sync.Mutex
	connection  *scrollConnection
	idle        *time.Timer
	generation  uint64
	idleTimeout time.Duration
}

type scrollConnection struct {
	process TerminalProcess
	cancel  context.CancelFunc
	rect    Rect
	ready   chan error
	done    chan struct{}
}

func NewScrollController(ctx context.Context, endpoint Endpoint, paneID string) *ScrollController {
	ctx, cancel := context.WithCancel(ctx)
	return &ScrollController{ctx: ctx, cancel: cancel, endpoint: endpoint, paneID: paneID, idleTimeout: 250 * time.Millisecond}
}

// Send writes a gesture without waiting for an output frame or a controller
// detach round trip. Completion means submitted, not acknowledged by the app;
// a failed or interrupted write must never be retried automatically.
func (s *ScrollController) Send(ctx context.Context, lines, column, row int) error {
	if !publicID.MatchString(s.paneID) || lines == 0 || lines < -100 || lines > 100 || column < 0 || row < 0 {
		return fmt.Errorf("invalid terminal scroll")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.connection != nil {
		select {
		case <-s.connection.done:
			s.connection = nil
		default:
		}
	}
	if s.connection == nil {
		connection, err := s.open(ctx)
		if err != nil {
			return err
		}
		s.connection = connection
	}
	connection := s.connection
	stop := context.AfterFunc(ctx, connection.cancel)
	defer stop()
	direction := "down"
	if lines < 0 {
		direction, lines = "up", -lines
	}
	var batch bytes.Buffer
	encoder := json.NewEncoder(&batch)
	for lines > 0 {
		step := min(lines, 3)
		_ = encoder.Encode(map[string]any{"type": "terminal.scroll", "direction": direction, "lines": step, "source": "wheel", "column": min(column, connection.rect.Width-1), "row": min(row, connection.rect.Height-1)})
		lines -= step
	}
	n, err := connection.process.Stdin().Write(batch.Bytes())
	if err == nil && n != batch.Len() {
		err = io.ErrShortWrite
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if s.ctx.Err() != nil {
		err = s.ctx.Err()
	}
	if err != nil {
		connection.cancel()
		s.connection = nil
		return fmt.Errorf("send terminal scroll: %w", err)
	}
	// An expired timer may already be waiting on mu; generation prevents it
	// from closing a controller that a newer gesture is still using.
	s.generation++
	generation := s.generation
	if s.idle != nil {
		s.idle.Stop()
	}
	s.idle = time.AfterFunc(s.idleTimeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.connection == connection && s.generation == generation {
			s.releaseLocked()
		}
	})
	return nil
}

func (s *ScrollController) open(ctx context.Context) (*scrollConnection, error) {
	connectionCtx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	snapshot, err := s.endpoint.Snapshot(connectionCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	protocol, err := nativeScrollVersion(snapshot.Protocol)
	if err != nil {
		cancel()
		return nil, err
	}
	found := false
	for _, pane := range snapshot.Panes {
		if pane.ID == s.paneID {
			found = true
		}
	}
	if !found {
		cancel()
		return nil, fmt.Errorf("terminal pane is unavailable")
	}
	native, ok := s.endpoint.(NativeScrollEndpoint)
	if !ok {
		cancel()
		return nil, fmt.Errorf("当前主机连接不支持保留终端尺寸的滚动，请更新工作台及受控端 CLI")
	}
	// Layout rectangles include pane chrome and can differ from the live PTY.
	// Read all four dimensions at the source before attaching. No CLI fallback:
	// it discards cell pixels and can trigger application redraws on every wheel.
	// Herdr has no atomic preserve-size attach: a native window resize between
	// this read and attach is still possible. Input is never retried to hide it.
	geometry, err := native.TerminalGeometry(connectionCtx, s.paneID)
	if err != nil {
		cancel()
		return nil, err
	}
	process, err := openNativeScroll(connectionCtx, native, protocol, s.paneID, geometry)
	if err != nil {
		cancel()
		return nil, err
	}
	rect := Rect{Width: int(geometry.Cols), Height: int(geometry.Rows)}
	connection := &scrollConnection{process: process, cancel: cancel, rect: rect, ready: make(chan error, 1), done: make(chan struct{})}
	go connection.drain(connectionCtx)
	select {
	case err := <-connection.ready:
		if err == nil && ctx.Err() == nil && s.ctx.Err() == nil {
			return connection, nil
		}
		cancel()
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		cancel()
	case <-s.ctx.Done():
		cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, s.ctx.Err()
}

func (c *scrollConnection) drain(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = c.process.Close() })
	defer func() {
		_ = c.process.Close()
		_ = c.process.Wait()
		stop()
		c.cancel()
		close(c.done)
	}()
	scanner := bufio.NewScanner(c.process.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	accepted := false
	var err error
	for scanner.Scan() {
		// Output is drained continuously so the controller never blocks Herdr's
		// renderer. The existing Web observer is the sole displayed stream.
		if accepted {
			continue
		}
		var frame struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			err = fmt.Errorf("invalid Herdr terminal response")
			break
		}
		if frame.Type == "terminal.closed" {
			err = fmt.Errorf("terminal scroll unavailable: %s", frame.Reason)
			break
		}
		if !accepted && frame.Type == "terminal.frame" {
			accepted = true
			c.ready <- nil
		}
	}
	if !accepted {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if scanner.Err() != nil {
			err = scanner.Err()
		} else if err == nil {
			err = fmt.Errorf("terminal scroll ended before the controller was ready")
		}
		c.ready <- err
	}
}

// releaseLocked preserves the final submitted gesture by sending release after
// it on the same pipe and waiting for EOF. The timeout bounds a broken transport.
func (s *ScrollController) releaseLocked() {
	connection := s.connection
	s.connection = nil
	if connection == nil {
		return
	}
	deadline := time.AfterFunc(time.Second, connection.cancel)
	_ = json.NewEncoder(connection.process.Stdin()).Encode(map[string]any{"type": "terminal.release"})
	_ = connection.process.Stdin().Close()
	<-connection.done
	deadline.Stop()
}

// Close releases access only. It is safe with an in-progress Send or idle timer.
func (s *ScrollController) Close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
	}
	if s.connection != nil {
		s.connection.cancel()
		<-s.connection.done
		s.connection = nil
	}
}
