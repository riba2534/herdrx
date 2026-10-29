package herdr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 候选发现仍整份回放，超过这个预算就跳过该文件。已经绑定的会话不因此整份失败，
// 改为只读尾部既有窗口（transcriptInitialWindowBytes）。
const transcriptGeminiMaxBytes = 8 << 20

func readGeminiDocument(ctx context.Context, files transcriptFS, rel string, size int64) (agentlog.GeminiDocument, error) {
	if size < 0 {
		size = 0
	}
	if size > transcriptGeminiMaxBytes {
		return agentlog.GeminiDocument{}, errTranscriptLimit
	}
	var data []byte
	var identity string
	for offset := int64(0); offset < size; {
		length := min(int64(transcriptInitialWindowBytes), size-offset)
		part, err := transcriptReadOne(ctx, files, transcriptRootGemini, rel, offset, length)
		if err != nil {
			return agentlog.GeminiDocument{}, err
		}
		if part.Err != nil {
			return agentlog.GeminiDocument{}, part.Err
		}
		if !part.Exists || len(part.Data) == 0 {
			break
		}
		if identity == "" {
			identity = part.Identity
		} else if part.Identity != identity {
			return agentlog.GeminiDocument{}, errTranscriptNotFound
		}
		data = append(data, part.Data...)
		offset += int64(len(part.Data))
	}
	return agentlog.DecodeGemini(rel, data), nil
}

func transcriptGeminiMessages(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd, sessionID, rel string, request TranscriptRequest) TranscriptPage {
	root := transcriptRootGemini
	shapes, heads, err := transcriptShapes(ctx, files, root, agent, []string{rel})
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	head := heads[0]
	if head.Err != nil {
		if errors.Is(head.Err, errTranscriptDenied) {
			return transcriptDegrade(ChatReasonReadDenied, agent)
		}
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	if !head.Exists {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	_ = shapes
	if head.Size > transcriptGeminiMaxBytes {
		return transcriptGeminiTail(ctx, files, codec, agent, cwd, sessionID, rel, request, head)
	}
	document, err := readGeminiDocument(ctx, files, rel, head.Size)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if document.SessionID == "" || (sessionID != "" && document.SessionID != sessionID) || !geminiOwned(ctx, files, document.ProjectHash, rel, cwd) {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	sessionID = document.SessionID
	// 游标是记录下标，身份只用会话自己的稳定字段：Gemini 每次写入都会重写整份文档，文件大小和
	// inode 都可能变，用它们当身份会让每次增长都 reset。
	identity := "doc:" + document.SessionID + ":" + document.StartTime
	token := []string{document.SessionID, rel, identity}
	reset := false
	start := 0
	switch {
	case request.Before != "":
		offset, ok := geminiRecordCursor(codec, request.Before, token, document.Records)
		if !ok || offset <= 0 {
			reset = true
			start = len(document.Records)
			break
		}
		start = offset
	case request.Cursor != "":
		offset, ok := geminiRecordCursor(codec, request.Cursor, token, document.Records)
		if !ok {
			reset = true
			start = len(document.Records)
			break
		}
		start = offset
	default:
		start = len(document.Records)
	}
	backward := request.Cursor == "" || request.Before != "" || reset
	records := document.Records
	var pageRecords []agentlog.Record
	var first, next int
	if backward {
		pageRecords, first = tailGeminiRecords(records[:start])
		next = start
		if len(pageRecords) == 0 {
			first = start
		}
	} else {
		pageRecords, next = headGeminiRecords(records[start:])
		next += start
		first = start
	}
	page := TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: document.SessionID,
		Messages:  pageRecords,
		Binding:   ChatBindingSelected,
		Reset:     reset,
	}
	if page.Messages == nil {
		page.Messages = []agentlog.Record{}
	}
	if backward {
		page.HasMore = first > 0
	} else {
		page.HasMore = next < len(records)
	}
	consumed := int64(next)
	if !backward && next == len(records) {
		consumed = int64(len(records))
	}
	sealed, err := codec.seal(document.SessionID, rel, identity, geminiRecordPosition(records, int(consumed)))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = sealed
	if backward && first > 0 && len(pageRecords) > 0 {
		previous, err := codec.seal(document.SessionID, rel, identity, geminiRecordPosition(records, first))
		if err != nil {
			return transcriptDegrade(ChatReasonInternalError, agent)
		}
		page.PrevCursor = previous
	}
	return page
}

func tailGeminiRecords(records []agentlog.Record) ([]agentlog.Record, int) {
	if len(records) == 0 {
		return []agentlog.Record{}, 0
	}
	first := len(records)
	size := 0
	count := 0
	for first > 0 {
		encoded := len(records[first-1].ID) + textSize(records[first-1])
		if count >= transcriptPageRecords || (size+encoded > transcriptMaxResponseBytes && count > 0) {
			break
		}
		size += encoded
		count++
		first--
	}
	return records[first:], first
}

func headGeminiRecords(records []agentlog.Record) ([]agentlog.Record, int) {
	last := 0
	size := 0
	for last < len(records) {
		encoded := len(records[last].ID) + textSize(records[last])
		if last >= transcriptPageRecords || (size+encoded > transcriptMaxResponseBytes && last > 0) {
			break
		}
		size += encoded
		last++
	}
	return records[:last], last
}

func textSize(record agentlog.Record) int {
	total := 0
	for _, block := range record.Blocks {
		total += len(block.Text) + len(block.Output) + len(block.Name)
	}
	return total
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

// transcriptGeminiTail 只读已绑定会话的尾部窗口。窗口和响应预算都不提高。
// 还有更早的字节时返回 before 游标，而不是对整份文件报 read_limit_exceeded。
func transcriptGeminiTail(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd, sessionID, rel string, request TranscriptRequest, head transcriptReadResult) TranscriptPage {
	headDoc, err := geminiPrefix(ctx, files, rel, head.Size)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if headDoc.SessionID != "" {
		if sessionID != "" && headDoc.SessionID != sessionID {
			return transcriptSessionLost(ctx, files, codec, agent, cwd)
		}
		sessionID = headDoc.SessionID
	}
	// 尾部窗口的游标是字节位置，必须仍是同一个文件；文件增长不改变身份。
	identity := "tail:" + head.Identity + ":" + headDoc.StartTime
	token := []string{sessionID, rel, identity}
	end := head.Size
	reset := false
	switch {
	case request.Before != "":
		offset, ok, err := geminiByteCursor(ctx, files, codec, request.Before, token, rel, head)
		if err != nil {
			return transcriptDegradeFromError(err, agent)
		}
		if !ok || offset <= 0 {
			reset = true
		} else {
			end = offset
		}
	case request.Cursor != "":
		offset, ok, err := geminiByteCursor(ctx, files, codec, request.Cursor, token, rel, head)
		if err != nil {
			return transcriptDegradeFromError(err, agent)
		}
		if !ok {
			reset = true
		} else if offset == head.Size {
			// 没有新字节：原样交回同一个游标（锚点已经核对过）。
			return TranscriptPage{Supported: true, Agent: agent, SessionID: sessionID, Messages: []agentlog.Record{}, NextCursor: request.Cursor, Binding: ChatBindingSelected}
		} else {
			end = head.Size
		}
	}
	start := end - int64(transcriptInitialWindowBytes)
	if start < 0 {
		start = 0
	}
	part, err := transcriptReadOne(ctx, files, transcriptRootGemini, rel, start, end-start)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if part.Err != nil || !part.Exists || part.Identity != head.Identity {
		if errors.Is(part.Err, errTranscriptDenied) {
			return transcriptDegrade(ChatReasonReadDenied, agent)
		}
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	aligned := part.Data
	alignedStart := start
	if start > 0 {
		if index := bytes.IndexByte(aligned, '\n'); index >= 0 {
			aligned = aligned[index+1:]
			alignedStart += int64(index) + 1
		} else {
			aligned = nil
			alignedStart = end
		}
	}
	document := agentlog.DecodeGeminiAt(rel, aligned, alignedStart, sessionID)
	if sessionID == "" {
		sessionID = document.SessionID
	}
	// 归属只认文件头的 projectHash；窗口里的正文不参与判断。
	if sessionID == "" || !geminiOwned(ctx, files, headDoc.ProjectHash, rel, cwd) {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	pageRecords, first := tailGeminiRecords(document.Records)
	page := TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: sessionID,
		Messages:  pageRecords,
		Binding:   ChatBindingSelected,
		Reset:     reset,
		HasMore:   alignedStart > 0 || first > 0,
	}
	if page.Messages == nil {
		page.Messages = []agentlog.Record{}
	}
	next, err := codec.seal(sessionID, rel, identity, geminiBytePosition(part.Data, start, end))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = next
	if alignedStart > 0 {
		previous, err := codec.seal(sessionID, rel, identity, geminiBytePosition(part.Data, start, alignedStart))
		if err != nil {
			return transcriptDegrade(ChatReasonInternalError, agent)
		}
		page.PrevCursor = previous
	}
	return page
}

// 游标位置带一个锚点，防止「同一会话被截短后又长回来」时按旧位置续读：
//   - 整份文档模式：位置是记录下标，锚点是它前一条记录 id 的摘要。/rewind 或压缩后，
//     同一下标前面已经不是原来那条记录，对不上就 reset。
//   - 尾部窗口模式：位置是字节偏移，锚点是偏移前最多 64 字节的摘要。原地重写会让这几个
//     字节变掉；单纯在末尾追加不会。
const geminiAnchorBytes = 64

func geminiDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func geminiRecordPosition(records []agentlog.Record, index int) string {
	anchor := "-"
	if index > 0 && index <= len(records) {
		anchor = geminiDigest([]byte(records[index-1].ID))
	}
	return formatInt(int64(index)) + "." + anchor
}

func geminiRecordCursor(codec transcriptCodec, token string, expect []string, records []agentlog.Record) (int, bool) {
	position, ok := geminiCursorPosition(codec, token, expect)
	if !ok {
		return 0, false
	}
	index, _, _ := strings.Cut(position, ".")
	value, err := strconv.Atoi(index)
	if err != nil || value < 0 || value > len(records) || geminiRecordPosition(records, value) != position {
		return 0, false
	}
	return value, true
}

// geminiBytePosition 用手里这段 data（从文件偏移 base 开始）给 offset 生成带锚点的位置。
func geminiBytePosition(data []byte, base, offset int64) string {
	size := min(int64(geminiAnchorBytes), offset-base)
	if size < 0 {
		size = 0
	}
	window := data[offset-base-size : offset-base]
	return formatInt(offset) + "." + formatInt(size) + "." + geminiDigest(window)
}

// geminiByteCursor 解开尾部窗口游标，并重读偏移前的锚点字节核对。
func geminiByteCursor(ctx context.Context, files transcriptFS, codec transcriptCodec, token string, expect []string, rel string, head transcriptReadResult) (int64, bool, error) {
	position, ok := geminiCursorPosition(codec, token, expect)
	if !ok {
		return 0, false, nil
	}
	fields := strings.Split(position, ".")
	if len(fields) != 3 {
		return 0, false, nil
	}
	offset, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || offset < 0 || offset > head.Size {
		return 0, false, nil
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || size < 0 || size > geminiAnchorBytes || size > offset {
		return 0, false, nil
	}
	part, err := transcriptReadOne(ctx, files, transcriptRootGemini, rel, offset-size, size)
	if err != nil {
		return 0, false, err
	}
	if part.Err != nil || !part.Exists || part.Identity != head.Identity || int64(len(part.Data)) != size {
		return 0, false, nil
	}
	return offset, geminiDigest(part.Data) == fields[2], nil
}

func geminiCursorPosition(codec transcriptCodec, token string, expect []string) (string, bool) {
	parts, ok := codec.open(token, 4)
	if !ok {
		return "", false
	}
	for index, value := range expect {
		if parts[index] != value {
			return "", false
		}
	}
	return parts[3], true
}

func geminiPrefix(ctx context.Context, files transcriptFS, rel string, size int64) (agentlog.GeminiDocument, error) {
	length := min(size, int64(transcriptShapeMaxBytes))
	part, err := transcriptReadOne(ctx, files, transcriptRootGemini, rel, 0, length)
	if err != nil {
		return agentlog.GeminiDocument{}, err
	}
	if part.Err != nil {
		return agentlog.GeminiDocument{}, part.Err
	}
	return agentlog.DecodeGemini(rel, part.Data), nil
}
