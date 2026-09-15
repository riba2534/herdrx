package herdr

// DSH（DeepSeek Harness）会话记录的**传输层**实现。
//
// 权威数据源是 DSH 自己写下的 append-only 会话产物，路径固定在
// `$HOME/.dsh/sessions`（上游 `dsh-base/cordis.patch.yml` 的
// `dshHomePath('sessions')`），布局是
//
//	<sessions>/<projectKey(cwd)>/<encodeSegment(id)>/session.v3.jsonl[.zstd]
//
// 本文件负责 agentlog 刻意不碰的部分：目录名编码、zstd 帧边界与有界解压。JSON 的语义
// 解析全部在 `internal/agentlog/dsh.go`，本文件只把**明文字节**喂给它。
//
// 两份上游实现是这里所有形状的出处，不是猜测：
//   - `dsh-session-persistence-jsonl/lib/index.js`：`projectKey` / `encodeSegment` /
//     `generationLogFilename` / `scanZstdFrames` / `compressZstdFrame` /
//     `readFirstZstdLine` / `assertZstdHeaderFrame`。
//   - `dsh-session-format-v1-to-v2/lib/index.js`：物理 header 的精确键集合。
//
// 压缩容器是**串接的独立 zstd 帧**，每帧带校验和，且每帧都可以单独解码：
// 第 0 帧恰好是一行 header，之后每帧恰好是一个持久化批次（可能多行）。帧是自定界的，
// 所以帧边界可以**不解压任何块**就结构性地扫出来 —— 这也是候选列表只读首帧 header
// 却不碰会话正文的依据。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/klauspost/compress/zstd"
	"github.com/riba2534/herdrx/internal/agentlog"
)

const (
	// transcriptDSHMaxCompressedBytes 是 v1 对**单个** DSH 会话产物接受的压缩字节上限。
	// 超出不是「空日志」也不是「格式错误」，而是明确的能力上限，见
	// ChatReasonReadLimitExceeded。
	transcriptDSHMaxCompressedBytes = 8 << 20
	// transcriptDSHMaxPlaintextBytes 是单次请求解压输出的总预算。分页只要一页，
	// 所以这个预算远大于一页，只是用来兜住「一帧解压出巨量内容」的多帧叠加。
	transcriptDSHMaxPlaintextBytes = 32 << 20
	// transcriptDSHFramePlainMax 是**单帧**解压输出的上限，同时用作 zstd 解码器的
	// 内存上限（decoder max memory）。DSH 的批次帧远小于它。
	transcriptDSHFramePlainMax = 8 << 20
	// transcriptDSHDecoderWindow 是解码器接受的 zstd 窗口上限。
	transcriptDSHDecoderWindow = 8 << 20
	// transcriptDSHChunkBytes 是一次底层读取的字节数。远端读取器的单次回答上限是
	// 2 MiB，base64 之后还要膨胀 4/3，所以整文件读取必须分块，不能一次要完。
	transcriptDSHChunkBytes = 1 << 20
	// transcriptPageRecordsHard 是契约里一页记录的硬上限（200 只是默认值）。
	// DSH 的分页只能切在帧边界上，一帧的记录数会略微顶破 200，但绝不能顶破它。
	transcriptPageRecordsHard = 500
)

// errDSHDecodeLimit 表示某个帧的解压输出超过了本次读取的预算。
var errDSHDecodeLimit = errors.New("dsh: decoded frame exceeds read limit")

// errDSHBatchUnterminated 表示一个结构完整的帧没有以换行收尾，因此不是一份完整批次。
var errDSHBatchUnterminated = errors.New("dsh: batch frame is not newline terminated")

// ── 目录名编码 ──
//
// `projectKey` 与 `encodeSegment` 都按 **UTF-16 码元** 迭代（上游用 charCodeAt），
// 所以 Go 这边必须先 utf16.Encode：一个增补平面字符会变成两个 `~XXXX` 转义，
// 按 rune 实现会得出不同的目录名。两者都是**有损**的（projectKey 会把分隔符折叠成
// 一个 '-' 并截断），因此目录名只能用来缩小搜索范围，归属仍然只能由 header 里
// 真实 cwd 的精确相等来判定。

// dshSafeUnit 报告一个 UTF-16 码元是否落在上游的 `[A-Za-z0-9._-]` 白名单里。
func dshSafeUnit(unit uint16) bool {
	switch {
	case unit >= 'a' && unit <= 'z':
		return true
	case unit >= 'A' && unit <= 'Z':
		return true
	case unit >= '0' && unit <= '9':
		return true
	case unit == '.' || unit == '_' || unit == '-':
		return true
	}
	return false
}

// dshEscapeUnit 把任意码元编码成 `~XXXX`（大写四位十六进制）。
func dshEscapeUnit(unit uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{
		'~',
		digits[(unit>>12)&0xF],
		digits[(unit>>8)&0xF],
		digits[(unit>>4)&0xF],
		digits[unit&0xF],
	})
}

// dshEscapeAll 按 encodeSegment 的规则编码一个字符串（不含 '.' / '..' 特例）。
func dshEscapeAll(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))
	for _, unit := range utf16.Encode([]rune(raw)) {
		if unit != '~' && dshSafeUnit(unit) {
			builder.WriteByte(byte(unit))
			continue
		}
		builder.WriteString(dshEscapeUnit(unit))
	}
	return builder.String()
}

// dshEncodeSegment 复刻上游 `encodeSegment`：把一个会话 id 编码成单个安全路径段。
// 非空 id 之外没有别的输入能通过，空串返回 false。
func dshEncodeSegment(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	// 上游对 '.' 与 '..' 特判，避免「本来安全的整段」变成目录穿越。
	if raw == "." {
		return "~002E", true
	}
	if raw == ".." {
		return "~002E~002E", true
	}
	return dshEscapeAll(raw), true
}

// dshDecodeSegment 是 encodeSegment 的逆：解析 `~XXXX` 转义与白名单字面量。
//
// 它只接受**规范**编码（解码后再编码必须回到原串），所以形如 `a~2fb` 的小写转义、
// 落单的 `~`、超出白名单的字面量一律拒绝。调用方用它把「客户端回传的会话目录段」
// 还原成会话 id，再与 header 里的 id 对拍。
func dshDecodeSegment(segment string) (string, bool) {
	if segment == "" {
		return "", false
	}
	units := make([]uint16, 0, len(segment))
	for index := 0; index < len(segment); {
		if segment[index] == '~' {
			if index+5 > len(segment) {
				return "", false
			}
			unit, ok := dshParseHex4(segment[index+1 : index+5])
			if !ok {
				return "", false
			}
			units = append(units, unit)
			index += 5
			continue
		}
		character := segment[index]
		if character == '~' || !dshSafeUnit(uint16(character)) {
			return "", false
		}
		units = append(units, uint16(character))
		index++
	}
	decoded := string(utf16.Decode(units))
	// 规范回环：拒绝非规范写法，也顺带拒绝 utf16.Decode 无法还原的落单代理项。
	if reencoded, ok := dshEncodeSegment(decoded); !ok || reencoded != segment {
		return "", false
	}
	return decoded, true
}

// dshParseHex4 解析四个大写十六进制位。
func dshParseHex4(text string) (uint16, bool) {
	var value uint16
	for index := 0; index < len(text); index++ {
		character := text[index]
		var digit uint16
		switch {
		case character >= '0' && character <= '9':
			digit = uint16(character - '0')
		case character >= 'A' && character <= 'F':
			digit = uint16(character-'A') + 10
		default:
			return 0, false
		}
		value = value<<4 | digit
	}
	return value, true
}

// dshProjectDir 复刻上游 `projectKey`（`projectDir` 在有 cwd 时的目录名）。
//
// 与上游的差异只有一处：上游在 `readable` 为空时回退到字面量 "root"，这里保留同一行为，
// 所以 `/` 会得到 `--root--`。cwd 为空串时上游走 `_no-cwd` 分支（会话根本没有 cwd），
// 那不是「某个项目的目录」，因此这里直接返回 false。
func dshProjectDir(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	var builder strings.Builder
	builder.Grow(len(cwd) + 4)
	separatorRun := false
	for _, unit := range utf16.Encode([]rune(cwd)) {
		switch {
		case unit == '/' || unit == '\\' || unit == ':':
			if !separatorRun {
				builder.WriteByte('-')
			}
			separatorRun = true
		case unit != '~' && dshSafeUnit(unit):
			builder.WriteByte(byte(unit))
			separatorRun = false
		default:
			builder.WriteString(dshEscapeUnit(unit))
			separatorRun = false
		}
	}
	// 上游只裁掉**前导** '-'，不折叠尾部；此时串里只剩 ASCII，按字节截断与按码元一致。
	readable := strings.TrimLeft(builder.String(), "-")
	if readable == "" {
		readable = "root"
	}
	if len(readable) > 251 {
		readable = readable[:251]
	}
	return "--" + readable + "--", true
}

// dshSessionDir 返回 cwd + 会话 id 对应的**相对**目录（相对 sessions 根）。
func dshSessionDir(cwd, sessionID string) (string, bool) {
	project, ok := dshProjectDir(cwd)
	if !ok {
		return "", false
	}
	segment, ok := dshEncodeSegment(sessionID)
	if !ok {
		return "", false
	}
	return project + "/" + segment, true
}

// dshRelForSession 返回 cwd + 会话 id + 文件名对应的相对路径。
func dshRelForSession(cwd, sessionID, name string) (string, bool) {
	dir, ok := dshSessionDir(cwd, sessionID)
	if !ok {
		return "", false
	}
	return dir + "/" + name, true
}

// dshForeignGeneration 报告一个文件名看起来是**别的** DSH 会话格式代际的产物。
//
// 只用来把「这个 cwd 下有会话、但不是本实现支持的代际」与「这个 cwd 下没有会话」
// 区分开：前者必须如实说暂不支持，不能含糊成「没有候选」。
func dshForeignGeneration(name string) bool {
	if agentlog.DSHSessionFile(name) || agentlog.DSHCompressedSessionFile(name) {
		return false
	}
	stem := strings.TrimSuffix(name, ".zstd")
	// v0 代际叫 session.jsonl；v1 之后叫 session.v<N>.jsonl。
	if stem == "session.jsonl" {
		return true
	}
	return strings.HasPrefix(stem, "session.v") && strings.HasSuffix(stem, ".jsonl")
}

// ── zstd 帧 ──

// dshZstdMagic 是 zstd 帧魔数（小端读出的 0xFD2FB528）。
const dshZstdMagic = 4247762216

// dshFrame 是一个已结构校验的完整 zstd 帧在**压缩文件**里的字节范围。
type dshFrame struct {
	Start int64
	End   int64
}

// dshScanFrames 结构性地扫出完整帧的范围，不解压任何块。
//
// 这是上游 `scanZstdFrames` 的逐行对照实现，包括它的三种结论：
//   - 结构非法（魔数不对、保留位/保留块类型、字典标志不可能）：返回错误，整份产物不可信；
//   - 尾部字节不足一帧：返回已完整帧 + `torn` = 该未完成帧的起点（-1 表示没有）；
//   - 正常走到末尾：返回全部完整帧 + torn = -1。
//
// maxFrames > 0 时扫够即停（torn 返回 -1），供「只读首帧 header」的候选枚举使用：
// 列目录绝不能解压全部会话正文。base 是 data[0] 在文件里的绝对偏移。
func dshScanFrames(data []byte, base int64, maxFrames int) ([]dshFrame, int64, error) {
	frames := make([]dshFrame, 0, 8)
	offset := 0
	for offset < len(data) {
		start := offset
		if len(data)-offset < 4 {
			return frames, base + int64(start), nil
		}
		if binary.LittleEndian.Uint32(data[offset:offset+4]) != dshZstdMagic {
			return frames, -1, fmt.Errorf("dsh: 帧魔数非法（偏移 %d）", base+int64(offset))
		}
		offset += 4
		if offset == len(data) {
			return frames, base + int64(start), nil
		}
		descriptor := data[offset]
		offset++
		if descriptor&24 != 0 {
			return frames, -1, fmt.Errorf("dsh: 帧头保留位非零（偏移 %d）", base+int64(offset-1))
		}
		contentSizeFlag := descriptor >> 6
		singleSegment := descriptor&32 != 0
		checksum := descriptor&4 != 0
		dictionaryBytes := int(descriptor & 3)
		if dictionaryBytes == 3 {
			dictionaryBytes = 4
		}
		contentSizeBytes := 0
		if contentSizeFlag == 0 {
			if singleSegment {
				contentSizeBytes = 1
			}
		} else {
			contentSizeBytes = 1 << contentSizeFlag
		}
		remainingHeader := contentSizeBytes + dictionaryBytes
		if !singleSegment {
			remainingHeader++
		}
		if len(data)-offset < remainingHeader {
			return frames, base + int64(start), nil
		}
		offset += remainingHeader
		for {
			if len(data)-offset < 3 {
				return frames, base + int64(start), nil
			}
			blockHeader := uint32(data[offset]) | uint32(data[offset+1])<<8 | uint32(data[offset+2])<<16
			offset += 3
			lastBlock := blockHeader&1 != 0
			blockType := (blockHeader >> 1) & 3
			blockSize := blockHeader >> 3
			if blockType == 3 {
				return frames, -1, fmt.Errorf("dsh: 保留块类型（偏移 %d）", base+int64(offset-3))
			}
			// RLE 块的载荷固定一字节，Raw 与 Compressed 用块头声明的长度。
			payloadBytes := blockSize
			if blockType == 1 {
				payloadBytes = 1
			}
			if uint32(len(data)-offset) < payloadBytes {
				return frames, base + int64(start), nil
			}
			offset += int(payloadBytes)
			if lastBlock {
				break
			}
		}
		if checksum {
			if len(data)-offset < 4 {
				return frames, base + int64(start), nil
			}
			offset += 4
		}
		frames = append(frames, dshFrame{Start: base + int64(start), End: base + int64(offset)})
		if maxFrames > 0 && len(frames) == maxFrames {
			return frames, -1, nil
		}
	}
	return frames, -1, nil
}

// dshDecoder 是一次请求内复用的有界 zstd 解码器。
//
// 上限来自三个方向：解码器内存上限（WithDecoderMaxMemory）、解码器窗口上限
// （WithDecoderMaxWindow）、以及 DecodeAll 的输出上限（WithDecodeAllCapLimit）。
// 缺一个都会让「一帧宣称自己解压出几个 GiB」变成内存事故。
type dshDecoder struct {
	decoder   *zstd.Decoder
	remaining int
}

func newDSHDecoder(budget int) (*dshDecoder, error) {
	decoder, err := zstd.NewReader(nil,
		// 并发 1 而不是 0：0 会按 NumCPU 起工作协程，而这个解码器是**每请求**建的，
		// 一帧一帧顺序解，多出来的协程只是纯开销。
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecodeAllCapLimit(true),
		zstd.WithDecoderMaxMemory(transcriptDSHFramePlainMax),
		zstd.WithDecoderMaxWindow(transcriptDSHDecoderWindow),
	)
	if err != nil {
		return nil, err
	}
	return &dshDecoder{decoder: decoder, remaining: budget}, nil
}

func (d *dshDecoder) Close() {
	if d.decoder != nil {
		d.decoder.Close()
	}
}

// decode 有界解压一个**结构完整**的帧，并校验其校验和。
//
// 上限有两个层次，都不依赖帧自己声明的内容大小：
//   - 整个请求的明文总预算 d.remaining（调用方按 32 MiB 起始，逐帧扣减）；
//   - 单帧上限 transcriptDSHFramePlainMax。
//
// 真正卡住输出的是 dst 的 **cap**：WithDecodeAllCapLimit 让 DecodeAll 只解
// `cap(dst)-len(dst)` 个字节，超出即报错。所以这里必须按限额分配 dst ——
// 小帧先用小容量，容量不足时逐步增长，直到本次预算；不会按最大预算给每帧预分配。
// 返回的字节只有在 err == nil 时有效。
func (d *dshDecoder) decode(frame []byte) ([]byte, error) {
	limit := d.remaining
	if limit > transcriptDSHFramePlainMax {
		limit = transcriptDSHFramePlainMax
	}
	if limit <= 0 {
		return nil, errDSHDecodeLimit
	}
	// 帧头声明的内容大小先过一遍账：还没解就知道超了，省掉一次分配。
	// 没有声明大小（HasFCS=false）的帧照样由 decodeAllLimit 兜住。
	var header zstd.Header
	knownSize := header.Decode(frame) == nil && header.HasFCS
	if knownSize && header.FrameContentSize > uint64(limit) {
		return nil, errDSHDecodeLimit
	}
	// 有长度声明时按真实长度分配；无声明则从小窗口逐步扩大。
	// DecodeAll 是无状态解码，容量不足可安全重试，且始终不越过本次预算。
	capacity := min(transcriptHeadBytes, limit)
	if knownSize {
		capacity = int(header.FrameContentSize)
	}
	var plain []byte
	var err error
	for {
		plain, err = d.decoder.DecodeAll(frame, make([]byte, 0, capacity))
		if !errors.Is(err, zstd.ErrDecoderSizeExceeded) || capacity >= limit || knownSize {
			break
		}
		capacity = min(capacity*2, limit)
	}
	if err != nil {
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) || errors.Is(err, zstd.ErrFrameSizeExceeded) {
			// 不是「帧坏了」，而是「这一帧比允许读的更多」：对用户是不同的下一步。
			return nil, errDSHDecodeLimit
		}
		return nil, fmt.Errorf("dsh: 帧解压失败: %w", err)
	}
	if len(plain) > d.remaining {
		return nil, errDSHDecodeLimit
	}
	d.remaining -= len(plain)
	return plain, nil
}

// dshIsHeaderPlaintext 报告一帧明文是否是「恰好一行、且以换行结尾」的 header 帧。
// 对应上游 `assertZstdHeaderFrame` / `assertIndependentHeaderFrame`。
func dshIsHeaderPlaintext(plain []byte) bool {
	return len(plain) > 0 && plain[len(plain)-1] == '\n' && !containsNewlineBeforeLast(plain)
}

func containsNewlineBeforeLast(data []byte) bool {
	for index := 0; index < len(data)-1; index++ {
		if data[index] == '\n' {
			return true
		}
	}
	return false
}

// dshHeaderAdmitted 单独校验 header 行的准入。
//
// 分页读取只解它当下需要的那一段字节，header 行通常**不在**其中；而 agentlog 对 header 的
// 准入拒绝（例如 isSeeded 的会话继承了父会话上下文）恰恰只发生在这一行上。所以归属校验
// 通过之后，还必须把 header 行单独喂给同一个解码器：否则「从尾部开始读」就会绕过这个拒绝，
// 把继承来的上下文当成用户自己的对话发布出去。
//
// 还没有完整 header 行时返回 true：那种情况下调用方本来就按「还没有内容」处理，不主张归属。
func dshHeaderAdmitted(agent, sessionID string, data []byte, offset int64) bool {
	line, ok := dshFirstCompleteLine(data)
	if !ok {
		return true
	}
	result := agentlog.DecodeRange(
		agentlog.LineContext{Agent: agent, SessionID: sessionID},
		line,
		offset,
	)
	return result.Failure == nil
}

// dshFirstCompleteLine 切出第一行完整（以换行结尾）的字节。
func dshFirstCompleteLine(data []byte) ([]byte, bool) {
	index := bytes.IndexByte(data, '\n')
	if index < 0 {
		return nil, false
	}
	return data[:index+1], true
}

// ── 读取 ──

// dshReadAll 分块读完一个文件。远端一次回答有 2 MiB 上限，所以**不能**一次要完：
// 这里按 transcriptDSHChunkBytes 分块，任何一块失败都整体失败。
//
// 返回的 result 是最后一次成功读取的结果（用它的 Size / Identity 复核文件身份）。
func dshReadAll(ctx context.Context, files transcriptFS, root transcriptRootKind, rel string, size int64) ([]byte, transcriptReadResult, error) {
	if size <= 0 {
		return nil, transcriptReadResult{}, nil
	}
	data := make([]byte, 0, size)
	var last transcriptReadResult
	for offset := int64(0); offset < size; {
		length := size - offset
		if length > transcriptDSHChunkBytes {
			length = transcriptDSHChunkBytes
		}
		result, err := transcriptReadOne(ctx, files, root, rel, offset, length)
		if err != nil {
			return nil, result, err
		}
		if result.Err != nil || !result.Exists {
			return nil, result, nil
		}
		if len(result.Data) == 0 {
			// 读不动了：交给调用方按「文件变了」处理，绝不在这里死循环。
			return nil, result, nil
		}
		data = append(data, result.Data...)
		offset += int64(len(result.Data))
		last = result
	}
	return data, last, nil
}
