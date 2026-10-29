package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// TranscriptCommand is the only exec name the access daemon accepts for a
// structured session read. It mirrors a fixed method such as pane.read: no
// arguments, no shell, and no path on the command line. The request is JSON
// on stdin. The response never carries cwd.
const TranscriptCommand = "herdrx-transcript"

// Bounds match the existing remote reader: 500 directory entries, 1 MiB per
// read, depth at most 5, and at most 64 reads in one request. One response can
// therefore exceed the workbench's 2 MiB LimitReader (64 reads of 1 MiB,
// base64-encoded). The workbench never asks for that much: its pages stay
// within a 256 KiB window and a 64 KiB head per file, and an oversized reply
// is rejected as a retryable error there instead of being truncated.
const (
	transcriptEntryLimit = 500
	transcriptMaxRead    = 1 << 20
	transcriptMaxDepth   = 5
	transcriptMaxReads   = 64
	transcriptRequestMax = 256 << 10
)

// transcriptRoots are the only directories a transcript read may enter.
// Callers never supply a root. agy is one sqlite file named by the session uuid.
var transcriptRoots = map[string]string{
	"claude": ".claude/projects",
	"codex":  ".codex/sessions",
	"traex":  ".trae/cli/sessions",
	"dsh":    ".dsh/sessions",
	"cursor": ".cursor/projects",
	"gemini": ".gemini/tmp",
	"agy":    ".gemini/antigravity-cli/conversations",
	"pi":     ".pi/agent/sessions",
	"grok":   ".grok/sessions",
}

type transcriptWireRequest struct {
	Op           string               `json:"op"`
	Agent        string               `json:"agent"`
	Rel          string               `json:"rel"`
	Depth        int                  `json:"depth"`
	Reads        []transcriptWireSpec `json:"reads"`
	AgyHasAfter  bool                 `json:"agy_has_after,omitempty"`
	AgyAfter     int64                `json:"agy_after,omitempty"`
	AgyHasBefore bool                 `json:"agy_has_before,omitempty"`
	AgyBefore    int64                `json:"agy_before,omitempty"`
	AgyDesc      bool                 `json:"agy_desc,omitempty"`
	AgyLimit     int                  `json:"agy_limit,omitempty"`
}

type transcriptWireSpec struct {
	Rel    string `json:"rel"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type transcriptWireEntry struct {
	Rel      string `json:"rel"`
	Dir      bool   `json:"dir"`
	Link     bool   `json:"link"`
	Size     int64  `json:"size"`
	Modified int64  `json:"mtime"`
}

type transcriptWireRead struct {
	Exists   bool   `json:"exists"`
	Size     int64  `json:"size"`
	Data     string `json:"data"`
	Error    string `json:"error"`
	Identity string `json:"identity"`
}

type transcriptWireStep struct {
	Idx      int64  `json:"idx"`
	StepType int    `json:"step_type"`
	Status   int    `json:"status"`
	Format   int    `json:"step_format"`
	Data     string `json:"data,omitempty"`
	Oversize bool   `json:"oversize,omitempty"`
}

type transcriptWireResponse struct {
	OK       bool                  `json:"ok"`
	Error    string                `json:"error,omitempty"`
	Entries  []transcriptWireEntry `json:"entries,omitempty"`
	Reads    []transcriptWireRead  `json:"reads,omitempty"`
	Steps    []transcriptWireStep  `json:"steps,omitempty"`
	Identity string                `json:"identity,omitempty"`
	End      bool                  `json:"end,omitempty"`
	Home     string                `json:"home,omitempty"`
	HomeReal string                `json:"home_real,omitempty"`
}

// ServeTranscript reads one whitelisted request and writes one JSON response.
// A platform that cannot do the bounded open returns an error so the command
// exits non-zero. That failure stays retryable on the client. It is not a
// permanent "this host can never read transcripts" result.
func ServeTranscript(r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(r, transcriptRequestMax+1))
	if err != nil {
		return writeTranscript(w, transcriptWireResponse{Error: "bad_request"})
	}
	if int64(len(raw)) > transcriptRequestMax {
		return writeTranscript(w, transcriptWireResponse{Error: "bad_request"})
	}
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasPrefix(home, "/") {
		return writeTranscript(w, transcriptWireResponse{Error: "home_unavailable"})
	}
	response, err := serveTranscript(home, raw)
	if err != nil {
		return err
	}
	return writeTranscript(w, response)
}

func writeTranscript(w io.Writer, response transcriptWireResponse) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

func decodeTranscriptRequest(raw []byte) (transcriptWireRequest, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return transcriptWireRequest{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var request transcriptWireRequest
	if err := decoder.Decode(&request); err != nil {
		return transcriptWireRequest{}, errBadTranscriptRequest
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return transcriptWireRequest{}, errBadTranscriptRequest
	}
	return request, nil
}

var errBadTranscriptRequest = errors.New("bad transcript request")
var errBadRel = errors.New("bad transcript path")

func splitRel(rel string) ([]string, error) {
	text := strings.TrimSpace(rel)
	if text == "" || text == "." {
		return nil, nil
	}
	if strings.HasPrefix(text, "/") || strings.Contains(text, `\`) || strings.Contains(text, "\x00") {
		return nil, errBadRel
	}
	parts := strings.Split(text, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errBadRel
		}
	}
	return parts, nil
}

func clampDepth(depth int) int {
	if depth < 1 {
		return 1
	}
	if depth > transcriptMaxDepth {
		return transcriptMaxDepth
	}
	return depth
}
