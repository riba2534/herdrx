package herdr

import (
	"context"
	"errors"
	"strconv"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 一份 Gemini 会话要整份回放后才能知道消息和工作区。超过这个预算就明确说读不下。
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
	document, err := readGeminiDocument(ctx, files, rel, head.Size)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if document.SessionID == "" || (sessionID != "" && document.SessionID != sessionID) || document.CWD != cwd {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	identity := head.Identity + ":" + formatInt(head.Size)
	token := []string{document.SessionID, rel, identity}
	reset := false
	start := 0
	switch {
	case request.Before != "":
		offset, ok := transcriptCursorOffset(codec, request.Before, token)
		if !ok || offset <= 0 || offset > int64(len(document.Records)) {
			reset = true
			start = len(document.Records)
			break
		}
		start = int(offset)
	case request.Cursor != "":
		offset, ok := transcriptCursorOffset(codec, request.Cursor, token)
		if !ok || offset < 0 || offset > int64(len(document.Records)) {
			reset = true
			start = len(document.Records)
			break
		}
		start = int(offset)
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
	sealed, err := codec.seal(document.SessionID, rel, identity, formatInt(consumed))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = sealed
	if backward && first > 0 && len(pageRecords) > 0 {
		previous, err := codec.seal(document.SessionID, rel, identity, formatInt(int64(first)))
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
