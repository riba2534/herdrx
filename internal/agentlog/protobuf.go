package agentlog

import (
	"unicode/utf8"
)

// pbField 是 protobuf wire 上的一个字段。没有 schema：长度字段同时可能是
// 字符串或子消息，由调用方按已知路径决定怎么解释。
type pbField struct {
	Num  int
	Wire int
	U64  uint64
	Raw  []byte
}

// pbParse 按 protobuf wire 规则拆一条消息。截断、非法 wire 或没吃完的尾部都返回 false。
// 未知字段会被留下，调用方不认识就跳过。
func pbParse(data []byte) ([]pbField, bool) {
	fields := make([]pbField, 0, 8)
	for len(data) > 0 {
		tag, rest, ok := pbVarint(data)
		if !ok || tag < 8 {
			return nil, false
		}
		data = rest
		field := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			value, rest, ok := pbVarint(data)
			if !ok {
				return nil, false
			}
			fields = append(fields, pbField{Num: field, Wire: wire, U64: value})
			data = rest
		case 1:
			if len(data) < 8 {
				return nil, false
			}
			fields = append(fields, pbField{Num: field, Wire: wire, Raw: data[:8]})
			data = data[8:]
		case 2:
			length, rest, ok := pbVarint(data)
			if !ok || length > uint64(len(rest)) {
				return nil, false
			}
			data = rest
			fields = append(fields, pbField{Num: field, Wire: wire, Raw: data[:length]})
			data = data[length:]
		case 5:
			if len(data) < 4 {
				return nil, false
			}
			fields = append(fields, pbField{Num: field, Wire: wire, Raw: data[:4]})
			data = data[4:]
		default:
			return nil, false
		}
	}
	return fields, true
}

func pbVarint(data []byte) (uint64, []byte, bool) {
	var value uint64
	for shift := 0; shift < 64; shift += 7 {
		if len(data) == 0 {
			return 0, nil, false
		}
		byteValue := data[0]
		data = data[1:]
		value |= uint64(byteValue&0x7f) << shift
		if byteValue < 0x80 {
			return value, data, true
		}
	}
	return 0, nil, false
}

// pbChildren 把某个长度字段的每一次出现都试着当成子消息。不是合法消息的出现被跳过。
func pbChildren(fields []pbField, num int) [][]pbField {
	children := make([][]pbField, 0, 1)
	for _, field := range fields {
		if field.Num != num || field.Wire != 2 {
			continue
		}
		child, ok := pbParse(field.Raw)
		if !ok {
			continue
		}
		children = append(children, child)
	}
	return children
}

// pbTextAt 沿路径取第一段可用文本。中间每一跳必须是子消息；最后一跳是 UTF-8 字符串。
func pbTextAt(fields []pbField, path ...int) string {
	if len(path) == 0 {
		return ""
	}
	if len(path) == 1 {
		for _, field := range fields {
			if field.Num != path[0] || field.Wire != 2 {
				continue
			}
			if text, ok := pbText(field.Raw); ok {
				return text
			}
		}
		return ""
	}
	for _, child := range pbChildren(fields, path[0]) {
		if text := pbTextAt(child, path[1:]...); text != "" {
			return text
		}
	}
	return ""
}

// pbText 只接受可打印 UTF-8。二进制子消息即使碰巧是合法 UTF-8，只要含控制字符就不是文本。
func pbText(raw []byte) (string, bool) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return "", false
	}
	for _, r := range string(raw) {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return "", false
		}
	}
	return string(raw), true
}
