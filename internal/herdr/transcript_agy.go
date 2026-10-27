package herdr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// agy 的会话文件没有 cwd 字段。kind=id 的 uuid 就是文件名，归属不靠目录扫描，
// 也不和 pane cwd 对拍。pane cwd 仍然必须是绝对路径，这是读入口原有的检查。

type transcriptAgyQuery struct {
	Rel       string
	HasAfter  bool
	After     int64
	HasBefore bool
	Before    int64
	Desc      bool
	Limit     int
}

type transcriptAgyStep struct {
	Idx        int64
	StepType   int
	Status     int
	StepFormat int
	Payload    []byte
	Oversize   bool
}

type transcriptAgyBatch struct {
	Steps    []transcriptAgyStep
	Identity string
	End      bool
}

type transcriptAgyReader interface {
	ReadAgy(ctx context.Context, query transcriptAgyQuery) (transcriptAgyBatch, error)
}

func (f *localTranscriptFS) ReadAgy(ctx context.Context, query transcriptAgyQuery) (transcriptAgyBatch, error) {
	if !agentlog.AgyDBRel(query.Rel) {
		return transcriptAgyBatch{}, errTranscriptDenied
	}
	parts := []string{".gemini", "antigravity-cli", "conversations", query.Rel}
	file, err := openRegularNoFollow(f.resolvedHome(), parts)
	if err != nil {
		return transcriptAgyBatch{}, err
	}
	info, statErr := file.Stat()
	file.Close()
	if statErr != nil {
		return transcriptAgyBatch{}, errTranscriptDenied
	}
	path := filepath.Join(f.resolvedHome(), ".gemini", "antigravity-cli", "conversations", query.Rel)
	rows, end, err := agentlog.QueryAgyDB(ctx, path, agentlog.AgyBounds{
		HasAfter: query.HasAfter, After: query.After,
		HasBefore: query.HasBefore, Before: query.Before,
		Desc: query.Desc, Limit: query.Limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return transcriptAgyBatch{}, errTranscriptNotFound
		case errors.Is(err, agentlog.ErrAgyDenied):
			return transcriptAgyBatch{}, errTranscriptDenied
		case errors.Is(err, agentlog.ErrAgyUnrecognized):
			return transcriptAgyBatch{}, errTranscriptUnrecognized
		default:
			return transcriptAgyBatch{}, err
		}
	}
	batch := transcriptAgyBatch{Identity: fileIdentity(info), End: end, Steps: make([]transcriptAgyStep, 0, len(rows))}
	for _, row := range rows {
		batch.Steps = append(batch.Steps, transcriptAgyStep{
			Idx: row.Idx, StepType: row.StepType, Status: row.Status,
			StepFormat: row.StepFormat, Payload: row.Payload, Oversize: row.Oversize,
		})
	}
	return batch, nil
}

func (f *sshTranscriptFS) ReadAgy(ctx context.Context, query transcriptAgyQuery) (transcriptAgyBatch, error) {
	response, err := f.endpoint.execTranscriptReader(ctx, transcriptRemoteRequest{
		Op: "agy", Agent: string(transcriptRootAgy), Rel: query.Rel,
		AgyHasAfter: query.HasAfter, AgyAfter: query.After,
		AgyHasBefore: query.HasBefore, AgyBefore: query.Before,
		AgyDesc: query.Desc, AgyLimit: query.Limit,
	})
	if err != nil {
		f.rememberReadError(err)
		return transcriptAgyBatch{}, err
	}
	batch := transcriptAgyBatch{Identity: response.Identity, End: response.End, Steps: make([]transcriptAgyStep, 0, len(response.Steps))}
	for _, step := range response.Steps {
		payload, decodeErr := base64.StdEncoding.DecodeString(step.Data)
		if decodeErr != nil {
			return transcriptAgyBatch{}, errTranscriptDenied
		}
		batch.Steps = append(batch.Steps, transcriptAgyStep{
			Idx: step.Idx, StepType: step.StepType, Status: step.Status,
			StepFormat: step.Format, Payload: payload, Oversize: step.Oversize,
		})
	}
	return batch, nil
}

func transcriptAgyMessages(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd, sessionID, rel string, request TranscriptRequest) TranscriptPage {
	reader, ok := files.(transcriptAgyReader)
	if !ok {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	if !agentlog.AgyUUID(sessionID) || rel != sessionID+".db" {
		return transcriptDegrade(ChatReasonReadDenied, agent)
	}
	probe, err := reader.ReadAgy(ctx, transcriptAgyQuery{Rel: rel, Desc: true, Limit: 1})
	if err != nil {
		if errors.Is(err, errTranscriptNotFound) {
			return transcriptSessionLost(ctx, files, codec, agent, cwd)
		}
		return transcriptDegradeFromError(err, agent)
	}
	token := []string{sessionID, rel, probe.Identity}
	desc := true
	reset := false
	var hasAfter, hasBefore bool
	var after, before int64
	switch {
	case request.Before != "":
		offset, ok := transcriptCursorOffset(codec, request.Before, token)
		if !ok || offset <= 0 {
			reset = true
		} else {
			hasBefore = true
			before = offset
		}
	case request.Cursor != "":
		offset, ok := transcriptCursorOffset(codec, request.Cursor, token)
		if !ok || offset < 0 {
			reset = true
		} else {
			desc = false
			hasAfter = true
			after = offset
		}
	}
	if reset {
		desc = true
		hasAfter, hasBefore = false, false
		after, before = 0, 0
	}
	collected, err := agyCollect(ctx, reader, rel, probe.Identity, desc, hasAfter, after, hasBefore, before)
	if err != nil {
		if errors.Is(err, errTranscriptNotFound) {
			return transcriptSessionLost(ctx, files, codec, agent, cwd)
		}
		return transcriptDegradeFromError(err, agent)
	}
	if collected.identityMismatch {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	backward := desc
	records := collected.records
	if backward {
		for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
			records[left], records[right] = records[right], records[left]
		}
	}
	page := TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: sessionID,
		Messages:  records,
		Binding:   ChatBindingSelected,
		Reset:     reset,
		Skipped:   collected.skipped,
		HasMore:   collected.hasMore,
	}
	if page.Messages == nil {
		page.Messages = []agentlog.Record{}
	}
	// 反向页的正向游标停在请求锚点上，避免「加载更早」把轮询起点拖回去。
	// 初始页停在扫描到的最新 idx（含没有正文、被跳过的 step）。
	// 正向页停在本页真正消费过的最后一行，不能越过还没返回的记录。
	newest := collected.tipIdx
	switch {
	case backward && hasBefore:
		newest = before
	case !backward && collected.advanced:
		newest = collected.forwardIdx
	case !backward:
		newest = after
	}
	next, err := codec.seal(sessionID, rel, probe.Identity, strconv.FormatInt(newest, 10))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = next
	if backward && collected.hasMore && len(records) > 0 {
		oldest, err := strconv.ParseInt(records[0].ID, 10, 64)
		if err != nil || oldest <= 0 {
			return page
		}
		previous, err := codec.seal(sessionID, rel, probe.Identity, strconv.FormatInt(oldest, 10))
		if err != nil {
			return transcriptDegrade(ChatReasonInternalError, agent)
		}
		page.PrevCursor = previous
	}
	return page
}

type agyCollected struct {
	records          []agentlog.Record
	skipped          int
	hasMore          bool
	tipIdx           int64
	forwardIdx       int64
	advanced         bool
	identityMismatch bool
}

func agyCollect(ctx context.Context, reader transcriptAgyReader, rel, identity string, desc bool, hasAfter bool, after int64, hasBefore bool, before int64) (agyCollected, error) {
	collected := agyCollected{}
	remaining := transcriptMaxResponseBytes
	seenTip := false
	for round := 0; round < 16; round++ {
		batch, err := reader.ReadAgy(ctx, transcriptAgyQuery{
			Rel: rel, HasAfter: hasAfter, After: after, HasBefore: hasBefore, Before: before,
			Desc: desc, Limit: 64,
		})
		if err != nil {
			return agyCollected{}, err
		}
		if batch.Identity != identity {
			collected.identityMismatch = true
			return collected, nil
		}
		if len(batch.Steps) == 0 {
			collected.hasMore = false
			return collected, nil
		}
		budgetStop := false
		for _, step := range batch.Steps {
			if desc && !seenTip {
				collected.tipIdx = step.Idx
				seenTip = true
			}
			record, _ := agentlog.DecodeAgyStep(step.Idx, step.StepType, step.Status, step.StepFormat, step.Payload)
			if step.Oversize {
				record = nil
			}
			var encoded []byte
			if record != nil {
				encoded, err = json.Marshal(record)
				if err != nil {
					record = nil
				}
			}
			if record != nil && len(collected.records) > 0 && (len(collected.records) >= transcriptPageRecords || len(encoded) > remaining) {
				budgetStop = true
				break
			}
			if !desc {
				collected.forwardIdx = step.Idx
				collected.advanced = true
			}
			if record == nil {
				collected.skipped++
				continue
			}
			if len(encoded) < remaining {
				remaining -= len(encoded)
			} else {
				remaining = 0
			}
			collected.records = append(collected.records, *record)
		}
		if budgetStop {
			collected.hasMore = true
			return collected, nil
		}
		last := batch.Steps[len(batch.Steps)-1].Idx
		if desc {
			hasBefore = true
			before = last
		} else {
			hasAfter = true
			after = last
		}
		if batch.End {
			collected.hasMore = false
			return collected, nil
		}
	}
	collected.hasMore = true
	return collected, nil
}
