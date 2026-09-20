package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestNativeObserverWireIsReadOnlyAndViewportOnly(t *testing.T) {
	// Golden packets follow the separately reviewed bincode enums in 0.8.2 and
	// 0.9.1. Observe has no takeover bit; Resize retains the read cell dimensions.
	for _, fixture := range []struct {
		version                        int
		hello, observe, resize         string
		welcome, initial, resizedFrame string
	}{
		{20, "09000000001486310810010002", "07000000080577313a7031", "0500000003641e0810", "0400000000140100", "0b00000002018631010568656c6c6f", "0b0000000202641e010568656c6c6f"},
		{22, "0700000000168631081000", "07000000070577313a7031", "0600000003641e081000", "0400000000160100", "0b00000001018631010568656c6c6f", "0b0000000102641e010568656c6c6f"},
	} {
		t.Run(fmt.Sprint(fixture.version), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			result := make(chan error, 1)
			go func() {
				defer server.Close()
				check := func(golden string) error {
					want, _ := hex.DecodeString(golden)
					got := make([]byte, len(want))
					if _, err := io.ReadFull(server, got); err != nil {
						return err
					}
					if !bytes.Equal(got, want) {
						return fmt.Errorf("observer wrote unsafe/wrong message %x; want %x", got, want)
					}
					return nil
				}
				write := func(golden string) { data, _ := hex.DecodeString(golden); _, _ = server.Write(data) }
				if err := check(fixture.hello); err != nil {
					result <- err
					return
				}
				write(fixture.welcome)
				if err := check(fixture.observe); err != nil {
					result <- err
					return
				}
				write(fixture.initial)
				if err := check(fixture.resize); err != nil {
					result <- err
					return
				}
				write(fixture.resizedFrame)
				result <- nil
			}()
			process, err := OpenViewportObserver(ctx, nativeTestEndpoint{client}, fixture.version, "w1:p1", TerminalGeometry{Cols: 134, Rows: 49, CellWidthPx: 8, CellHeightPx: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			scanner := bufio.NewScanner(process.Stdout())
			checkFrame := func(cols, rows int) {
				t.Helper()
				if !scanner.Scan() {
					t.Fatal("missing frame", scanner.Err())
				}
				var frame struct {
					Width, Height int
					Full          bool
					Bytes         []byte
				}
				if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil || frame.Width != cols || frame.Height != rows || !frame.Full || string(frame.Bytes) != "hello" {
					t.Fatalf("invalid viewport frame %+v, %v", frame, err)
				}
			}
			checkFrame(134, 49)
			for _, input := range []string{`{"type":"terminal.resize","cols":20,"rows":10}`, `{"type":"terminal.input","text":"unexpected"}`, `{"type":"terminal.scroll","lines":1}`, `{"type":"terminal.release"}`} {
				if n, err := io.WriteString(process.Stdin(), input); err == nil || n != 0 {
					t.Fatalf("read-only observer accepted %q", input)
				}
			}
			viewport := process.(ViewportObserver)
			if err := viewport.ResizeViewport(ctx, 1, 1); err == nil {
				t.Fatal("invalid viewport accepted")
			}
			if err := viewport.ResizeViewport(ctx, 100, 30); err != nil {
				t.Fatal(err)
			}
			checkFrame(100, 30)
			if err := process.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeObserverRejectsUnreviewedProtocolAndInvalidTarget(t *testing.T) {
	for _, tc := range []struct {
		version int
		pane    string
	}{{99, "w1:p1"}, {22, "bad\ninput"}} {
		// A nil socket also ensures rejection happens before a network operation.
		if _, err := OpenViewportObserver(t.Context(), nativeTestEndpoint{}, tc.version, tc.pane, TerminalGeometry{Cols: 120, Rows: 40}); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestNativeObserverCancelsBlockedHandshakeAndResize(t *testing.T) {
	for _, stage := range []string{"handshake", "resize"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			stopServer := make(chan struct{})
			defer close(stopServer)
			go func() {
				_, _ = readNativeMessage(server)
				if stage == "resize" {
					_ = writeNativeMessage(server, []byte{0, 22, 1, 0})
					_, _ = readNativeMessage(server)
				}
				<-stopServer // No welcome / no further reads: cancellation must close.
			}()
			process, err := OpenViewportObserver(ctx, nativeTestEndpoint{client}, 22, "w1:p1", TerminalGeometry{Cols: 120, Rows: 40})
			if stage == "handshake" {
				if err == nil {
					t.Fatal("blocked handshake succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			resizeCtx, resizeCancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer resizeCancel()
			if err := process.(ViewportObserver).ResizeViewport(resizeCtx, 100, 30); err == nil {
				t.Fatal("blocked resize succeeded")
			}
			done := make(chan struct{})
			go func() { _ = process.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("canceled observer leaked")
			}
		})
	}
}
