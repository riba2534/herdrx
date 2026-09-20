package herdr

import (
	"context"
	"fmt"
	"io"
	"time"
)

// ViewportObserver changes only the read-only viewer's render area. It never
// attaches a controller, resizes the PTY, sends input or claims shell focus.
type ViewportObserver interface {
	ResizeViewport(context.Context, uint16, uint16) error
}

// OpenViewportObserver is the narrow protocol-20/22 equivalent of CLI observe,
// with the additional ability to follow the actual terminal's geometry without
// restarting the observer. The CLI observe command does not consume stdin.
func OpenViewportObserver(ctx context.Context, endpoint NativeScrollEndpoint, version int, paneID string, geometry TerminalGeometry) (TerminalProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !publicID.MatchString(paneID) {
		return nil, fmt.Errorf("invalid terminal observation target")
	}
	protocol, err := nativeScrollVersion(version)
	if err != nil {
		return nil, fmt.Errorf("Herdr 协议 %d 尚不支持动态观察视口", version)
	}
	if err := geometry.Validate(); err != nil {
		return nil, fmt.Errorf("无法可靠读取观察视口尺寸: %w", err)
	}
	conn, err := endpoint.OpenTerminalSocket(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	handshakeTimer := time.AfterFunc(5*time.Second, func() { _ = conn.Close() })
	defer handshakeTimer.Stop()
	ok := false
	defer func() {
		if !ok {
			stop()
			_ = conn.Close()
		}
	}()
	hello := nativeValues(0, uint64(version), uint64(geometry.Cols), uint64(geometry.Rows), uint64(geometry.CellWidthPx), uint64(geometry.CellHeightPx))
	observeTag := uint64(7)
	if version == 20 {
		hello = append(hello, 1, 0, 2) // TerminalAnsi, no keybindings, TerminalAttach intent.
		observeTag = 8
	} else {
		hello = append(hello, 0) // Exact pixel mouse is not claimed.
	}
	if err := writeNativeMessage(conn, hello); err != nil {
		return nil, fmt.Errorf("open native observer: %w", err)
	}
	welcome, err := readNativeMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("read native observer welcome: %w", err)
	}
	if err := protocol.welcome(welcome); err != nil {
		return nil, err
	}
	// ObserveTerminal has no takeover field. No AttachTerminal/ControlTerminal
	// message is available through this adapter.
	if err := writeNativeMessage(conn, appendNativeString(nativeValues(observeTag), paneID)); err != nil {
		return nil, fmt.Errorf("attach native observer: %w", err)
	}
	reader, writer := io.Pipe()
	base := &nativeScrollProcess{conn: conn, reader: reader, writer: writer, stop: stop, done: make(chan struct{})}
	process := &nativeViewportProcess{nativeScrollProcess: base, version: version, cellWidth: geometry.CellWidthPx, cellHeight: geometry.CellHeightPx}
	ok = true
	go base.readOutput(protocol)
	return process, nil
}

type nativeViewportProcess struct {
	*nativeScrollProcess
	version               int
	cellWidth, cellHeight uint32
}

func (p *nativeViewportProcess) Stdin() io.WriteCloser { return readonlyViewportInput{p} }

func (p *nativeViewportProcess) ResizeViewport(ctx context.Context, cols, rows uint16) error {
	if cols < 10 || cols > 1000 || rows < 3 || rows > 500 {
		return fmt.Errorf("invalid observer viewport size")
	}
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	defer stop()
	// Client Resize applies to TerminalObserve's render state only. Keep the
	// acquired cell dimensions; no browser CSS/DPR estimate enters this channel.
	payload := nativeValues(3, uint64(cols), uint64(rows), uint64(p.cellWidth), uint64(p.cellHeight))
	if p.version == 22 {
		payload = append(payload, 0)
	}
	if err := writeNativeMessage(p.conn, payload); err != nil {
		return fmt.Errorf("resize observer viewport: %w", err)
	}
	return nil
}

type readonlyViewportInput struct{ process *nativeViewportProcess }

func (readonlyViewportInput) Write([]byte) (int, error) {
	return 0, fmt.Errorf("read-only observer does not accept terminal input or control commands")
}
func (input readonlyViewportInput) Close() error { return input.process.Close() }
