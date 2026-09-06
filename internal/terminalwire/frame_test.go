package terminalwire

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	want := Frame{Opcode: OpcodeFrame, Flags: FlagFull, StreamID: 42, Seq: 99, Payload: TerminalPayload(120, 40, []byte("hello"))}
	got, err := Decode(Encode(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Opcode != want.Opcode || got.Flags != want.Flags || got.StreamID != want.StreamID || got.Seq != want.Seq || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	cols, rows, err := Dimensions(got.Payload)
	if err != nil || cols != 120 || rows != 40 {
		t.Fatalf("dimensions = %d x %d, err=%v", cols, rows, err)
	}
}

func TestDecodeRejectsShortOrUnknownFrame(t *testing.T) {
	if _, err := Decode([]byte{Kind}); err == nil {
		t.Fatal("short frame was accepted")
	}
	encoded := Encode(Frame{})
	encoded[1]++
	if _, err := Decode(encoded); err == nil {
		t.Fatal("unknown version was accepted")
	}
}
