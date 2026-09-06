package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type nativeTestEndpoint struct{ conn net.Conn }

func (e nativeTestEndpoint) TerminalGeometry(context.Context, string) (TerminalGeometry, error) {
	return TerminalGeometry{Cols: 134, Rows: 49, CellWidthPx: 8, CellHeightPx: 16}, nil
}
func (e nativeTestEndpoint) OpenTerminalSocket(context.Context) (net.Conn, error) {
	return e.conn, nil
}

// These packets are independent fixtures from the bincode schemas in Herdr
// v0.8.2 (protocol 20) and protocol 22. The protocol-20 Hello was also captured
// from the installed CLI against an isolated fake Unix socket.
func TestNativeScrollWireProtocols(t *testing.T) {
	for _, fixture := range []struct {
		version        int
		hello, control string
		welcome, frame string
	}{
		{20, "09000000001486310810010002", "08000000090577313a703100", "0400000000140100", "0b00000002018631010568656c6c6f"},
		{22, "0700000000168631081000", "08000000080577313a703100", "0400000000160100", "0b00000001018631010568656c6c6f"},
	} {
		t.Run(fmt.Sprint(fixture.version), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			endpoint := nativeTestEndpoint{client}
			result := make(chan error, 1)
			go func() {
				defer server.Close()
				check := func(golden string) error {
					expected, _ := hex.DecodeString(golden)
					actual := make([]byte, len(expected))
					if _, err := io.ReadFull(server, actual); err != nil {
						return err
					}
					if !bytes.Equal(actual, expected) {
						return fmt.Errorf("wire mismatch: %x != %x", actual, expected)
					}
					return nil
				}
				if err := check(fixture.hello); err != nil {
					result <- err
					return
				}
				welcome, _ := hex.DecodeString(fixture.welcome)
				_, _ = server.Write(welcome)
				if err := check(fixture.control); err != nil {
					result <- err
					return
				}
				frame, _ := hex.DecodeString(fixture.frame)
				_, _ = server.Write(frame)
				if err := check("0900000006000003010a010500"); err != nil {
					result <- err
					return
				}
				if err := check("0900000006000103010a010500"); err != nil {
					result <- err
					return
				}
				result <- check("0100000004")
			}()
			protocol, _ := nativeScrollVersion(fixture.version)
			geometry, _ := endpoint.TerminalGeometry(ctx, "w1:p1")
			process, err := openNativeScroll(ctx, endpoint, protocol, "w1:p1", geometry)
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			scanner := bufio.NewScanner(process.Stdout())
			if !scanner.Scan() {
				t.Fatal("missing terminal frame", scanner.Err())
			}
			var frame struct {
				Type          string `json:"type"`
				Width, Height int
				Full          bool
				Bytes         []byte
			}
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil || frame.Type != "terminal.frame" || frame.Width != 134 || frame.Height != 49 || !frame.Full || string(frame.Bytes) != "hello" {
				t.Fatalf("invalid translated frame: %+v, %v", frame, err)
			}
			for _, direction := range []string{"up", "down"} {
				if _, err := fmt.Fprintf(process.Stdin(), `{"type":"terminal.scroll","direction":%q,"lines":3,"source":"wheel","column":10,"row":5}`+"\n", direction); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := io.WriteString(process.Stdin(), "{\"type\":\"terminal.release\"}\n"); err != nil {
				t.Fatal(err)
			}
			_ = process.Stdin().Close()
			if err := process.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeScrollRejectsIncompatibleWelcomeBeforeAttach(t *testing.T) {
	for _, payload := range [][]byte{
		{0, 21, 1, 0},    // version mismatch
		{0, 20, 0, 0},    // semantic encoding
		{0, 20, 1, 0, 0}, // trailing bytes
		{0, 20, 1, 2},    // invalid Option flag
		{0, 20, 1, 1, 0}, // rejection with an empty reason
	} {
		t.Run(hex.EncodeToString(payload), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			unattached := make(chan bool, 1)
			go func() {
				_, _ = readNativeMessage(server)
				_ = writeNativeMessage(server, payload)
				message, err := readNativeMessage(server)
				unattached <- err != nil && len(message) == 0
			}()
			protocol, _ := nativeScrollVersion(20)
			if _, err := openNativeScroll(ctx, nativeTestEndpoint{client}, protocol, "w1:p1", TerminalGeometry{Cols: 120, Rows: 40}); err == nil {
				t.Fatal("incompatible welcome was accepted")
			}
			if !<-unattached {
				t.Fatal("sent control after an incompatible welcome")
			}
		})
	}
}

func TestNativeScrollCancelBlockedHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		protocol, _ := nativeScrollVersion(20)
		_, err := openNativeScroll(ctx, nativeTestEndpoint{client}, protocol, "w1:p1", TerminalGeometry{Cols: 120, Rows: 40})
		done <- err
	}()
	if _, err := readNativeMessage(server); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake ignored cancellation")
	}
}

func TestNativeMessageBoundsAndIntegers(t *testing.T) {
	for _, packet := range []string{"00000000", "01000002", "ffffffff", "050000000102"} {
		data, _ := hex.DecodeString(packet)
		if _, err := readNativeMessage(bytes.NewReader(data)); err == nil {
			t.Fatalf("accepted invalid framed data %s", packet)
		}
	}
	for _, fixture := range []struct {
		value uint64
		wire  string
	}{{250, "fa"}, {251, "fbfb00"}, {65535, "fbffff"}, {65536, "fc00000100"}, {1 << 32, "fd0000000001000000"}} {
		if got := hex.EncodeToString(nativeValues(fixture.value)); got != fixture.wire {
			t.Fatalf("incorrect bincode integer: %s != %s", got, fixture.wire)
		}
		wire, _ := hex.DecodeString(fixture.wire)
		d := nativeDecoder{data: wire}
		if got := d.uint(); got != fixture.value || d.finish() != nil {
			t.Fatalf("incorrect integer decode: %d != %d", got, fixture.value)
		}
	}
	for _, data := range [][]byte{{251}, {252, 0}, {253, 0}, {254}, {255}} {
		d := nativeDecoder{data: data}
		_ = d.uint()
		if d.finish() == nil {
			t.Fatalf("accepted malformed integer %x", data)
		}
	}
}

func TestNativeScrollRejectsMalformedOutput(t *testing.T) {
	for _, payload := range [][]byte{
		{2, 1, 120, 40, 2, 0},    // invalid boolean
		{2, 1, 120, 40, 1, 9},    // missing byte vector
		{2, 1, 120, 40, 1, 0, 0}, // trailing data
		{2, 1, 0, 40, 1, 0},      // impossible geometry
	} {
		t.Run(hex.EncodeToString(payload), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			go func() {
				_, _ = readNativeMessage(server)
				_ = writeNativeMessage(server, []byte{0, 20, 1, 0})
				_, _ = readNativeMessage(server)
				_ = writeNativeMessage(server, payload)
				_ = server.Close()
			}()
			protocol, _ := nativeScrollVersion(20)
			process, err := openNativeScroll(ctx, nativeTestEndpoint{client}, protocol, "w1:p1", TerminalGeometry{Cols: 120, Rows: 40})
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			if bufio.NewScanner(process.Stdout()).Scan() {
				t.Fatal("malformed frame reached the observer")
			}
			if err := process.Wait(); err == nil {
				t.Fatal("malformed output succeeded")
			}
		})
	}
}

type unknownProtocolEndpoint struct{ *controllerEndpoint }

func (e unknownProtocolEndpoint) Snapshot(ctx context.Context) (Snapshot, error) {
	snapshot, err := e.controllerEndpoint.Snapshot(ctx)
	snapshot.Protocol = 999
	return snapshot, err
}

func TestScrollUnknownProtocolDoesNotFallbackToCLI(t *testing.T) {
	e := unknownProtocolEndpoint{&controllerEndpoint{}}
	scroll := NewScrollController(context.Background(), e, "w1:p1")
	defer scroll.Close()
	if err := scroll.Send(context.Background(), 3, 0, 0); err == nil {
		t.Fatal("unknown protocol accepted scroll")
	}
	if len(e.processes) != 0 {
		t.Fatal("unknown protocol opened a terminal controller")
	}
	if _, err := nativeScrollVersion(21); err == nil {
		t.Fatal("unreviewed protocol 21 was accepted")
	}
}

type shortNativeWriter struct{}

func (shortNativeWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestNativeShortWriteIsUncertain(t *testing.T) {
	if err := writeNativeMessage(shortNativeWriter{}, []byte{4}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial detach was accepted: %v", err)
	}
}
