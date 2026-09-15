package herdr

// DSH 压缩会话产物的分页读取。
//
// 与明文 provider 的字节分页有一个本质差别：这里**只能切在 zstd 帧边界上**。
// 理由是游标必须是一个能当作下一次读取起点的绝对偏移，而压缩文件里只有帧边界是
// 这样的位置；切在帧中间会让同一帧剩下的记录既回不去也拿不到，「加载更早」会永远
// 跳过它们。所以：
//
//   - 一页 = 若干个**完整帧**的全部记录，记录顺序不变；
//   - 游标 = 帧边界（压缩文件的绝对偏移），追加时稳定，截断/重写时不再落在帧边界上，
//     于是自然触发 reset；
//   - 尾部的半帧（Agent 正在写）不解析、不产出、游标也不前进，下次续读整帧重读。
//
// 由此带来一个有意的取舍：一帧的记录数会让一页略微超过默认的 200 条。硬上限仍然是
// 契约的 500 条与 256 KiB；**单帧**自身顶破硬上限时返回 read_limit_exceeded，
// 而不是切碎帧（切碎就是丢记录）。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// dshDecodedFrame 是一帧解压并解码后的记录。
type dshDecodedFrame struct {
	start   int64
	end     int64
	entries []agentlog.RangeEntry
	sizes   []int
	bytes   int
	skipped int
}

// transcriptDSHCompressedMessages 读取一份 **zstd** DSH 会话产物。
//
// 明文代际（session.v3.jsonl）不走这里，直接复用通用字节分页引擎：它的行边界就是
// 天然的切页点，没有帧语义要照顾。
func transcriptDSHCompressedMessages(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd, sessionID, rel string, request TranscriptRequest) TranscriptPage {
	root := transcriptRootDSH

	// 先读一小段拿到文件大小与身份，再决定是否值得整份读入。
	head, err := transcriptReadOne(ctx, files, root, rel, 0, transcriptHeadBytes)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if head.Err != nil {
		if errors.Is(head.Err, errTranscriptDenied) {
			return transcriptDegrade(ChatReasonReadDenied, agent)
		}
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	if !head.Exists {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	if head.Size <= 0 {
		// 还没有任何字节：产物存在但尚未落盘，不是降级，也不主张任何归属。
		return transcriptDSHEmpty(agent, sessionID)
	}
	if head.Size > transcriptDSHMaxCompressedBytes {
		return transcriptDegrade(ChatReasonReadLimitExceeded, agent)
	}
	size := head.Size
	identity := head.Identity

	data, tail, err := dshReadAll(ctx, files, root, rel, size)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if tail.Err != nil || !tail.Exists {
		if errors.Is(tail.Err, errTranscriptDenied) {
			return transcriptDegrade(ChatReasonReadDenied, agent)
		}
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	if int64(len(data)) < size || tail.Identity != identity {
		// 两次读取之间文件被截断或被替换：会话身份不再可信。
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}

	frames, _, err := dshScanFrames(data, 0, 0)
	if err != nil {
		return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
	}
	if len(frames) == 0 {
		// 连一帧都没有：header 还没写完，或产物已损坏。
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}

	decoder, err := newDSHDecoder(transcriptDSHMaxPlaintextBytes)
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	defer decoder.Close()

	// 第 0 帧恒为 header，且必须**恰好一行**；它的 cwd 与 id 是归属的唯一证明。
	headerPlain, err := decoder.decode(data[frames[0].Start:frames[0].End])
	if err != nil {
		return dshDecodeDegrade(err, agent)
	}
	if !dshIsHeaderPlaintext(headerPlain) {
		return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
	}
	shape := agentlog.ReadFileShape(agent, headerPlain)
	if shape.DSHVersion != agentlog.DSHFormatVersion {
		return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
	}
	if shape.CWD != cwd || shape.SessionID != sessionID {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	// 归属过了不代表 header 行可以被准入：分页只解自己要用的那一段，header 行常常不在
	// 其中，所以这里必须单独校验它，否则从尾部开始读会绕过 header 的拒绝。
	if !dshHeaderAdmitted(agent, sessionID, headerPlain, frames[0].Start) {
		return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
	}

	// 游标绑定会话 id、相对路径与文件身份；三者任一不符都重新给候选。
	token := []string{sessionID, rel, identity}
	reset := false
	anchor := int64(-1)
	switch {
	case request.Before != "":
		offset, ok := transcriptCursorOffset(codec, request.Before, token)
		if !ok || offset <= 0 || offset > size {
			reset = true
			break
		}
		anchor = offset
	case request.Cursor != "":
		offset, ok := transcriptCursorOffset(codec, request.Cursor, token)
		if !ok || offset < 0 || offset > size {
			reset = true
			break
		}
		anchor = offset
	}

	// atEnd 报告一个偏移是否「已经在所有完整帧之后」。尾部半帧的起点也算：游标停在
	// 最后一帧末尾时，那一帧可能正在写，等它写完就自然变成帧边界。
	atEnd := func(offset int64) bool {
		if offset == size {
			return true
		}
		return offset == frames[len(frames)-1].End
	}
	frameIndexAt := func(offset int64) int {
		for index := range frames {
			if frames[index].Start == offset {
				return index
			}
		}
		return -1
	}

	backward := request.Before != "" || request.Cursor == "" || reset
	startIndex := 0
	endIndex := len(frames)
	if !backward {
		if atEnd(anchor) {
			startIndex = len(frames)
			endIndex = len(frames)
		} else if index := frameIndexAt(anchor); index < 0 {
			// 游标不再落在任何帧边界上：文件被重写或截断，按重来页处理。
			reset = true
			backward = true
		} else {
			startIndex = index
		}
	} else if anchor >= 0 && !atEnd(anchor) {
		if index := frameIndexAt(anchor); index < 0 {
			reset = true
		} else {
			endIndex = index
		}
	}

	line := agentlog.LineContext{Agent: agent, SessionID: sessionID}
	decoded := make([]dshDecodedFrame, 0, 8)
	// stopIndex 记录解码停在哪个帧下标，用来判断「还有没有更新的帧没读」。
	stopIndex := startIndex
	// oldestIndex 是已解码的**最早**一帧的下标。第 0 帧恒为 header，不含任何记录，
	// 所以「还有更早的内容」只能是 oldestIndex > 1。
	oldestIndex := endIndex
	if backward {
		count := 0
		span := int64(0)
		for index := endIndex - 1; index >= 0; index-- {
			if len(decoded) > 0 {
				if count >= transcriptPageRecords || span >= transcriptInitialWindowBytes {
					break
				}
			}
			frame, err := dshDecodeFrame(decoder, data, frames[index], line)
			if err != nil {
				return dshDecodeDegrade(err, agent)
			}
			decoded = append(decoded, frame)
			count += len(frame.entries)
			span += frames[index].End - frames[index].Start
			oldestIndex = index
		}
		// 反向累积得到的是倒序，翻回正序再切页。
		for left, right := 0, len(decoded)-1; left < right; left, right = left+1, right-1 {
			decoded[left], decoded[right] = decoded[right], decoded[left]
		}
	} else {
		count, bytes := 0, 0
		index := startIndex
		for ; index < endIndex; index++ {
			if len(decoded) > 0 {
				if count >= transcriptPageRecords || bytes >= transcriptMaxResponseBytes {
					break
				}
			}
			frame, err := dshDecodeFrame(decoder, data, frames[index], line)
			if err != nil {
				return dshDecodeDegrade(err, agent)
			}
			decoded = append(decoded, frame)
			count += len(frame.entries)
			bytes += frame.bytes
		}
		stopIndex = index
	}

	first, last, overflow := dshSelectFrames(decoded, backward)
	if overflow {
		// 单帧自身就超过契约硬上限：如实报超限，不切碎帧、不丢记录。
		return transcriptDegrade(ChatReasonReadLimitExceeded, agent)
	}

	records := make([]agentlog.RangeEntry, 0, 16)
	skipped := 0
	for _, frame := range decoded[first:last] {
		records = append(records, frame.entries...)
		skipped += frame.skipped
	}

	// windowStart 是「这一页之前还有内容」的判据。
	//
	// 第 0 帧恒为 header（不含任何记录），所以只有 oldestIndex > 1 才说明前面确实还有
	// 事件帧；否则会给客户端一个永远点不完的「加载更早」。
	hasMore := false
	if backward {
		hasMore = first > 0 || oldestIndex > 1
	} else {
		hasMore = stopIndex < len(frames) || last < len(decoded)
	}

	// 切页坐标一律取自**帧边界**，不取自记录：
	//   - 一帧里可能一条记录都没有（整帧都是元数据行），用记录反推会让游标停在上一帧，
	//     下一页把同一帧再解一遍；
	//   - 记录末字节只是帧内的一个位置，不是可用的读取起点。
	pageStart := anchor
	if pageStart < 0 {
		pageStart = 0
	}
	consumed := pageStart
	switch {
	case last > first:
		pageStart = decoded[first].start
		consumed = decoded[last-1].end
	case len(decoded) > 0 && !backward:
		// 正向页一条记录都没解出来：游标仍要跨过已经检查过的帧，否则会原地打转。
		consumed = decoded[len(decoded)-1].end
	}

	page := TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: sessionID,
		Messages:  agentlog.UpsertEntries(records),
		Binding:   ChatBindingSelected,
		Reset:     reset,
		HasMore:   hasMore,
		Skipped:   skipped,
	}
	next, err := codec.seal(sessionID, rel, identity, strconv.FormatInt(consumed, 10))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = next
	// 与通用引擎同一条规矩：只有反向页才给 previous_cursor，正向游标页给了会让
	// 「加载更早」原地打转；反向页一条都没解出来时也不给，客户端据此收起入口。
	if backward && last > first && (first > 0 || oldestIndex > 1) {
		previous, err := codec.seal(sessionID, rel, identity, strconv.FormatInt(pageStart, 10))
		if err != nil {
			return transcriptDegrade(ChatReasonInternalError, agent)
		}
		page.PrevCursor = previous
	}
	return page
}

// dshDecodeFrame 解压并解码一帧，把记录偏移统一改写成**帧边界**。
//
// agentlog 按行给出 Start/End，但压缩容器里的行偏移不是可用的读取位置；这里统一覆盖成
// 该帧的压缩区间，让分页与游标只认识帧边界这一种坐标。记录身份来自事件的 seq，与偏移
// 无关，所以覆盖不会影响去重。
func dshDecodeFrame(decoder *dshDecoder, data []byte, frame dshFrame, line agentlog.LineContext) (dshDecodedFrame, error) {
	plain, err := decoder.decode(data[frame.Start:frame.End])
	if err != nil {
		return dshDecodedFrame{}, err
	}
	// 上游的每个批次帧都以换行收尾（`eventLines(events) + "\n"`），header 帧同理。
	// 一个**结构完整**的帧却不以换行收尾，说明它不是一份完整的批次：这时帧是完整的，
	// 永远等不到「写完之后重读」，所以它是格式错误，而不是「还没写完」。
	//
	// 这个检查不能省：agentlog 会丢弃尾部未闭合的半行，而游标是按整帧推进的 —— 放行
	// 就等于把帧尾那条记录永久跳过，客户端再也拿不到它。
	if len(plain) == 0 || plain[len(plain)-1] != '\n' {
		return dshDecodedFrame{}, errDSHBatchUnterminated
	}
	decoded := agentlog.DecodeRange(line, plain, frame.Start)
	if decoded.Failure != nil {
		// 同一偏移重读只会得到同一个 Failure，当成明确失败向上传播。
		return dshDecodedFrame{}, decoded.Failure
	}
	if decoded.Consumed != frame.Start+int64(len(plain)) {
		// 兜底：解出来的消费位置必须正好是整帧明文，否则上面那条换行断言就有漏网。
		return dshDecodedFrame{}, errDSHBatchUnterminated
	}
	out := dshDecodedFrame{start: frame.Start, end: frame.End, skipped: decoded.Skipped}
	for _, entry := range decoded.Entries {
		if entry.Record == nil {
			continue
		}
		encoded, err := json.Marshal(*entry.Record)
		if err != nil {
			continue
		}
		entry.Start = frame.Start
		entry.End = frame.End
		out.entries = append(out.entries, entry)
		out.sizes = append(out.sizes, len(encoded))
		out.bytes += len(encoded)
	}
	return out, nil
}

// dshSelectFrames 从已解码的帧里切出契约允许的一页，**只切在帧边界上**。
//
// backward 表示这一页保留窗口尾部（初始页、反向页、重来页），否则保留开头（正向游标页）。
// 第三个返回值表示「单帧自身就超过硬上限」——调用方必须把它变成 read_limit_exceeded，
// 因为切碎这一帧就等于丢记录。
func dshSelectFrames(frames []dshDecodedFrame, backward bool) (int, int, bool) {
	if backward {
		last := len(frames)
		first := last
		count, bytes := 0, 0
		for first > 0 {
			frame := frames[first-1]
			switch {
			case first == last:
				// 最新的那一帧必须自己装得下。
				if len(frame.entries) > transcriptPageRecordsHard || frame.bytes > transcriptMaxResponseBytes {
					return 0, 0, true
				}
			case count >= transcriptPageRecords:
				return first, last, false
			case count+len(frame.entries) > transcriptPageRecordsHard || bytes+frame.bytes > transcriptMaxResponseBytes:
				return first, last, false
			}
			count += len(frame.entries)
			bytes += frame.bytes
			first--
		}
		return first, last, false
	}
	last := 0
	count, bytes := 0, 0
	for last < len(frames) {
		frame := frames[last]
		switch {
		case last == 0:
			if len(frame.entries) > transcriptPageRecordsHard || frame.bytes > transcriptMaxResponseBytes {
				return 0, 0, true
			}
		case count >= transcriptPageRecords:
			return 0, last, false
		case count+len(frame.entries) > transcriptPageRecordsHard || bytes+frame.bytes > transcriptMaxResponseBytes:
			return 0, last, false
		}
		count += len(frame.entries)
		bytes += frame.bytes
		last++
	}
	return 0, last, false
}

// dshDecodeDegrade 把一帧的失败映射成契约里的降级原因。
//
// 超预算与「解不出来」对用户是不同的下一步：前者是这次读得太多，后者是这份产物已经
// 不可信。两者都不能被伪装成空日志。
func dshDecodeDegrade(err error, agent string) TranscriptPage {
	if errors.Is(err, errDSHDecodeLimit) {
		return transcriptDegrade(ChatReasonReadLimitExceeded, agent)
	}
	return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
}

// transcriptDSHEmpty 是一份「存在但还没有内容」的 DSH 产物的回答。
func transcriptDSHEmpty(agent, sessionID string) TranscriptPage {
	return TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: sessionID,
		Messages:  []agentlog.Record{},
		Binding:   ChatBindingSelected,
	}
}
