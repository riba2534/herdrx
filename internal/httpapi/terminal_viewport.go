package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/riba2534/herdrx/internal/herdr"
)

type terminalSize struct{ cols, rows uint16 }

// CLI observe renders a fixed canvas, not the current PTY grid. A narrow native
// observer resizes that canvas without acquiring control. If source geometry is
// unavailable, legacy observation remains available but cannot control sizing.
func (s *workbenchSession) openObserver(stream *terminalStream, protocol int, cols, rows uint16) (herdr.TerminalProcess, error) {
	if native, ok := s.endpoint.(herdr.NativeScrollEndpoint); ok {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		geometry, err := native.TerminalGeometry(ctx, stream.paneID)
		cancel()
		if err == nil {
			ctx, cancel := context.WithCancel(s.ctx)
			timer := time.AfterFunc(5*time.Second, cancel)
			process, err := herdr.OpenViewportObserver(ctx, native, protocol, stream.paneID, geometry)
			timer.Stop()
			if err != nil {
				cancel()
				return nil, err
			}
			stream.viewportCancel = cancel
			stream.viewportRequests = make(chan terminalSize, 1)
			return process, nil
		}
	}
	return s.endpoint.OpenTerminal(s.ctx, herdr.TerminalOpen{PaneID: stream.paneID, Mode: "observe", Cols: cols, Rows: rows})
}
func (s *workbenchSession) startViewportSync(stream *terminalStream) {
	observer, ok := stream.process.(herdr.ViewportObserver)
	if !ok {
		return
	}
	if stream.viewportRequests == nil {
		stream.viewportRequests = make(chan terminalSize, 1)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	originalCancel := stream.viewportCancel
	stream.viewportCancel = func() {
		cancel()
		if originalCancel != nil {
			originalCancel()
		}
	}
	go func() {
		var last terminalSize
		for {
			select {
			case <-ctx.Done():
				return
			case size := <-stream.viewportRequests:
				// Coalesce targets accumulated while the previous write was in flight.
				select {
				case size = <-stream.viewportRequests:
				default:
				}
				if size == last {
					continue
				}
				if stream.closed.Load() {
					return
				}
				callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
				err := observer.ResizeViewport(callCtx, size.cols, size.rows)
				callCancel()
				if err != nil {
					if ctx.Err() == nil && s.closeStream(stream.id) {
						_ = s.writer.JSON(s.ctx, map[string]any{"t": "terminal.closed", "stream_id": stream.id, "reason": fmt.Sprintf("观察画布同步失败，请重新连接：%v", err)})
					}
					return
				}
				last = size
			}
		}
	}()
}
func (stream *terminalStream) queueViewport(size terminalSize) {
	if stream.viewportRequests == nil || stream.closed.Load() {
		return
	}
	select {
	case stream.viewportRequests <- size:
		return
	default:
	}
	select {
	case <-stream.viewportRequests:
	default:
	}
	select {
	case stream.viewportRequests <- size:
	default:
	}
}

// The current direct controller's rendered canvas follows its PTY resize. Keep
// all observers in that canvas; this callback cannot issue task resize commands.
func (e *geometryEntry) controllerViewport(l *geometryLease, cols, rows uint16) {
	e.mu.Lock()
	if e.lease != l || l.revoked.Load() {
		e.mu.Unlock()
		return
	}
	size := terminalSize{cols, rows}
	e.controllerSize = size
	for stream := range e.watchers {
		stream.queueViewport(size)
	}
	e.mu.Unlock()
}

func (s *workbenchSession) refreshViewports() {
	s.streamsMu.Lock()
	streams := make([]*terminalStream, 0, len(s.streams))
	for _, stream := range s.streams {
		if stream.viewportRequests != nil {
			streams = append(streams, stream)
		}
	}
	s.streamsMu.Unlock()
	for _, stream := range streams {
		e := stream.geometry
		if e == nil {
			continue
		}
		e.mu.Lock()
		if e.lease != nil || e.probing || time.Since(e.lastProbe) < 1500*time.Millisecond {
			e.mu.Unlock()
			continue
		}
		e.probing = true
		e.lastProbe = time.Now()
		revision := e.viewportRevision
		e.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			defer cancel()
			native, ok := s.endpoint.(herdr.NativeScrollEndpoint)
			if !ok {
				e.mu.Lock()
				e.probing = false
				e.mu.Unlock()
				return
			}
			geometry, err := native.TerminalGeometry(ctx, stream.paneID)
			runtime := s.runtimeGeneration()
			e.mu.Lock()
			defer e.mu.Unlock()
			e.probing = false
			if err != nil || s.ctx.Err() != nil || e.lease != nil || e.viewportRevision != revision || stream.closed.Load() || e.key.runtime != runtime {
				return
			}
			for target := range e.watchers {
				target.queueViewport(terminalSize{geometry.Cols, geometry.Rows})
			}
		}()
	}
}
