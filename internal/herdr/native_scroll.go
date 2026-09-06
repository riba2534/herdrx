package herdr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// Herdr's direct-terminal protocol is private and versioned. The CLI's control
// handshake discards pixel geometry; this narrow adapter preserves the PTY's
// existing geometry and leaves wheel mode selection to Herdr. It deliberately
// rejects versions whose exact wire schema has not been reviewed.
type nativeScrollProtocol struct {
	version                      int
	controlTag, frameTag, endTag uint64
}

func nativeScrollVersion(version int) (nativeScrollProtocol, error) {
	switch version {
	case 20:
		return nativeScrollProtocol{20, 9, 2, 4}, nil
	case 22:
		return nativeScrollProtocol{22, 8, 1, 3}, nil
	default:
		return nativeScrollProtocol{}, fmt.Errorf("Herdr 协议版本 %d 尚不支持保留终端尺寸的滚动，请更新工作台及受控端 CLI", version)
	}
}

func openNativeScroll(ctx context.Context, endpoint NativeScrollEndpoint, protocol nativeScrollProtocol, paneID string, geometry TerminalGeometry) (TerminalProcess, error) {
	if err := geometry.Validate(); err != nil {
		return nil, fmt.Errorf("无法可靠读取终端尺寸，请检查受控端 CLI 和终端进程: %w", err)
	}
	conn, err := endpoint.OpenTerminalSocket(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	ok := false
	defer func() {
		if !ok {
			stop()
			_ = conn.Close()
		}
	}()
	// Version 20: Hello, TerminalAnsi, server keybindings, TerminalAttach.
	// Version 22: TerminalHello with pixel mouse disabled; cell pixels are still
	// preserved for the child. Neither handshake claims a shell surface.
	hello := nativeValues(0, uint64(protocol.version), uint64(geometry.Cols), uint64(geometry.Rows), uint64(geometry.CellWidthPx), uint64(geometry.CellHeightPx))
	if protocol.version == 20 {
		hello = append(hello, 1, 0, 2)
	} else {
		hello = append(hello, 0)
	}
	if err := writeNativeMessage(conn, hello); err != nil {
		return nil, fmt.Errorf("open native scroll: %w", err)
	}
	welcome, err := readNativeMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("read native scroll welcome: %w", err)
	}
	if err := protocol.welcome(welcome); err != nil {
		return nil, err
	}
	control := nativeValues(protocol.controlTag)
	control = appendNativeString(control, paneID)
	control = append(control, 0) // takeover=false, including when another client owns it.
	if err := writeNativeMessage(conn, control); err != nil {
		return nil, fmt.Errorf("attach native scroll: %w", err)
	}
	reader, writer := io.Pipe()
	process := &nativeScrollProcess{conn: conn, reader: reader, writer: writer, stop: stop, done: make(chan struct{})}
	ok = true
	go process.readOutput(protocol)
	return process, nil
}

func (p nativeScrollProtocol) welcome(payload []byte) error {
	d := nativeDecoder{data: payload}
	tag, version, encoding := d.uint(), d.uint(), d.uint()
	refused := d.boolean()
	reason := ""
	if refused {
		reason = string(d.blob())
	}
	if err := d.finish(); err != nil || tag != 0 || version != uint64(p.version) || encoding != 1 {
		return fmt.Errorf("Herdr 返回了不兼容的终端协议，已停止滚动连接")
	}
	if refused {
		return fmt.Errorf("Herdr refused terminal scroll: %s", reason)
	}
	return nil
}

type nativeScrollProcess struct {
	conn   net.Conn
	reader *io.PipeReader
	writer *io.PipeWriter
	stop   func() bool
	done   chan struct{}
	err    error // Published by closing done.
	once   sync.Once

	inputMu     sync.Mutex
	inputClosed bool
}

func (p *nativeScrollProcess) Stdout() io.Reader     { return p.reader }
func (p *nativeScrollProcess) Stdin() io.WriteCloser { return nativeScrollInput{p} }
func (p *nativeScrollProcess) Wait() error           { <-p.done; return p.err }
func (p *nativeScrollProcess) Close() error {
	p.once.Do(func() {
		_ = p.conn.Close()
		_ = p.reader.Close()
		_ = p.writer.Close()
	})
	return nil
}

func (p *nativeScrollProcess) readOutput(protocol nativeScrollProtocol) {
	defer func() {
		p.stop()
		_ = p.conn.Close()
		_ = p.writer.CloseWithError(p.err)
		close(p.done)
	}()
	encoder := json.NewEncoder(p.writer)
	for {
		payload, err := readNativeMessage(p.conn)
		if err != nil {
			if err != io.EOF {
				p.err = err
			}
			return
		}
		d := nativeDecoder{data: payload}
		switch d.uint() {
		case protocol.frameTag:
			seq, width, height := d.uint(), d.uint(), d.uint()
			full, data := d.boolean(), d.blob()
			if err := d.finish(); err != nil || width < 4 || width > 1000 || height < 2 || height > 500 {
				p.err = fmt.Errorf("invalid Herdr native terminal frame")
				return
			}
			// Encoding []byte as JSON preserves the CLI's base64 envelope exactly.
			p.err = encoder.Encode(struct {
				Type     string `json:"type"`
				Seq      uint64 `json:"seq"`
				Encoding string `json:"encoding"`
				Width    uint64 `json:"width"`
				Height   uint64 `json:"height"`
				Full     bool   `json:"full"`
				Bytes    []byte `json:"bytes"`
			}{"terminal.frame", seq, "ansi", width, height, full, data})
		case protocol.endTag:
			reason := d.optionalString()
			if err := d.finish(); err != nil {
				p.err = err
				return
			}
			p.err = encoder.Encode(map[string]any{"type": "terminal.closed", "reason": reason})
			return
		default:
			// Theme, cursor-mode and notification messages are not rendered by this
			// controller. The independent observer remains the displayed stream.
			if d.err != nil {
				p.err = d.err
				return
			}
		}
		if p.err != nil {
			return
		}
	}
}

type nativeScrollInput struct{ process *nativeScrollProcess }

func (input nativeScrollInput) Write(data []byte) (int, error) {
	p := input.process
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if p.inputClosed {
		return 0, io.ErrClosedPipe
	}
	var wire bytes.Buffer
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var command struct {
			Type      string `json:"type"`
			Direction string `json:"direction"`
			Lines     uint16 `json:"lines"`
			Source    string `json:"source"`
			Column    uint16 `json:"column"`
			Row       uint16 `json:"row"`
		}
		if err := decoder.Decode(&command); err == io.EOF {
			break
		} else if err != nil {
			return 0, fmt.Errorf("invalid native scroll input: %w", err)
		}
		var payload []byte
		switch command.Type {
		case "terminal.release":
			payload = []byte{4}
		case "terminal.scroll":
			if command.Lines == 0 || command.Lines > 100 || command.Source != "wheel" || (command.Direction != "up" && command.Direction != "down") {
				return 0, fmt.Errorf("invalid native scroll gesture")
			}
			direction := uint64(0)
			if command.Direction == "down" {
				direction = 1
			}
			// AttachScroll, Wheel, direction, lines, Some(column), Some(row), modifiers.
			payload = nativeValues(6, 0, direction, uint64(command.Lines), 1, uint64(command.Column), 1, uint64(command.Row), 0)
		default:
			return 0, fmt.Errorf("native scroll does not accept %q", command.Type)
		}
		_ = writeNativeMessage(&wire, payload)
	}
	if err := writeNativeBytes(p.conn, wire.Bytes()); err != nil {
		return 0, err // A partial submission is uncertain and must never be replayed.
	}
	return len(data), nil
}

func (input nativeScrollInput) Close() error {
	p := input.process
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if p.inputClosed {
		return nil
	}
	p.inputClosed = true
	// Detach precedes Close on the same stream. A transport without half-close
	// still ends when Herdr handles Detach; do not close before that ordering.
	if half, ok := p.conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return nil
}

const maxNativeMessage = 32 << 20

func writeNativeBytes(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func writeNativeMessage(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxNativeMessage {
		return fmt.Errorf("invalid native terminal message length")
	}
	packet := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(packet, uint32(len(payload)))
	copy(packet[4:], payload)
	return writeNativeBytes(writer, packet)
}

func readNativeMessage(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxNativeMessage {
		return nil, fmt.Errorf("invalid native terminal message length: %d", size)
	}
	data := make([]byte, int(size))
	_, err := io.ReadFull(reader, data)
	return data, err
}

func nativeValues(values ...uint64) []byte {
	var encoded []byte
	for _, value := range values {
		switch {
		case value < 251:
			encoded = append(encoded, byte(value))
		case value <= 65535:
			encoded = binary.LittleEndian.AppendUint16(append(encoded, 251), uint16(value))
		case value <= 1<<32-1:
			encoded = binary.LittleEndian.AppendUint32(append(encoded, 252), uint32(value))
		default:
			encoded = binary.LittleEndian.AppendUint64(append(encoded, 253), value)
		}
	}
	return encoded
}

func appendNativeString(data []byte, value string) []byte {
	data = append(data, nativeValues(uint64(len(value)))...)
	return append(data, value...)
}

type nativeDecoder struct {
	data []byte
	err  error
}

func (d *nativeDecoder) take(size int) []byte {
	if d.err != nil || size < 0 || size > len(d.data) {
		d.err = io.ErrUnexpectedEOF
		return nil
	}
	data := d.data[:size]
	d.data = d.data[size:]
	return data
}
func (d *nativeDecoder) uint() uint64 {
	first := d.take(1)
	if first == nil {
		return 0
	}
	switch first[0] {
	case 251:
		if data := d.take(2); data != nil {
			return uint64(binary.LittleEndian.Uint16(data))
		}
	case 252:
		if data := d.take(4); data != nil {
			return uint64(binary.LittleEndian.Uint32(data))
		}
	case 253:
		if data := d.take(8); data != nil {
			return binary.LittleEndian.Uint64(data)
		}
	case 254, 255:
		d.err = fmt.Errorf("unsupported native integer")
	default:
		return uint64(first[0])
	}
	return 0
}
func (d *nativeDecoder) boolean() bool {
	data := d.take(1)
	if data == nil {
		return false
	}
	if data[0] > 1 {
		d.err = fmt.Errorf("invalid native boolean")
	}
	return data[0] == 1
}
func (d *nativeDecoder) blob() []byte {
	size := d.uint()
	if size > uint64(len(d.data)) {
		d.err = io.ErrUnexpectedEOF
		return nil
	}
	return d.take(int(size))
}
func (d *nativeDecoder) optionalString() string {
	if !d.boolean() {
		return ""
	}
	return string(d.blob())
}
func (d *nativeDecoder) finish() error {
	if d.err != nil {
		return d.err
	}
	if len(d.data) != 0 {
		return fmt.Errorf("trailing native terminal data")
	}
	return nil
}
