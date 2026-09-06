package terminalwire

import (
	"encoding/binary"
	"fmt"
)

const (
	Kind       byte = 0x74
	Version    byte = 1
	HeaderSize      = 16

	OpcodeFrame     byte = 1
	OpcodeAck       byte = 2
	OpcodeInput     byte = 3
	OpcodeResize    byte = 4
	OpcodeScroll    byte = 5
	OpcodeRelease   byte = 6
	OpcodeResizeAck byte = 7

	FlagFull byte = 1
)

type Frame struct {
	Opcode   byte
	Flags    byte
	StreamID uint32
	Seq      uint64
	Payload  []byte
}

func Encode(frame Frame) []byte {
	encoded := make([]byte, HeaderSize+len(frame.Payload))
	encoded[0] = Kind
	encoded[1] = Version
	encoded[2] = frame.Opcode
	encoded[3] = frame.Flags
	binary.LittleEndian.PutUint32(encoded[4:8], frame.StreamID)
	binary.LittleEndian.PutUint64(encoded[8:16], frame.Seq)
	copy(encoded[HeaderSize:], frame.Payload)
	return encoded
}

func Decode(encoded []byte) (Frame, error) {
	if len(encoded) < HeaderSize {
		return Frame{}, fmt.Errorf("terminal frame is shorter than %d bytes", HeaderSize)
	}
	if encoded[0] != Kind || encoded[1] != Version {
		return Frame{}, fmt.Errorf("unsupported terminal frame")
	}
	return Frame{
		Opcode: encoded[2], Flags: encoded[3], StreamID: binary.LittleEndian.Uint32(encoded[4:8]),
		Seq: binary.LittleEndian.Uint64(encoded[8:16]), Payload: encoded[HeaderSize:],
	}, nil
}

func TerminalPayload(cols, rows uint16, ansi []byte) []byte {
	payload := make([]byte, 4+len(ansi))
	binary.LittleEndian.PutUint16(payload[0:2], cols)
	binary.LittleEndian.PutUint16(payload[2:4], rows)
	copy(payload[4:], ansi)
	return payload
}

func Dimensions(payload []byte) (uint16, uint16, error) {
	if len(payload) < 4 {
		return 0, 0, fmt.Errorf("terminal dimension payload is too short")
	}
	return binary.LittleEndian.Uint16(payload[0:2]), binary.LittleEndian.Uint16(payload[2:4]), nil
}
