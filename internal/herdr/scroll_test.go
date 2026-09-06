package herdr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type controllerEndpoint struct {
	Endpoint
	mu        sync.Mutex
	snapshots int
	opens     []TerminalOpen
	processes []*controllerProcess
	reject    bool
	stall     bool
	opened    chan struct{}
}

func (e *controllerEndpoint) Snapshot(context.Context) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snapshots++
	return Snapshot{Protocol: 20, Panes: []Pane{{ID: "w1:p1"}}, Layouts: []Layout{{Panes: []LayoutPane{{PaneID: "w1:p1", Rect: Rect{Width: 122, Height: 42}}}}}}, nil
}
func (e *controllerEndpoint) TerminalGeometry(context.Context, string) (TerminalGeometry, error) {
	return TerminalGeometry{Cols: 120, Rows: 40, CellWidthPx: 8, CellHeightPx: 16}, nil
}
func (e *controllerEndpoint) OpenTerminalSocket(context.Context) (net.Conn, error) {
	reader, writer := io.Pipe()
	p := &controllerProcess{endpoint: e, reader: reader, writer: writer, closed: make(chan struct{})}
	e.mu.Lock()
	e.processes = append(e.processes, p)
	e.mu.Unlock()
	return p, nil
}

type controllerProcess struct {
	endpoint *controllerEndpoint
	reader   *io.PipeReader
	writer   *io.PipeWriter
	mu       sync.Mutex
	commands []map[string]any
	failed   bool
	closed   chan struct{}
	once     sync.Once
}

func (p *controllerProcess) Read(data []byte) (int, error) { return p.reader.Read(data) }
func (p *controllerProcess) Close() error {
	p.once.Do(func() { _ = p.writer.Close(); close(p.closed) })
	return nil
}
func (p *controllerProcess) LocalAddr() net.Addr              { return nil }
func (p *controllerProcess) RemoteAddr() net.Addr             { return nil }
func (p *controllerProcess) SetDeadline(time.Time) error      { return nil }
func (p *controllerProcess) SetReadDeadline(time.Time) error  { return nil }
func (p *controllerProcess) SetWriteDeadline(time.Time) error { return nil }
func (p *controllerProcess) input() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.commands...)
}
func (p *controllerProcess) respond(payload []byte) {
	go func() { _ = writeNativeMessage(p.writer, payload) }()
}
func (p *controllerProcess) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return 0, io.ErrClosedPipe
	}
	reader := bytes.NewReader(data)
	for reader.Len() > 0 {
		payload, err := readNativeMessage(reader)
		if err != nil {
			return 0, err
		}
		d := nativeDecoder{data: payload}
		switch d.uint() {
		case 0:
			_ = d.uint()
			cols, rows := d.uint(), d.uint()
			cellWidth, cellHeight := d.uint(), d.uint()
			if cellWidth != 8 || cellHeight != 16 {
				return 0, errors.New("source pixels were discarded")
			}
			p.endpoint.mu.Lock()
			p.endpoint.opens = append(p.endpoint.opens, TerminalOpen{PaneID: "w1:p1", Mode: "control", Cols: uint16(cols), Rows: uint16(rows)})
			p.endpoint.mu.Unlock()
			p.respond([]byte{0, 20, 1, 0})
		case 9:
			if p.endpoint.opened != nil {
				close(p.endpoint.opened)
			}
			if p.endpoint.reject {
				p.respond(appendNativeString([]byte{4, 1}, "already attached"))
			} else if !p.endpoint.stall {
				p.respond([]byte{2, 1, 120, 40, 1, 0})
			}
		case 6:
			_ = d.uint()
			direction := "up"
			if d.uint() == 1 {
				direction = "down"
			}
			lines := d.uint()
			_ = d.boolean()
			column := d.uint()
			_ = d.boolean()
			row := d.uint()
			p.commands = append(p.commands, map[string]any{"type": "terminal.scroll", "direction": direction, "lines": float64(lines), "column": float64(column), "row": float64(row)})
		case 4:
			p.commands = append(p.commands, map[string]any{"type": "terminal.release"})
			_ = p.Close()
		default:
			return 0, errors.New("unexpected native terminal input")
		}
	}
	return len(data), nil
}

func TestScrollControllerReusesBurstAndReleasesOnIdle(t *testing.T) {
	e := &controllerEndpoint{}
	s := NewScrollController(context.Background(), e, "w1:p1")
	defer s.Close()
	request, cancel := context.WithCancel(context.Background())
	if err := s.Send(request, -3, 200, 90); err != nil {
		t.Fatal(err)
	}
	cancel() // Ending an RPC must not end the controller shared by the burst.
	if err := s.Send(context.Background(), 6, 0, 0); err != nil {
		t.Fatal(err)
	}
	if e.snapshots != 1 || len(e.opens) != 1 {
		t.Fatalf("burst reopened: %d snapshots, %d opens", e.snapshots, len(e.opens))
	}
	if e.opens[0].Takeover || e.opens[0].Cols != 120 || e.opens[0].Rows != 40 {
		t.Fatalf("invalid control geometry: %+v", e.opens[0])
	}
	p := e.processes[0]
	commands := p.input()
	if len(commands) != 3 || commands[0]["direction"] != "up" || commands[1]["direction"] != "down" || commands[0]["column"] != float64(119) || commands[0]["row"] != float64(39) {
		t.Fatalf("incorrect ordered gesture data: %v", commands)
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("idle controller was retained")
	}
	commands = p.input()
	if commands[len(commands)-1]["type"] != "terminal.release" {
		t.Fatal("idle release was not ordered after input")
	}
	if err := s.Send(context.Background(), -3, 0, 0); err != nil {
		t.Fatal(err)
	}
	if len(e.opens) != 2 {
		t.Fatal("new gesture did not reconnect after idle")
	}
}

func TestScrollControllerCancellationAndRejectionSendNoInput(t *testing.T) {
	for _, mode := range []string{"access-canceled", "request-canceled", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			access, cancelAccess := context.WithCancel(context.Background())
			defer cancelAccess()
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			e := &controllerEndpoint{reject: mode == "rejected", stall: mode != "rejected", opened: make(chan struct{})}
			s := NewScrollController(access, e, "w1:p1")
			defer s.Close()
			done := make(chan error, 1)
			go func() { done <- s.Send(request, -3, 0, 0) }()
			<-e.opened
			if mode == "access-canceled" {
				cancelAccess()
			}
			if mode == "request-canceled" {
				cancelRequest()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled/rejected request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("pending controller ignored cancellation")
			}
			p := e.processes[0]
			select {
			case <-p.closed:
			case <-time.After(time.Second):
				t.Fatal("pending controller leaked")
			}
			if len(p.input()) != 0 || e.opens[0].Takeover {
				t.Fatal("input was sent before acceptance or control was taken over")
			}
		})
	}
}

func TestScrollControllerFailedWriteIsNotReplayedAndCloseIsFinal(t *testing.T) {
	e := &controllerEndpoint{}
	s := NewScrollController(context.Background(), e, "w1:p1")
	defer s.Close()
	if err := s.Send(context.Background(), -3, 0, 0); err != nil {
		t.Fatal(err)
	}
	p := e.processes[0]
	p.mu.Lock()
	p.failed = true
	p.mu.Unlock()
	if err := s.Send(context.Background(), -3, 0, 0); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected failed write, got %v", err)
	}
	if len(e.opens) != 1 || len(p.input()) != 1 {
		t.Fatal("failed gesture was replayed")
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("failed controller was left attached")
	}
	s.Close()
	if err := s.Send(context.Background(), -3, 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed stream accepted input: %v", err)
	}
	if len(e.opens) != 1 {
		t.Fatal("closed stream reopened control")
	}
}

func TestScrollControllerRejectsUnknownPaneAndInvalidGesture(t *testing.T) {
	for _, input := range []struct {
		pane          string
		lines, column int
	}{{"w9:p9", -3, 0}, {"w1:p1", 0, 0}, {"w1:p1", 101, 0}, {"w1:p1", 3, -1}} {
		e := &controllerEndpoint{}
		s := NewScrollController(context.Background(), e, input.pane)
		if err := s.Send(context.Background(), input.lines, input.column, 0); err == nil {
			t.Fatal("expected invalid gesture rejection")
		}
		s.Close()
		if len(e.opens) != 0 {
			t.Fatal("invalid scroll opened a controller")
		}
	}
}
