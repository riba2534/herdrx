package herdr

// 结构化对话记录：`Endpoint` 的**可选能力**。
//
// 权威数据源是 Agent 自己写下的 append-only 会话日志，读取能力走独立的、owner 认证的
// HTTP 路由，**不进入 WebSocket 的 RPC 方法白名单**（那条路径零参数校验、原样透传，
// 接入文件读取等于开放路径穿越）。
//
// 这里刻意**不改 `herdr.Endpoint`** —— 它已有本机 / 原生 SSH / 系统 OpenSSH 三套实现与
// 大量测试桩；新能力用独立接口 `TranscriptEndpoint`，与 `NativeScrollEndpoint` 同风格。
//
// 语义契约见 `docs/design/structured-chat-contract.md`，
// 字段正文见 `web/src/lib/structuredChatTypes.ts`。两边都是冻结的，改字段要先改契约。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riba2534/herdrx/internal/agentlog"
)

// 契约上限（`STRUCTURED_CHAT_*`）。服务端可以调低，任何一侧都不得调高。
const (
	// 一页默认记录数；同时也是硬上限 500 之内的取值，服务端永不超出它。
	transcriptPageRecords = 200
	// 单次响应体上限：超出的记录留到下一页，绝不截断成半条记录。
	transcriptMaxResponseBytes = 256 * 1024
	// 首屏（无游标）只读日志尾部这一段字节，避免打开大文件就全量传输。
	transcriptInitialWindowBytes = 256 * 1024
	// 单次最多返回的候选数。
	transcriptMaxCandidates = 20
	// 校验候选 cwd 时先读的文件头长度。单行可能很长（一次粘贴的代码），所以这只是
	// 起始窗口，读不出结论时按需翻倍到 transcriptShapeMaxBytes。
	transcriptHeadBytes = 4096
	// 探测会话身份时的窗口上限。
	transcriptShapeMaxBytes = 64 * 1024
	// 反向对齐游标时需要回看的字节数。
	transcriptAlignProbeBytes = 4096
	// 一次 list 最多返回的目录项数。
	transcriptMaxListEntries = 500
	// Codex 会话目录的最大递归深度（`sessions/YYYY/MM/DD/rollout-*.jsonl`）。
	transcriptMaxCodexDepth = 4
)

// 降级原因，闭集合。取值必须与契约 `CHAT_REASONS` 逐字一致，不允许自造字符串。
const (
	ChatReasonUnsupportedAgent     = "unsupported_agent"
	ChatReasonNoAgent              = "no_agent"
	ChatReasonUnsupportedTransport = "unsupported_transport"
	ChatReasonCWDUnavailable       = "cwd_unavailable"
	ChatReasonLogRootUnavailable   = "log_root_unavailable"
	ChatReasonSessionUnavailable   = "session_unavailable"
	ChatReasonNoSessionCandidates  = "no_session_candidates"
	ChatReasonReadDenied           = "read_denied"
	ChatReasonUnrecognizedFormat   = "unrecognized_format"
	ChatReasonReadLimitExceeded    = "read_limit_exceeded"
	ChatReasonInternalError        = "internal_error"
)

// 绑定来源。本期只允许 `selected`：即使候选只剩一个也**不自动认领**。
// `verified` 在契约里预留但本期不得产生 —— herdrx 目前没有 hook 级别的 pane/provider
// 会话证据源，凭空返回 verified 就是臆造绑定。
const ChatBindingSelected = "selected"

// TranscriptScope 是服务端自己取到的 pane 事实。agent 与 cwd 都来自
// `endpoint.Snapshot`，**绝不接受客户端传入** —— 否则客户端可以谎报 cwd 去读别的项目。
type TranscriptScope struct {
	PaneID string
	Agent  string
	CWD    string
}

// TranscriptRequest 是三个不透明值的组合。客户端只回传，不解析、不构造、不缓存成路径。
type TranscriptRequest struct {
	// Session 省略 = 只列候选、不返回任何消息。
	Session string
	// Cursor 是正向增量游标。与 Before 互斥；同时出现时以 Before 为准。
	Cursor string
	Before string
}

// TranscriptPage 是端点的完整回答。所有降级情形同样是「成功返回」，由 Reason 表达。
type TranscriptPage struct {
	Supported  bool
	Reason     string
	Agent      string
	Candidates []agentlog.Candidate
	SessionID  string
	Messages   []agentlog.Record
	NextCursor string
	PrevCursor string
	Reset      bool
	HasMore    bool
	Skipped    int
	Binding    string
}

// TranscriptEndpoint 是可选能力接口。实现它的 Endpoint 才有结构化会话记录可读。
type TranscriptEndpoint interface {
	Transcript(ctx context.Context, scope TranscriptScope, request TranscriptRequest) (TranscriptPage, error)
}

// AsTranscriptEndpoint 探测一个 Endpoint 是否具备结构化记录能力。
// 不支持就是 `supported:false` + `unsupported_transport`，绝不假装支持。
func AsTranscriptEndpoint(endpoint Endpoint) (TranscriptEndpoint, bool) {
	reader, ok := endpoint.(TranscriptEndpoint)
	return reader, ok
}

// ── 不透明令牌 ──
//
// 候选 id 与游标都是服务端派生的不透明串，内部绑定「agent / 会话 id / 相对路径」或
// 「会话 id / 相对路径 / 文件身份 / 字节偏移」。用 AES-GCM 封装，因此：客户端无法伪造、
// 无法从串里读出路径、也拿不到任何可以指向别处的句柄。密钥是进程内随机的，所以服务重启
// 会让旧令牌失效 —— 客户端拿到 `session_unavailable` + `reset` 后重新选择，这是安全的降级。

type transcriptCodec struct{ aead cipher.AEAD }

func newTranscriptCodec() (transcriptCodec, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return transcriptCodec{}, fmt.Errorf("generate structured chat token key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return transcriptCodec{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return transcriptCodec{}, err
	}
	return transcriptCodec{aead: aead}, nil
}

var transcriptTokens = sync.OnceValues(newTranscriptCodec)

func (c transcriptCodec) seal(parts ...string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(strings.Join(parts, "\x00")), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open 解开令牌，并要求恰好得到 count 个分量。任何一个分量含分隔符都会让分量数变化，
// 因此天然被拒绝。
func (c transcriptCodec) open(token string, count int) ([]string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return nil, false
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(string(plain), "\x00")
	if len(parts) != count {
		return nil, false
	}
	return parts, true
}

// ── 受限文件访问 ──
//
// 进程内（本机）与远端（SSH）实现共用同一套语义：固定日志根、严格子路径、
// 拒绝符号链接、有界读取。读取方只负责「取字节」，解析与判定全在 agentlog 与这里。
//
// 日志根是三个固定值：Claude 的 `<HOME>/.claude/projects`、Codex 的
// `<HOME>/.codex/sessions` 与 DSH 的 `<HOME>/.dsh/sessions`
// （上游 `dsh-base/cordis.patch.yml` 的 `dshHomePath('sessions')`）。调用方永远不传路径。

type transcriptRootKind string

const (
	transcriptRootClaude transcriptRootKind = "claude"
	transcriptRootCodex  transcriptRootKind = "codex"
	transcriptRootDSH    transcriptRootKind = "dsh"
)

func transcriptRootFor(agent string) transcriptRootKind {
	switch agent {
	case agentlog.AgentCodex:
		return transcriptRootCodex
	case agentlog.AgentDSH:
		return transcriptRootDSH
	}
	return transcriptRootClaude
}

var (
	// errTranscriptDenied 表示路径越界、符号链接或非普通文件被拒。
	errTranscriptDenied = errors.New("structured chat path denied")
	// errTranscriptNotFound 表示路径不存在（不是安全事件）。
	errTranscriptNotFound = errors.New("structured chat path missing")
	// errTranscriptRoot 表示日志根不存在、不可读，或取不到 HOME。
	errTranscriptRoot = errors.New("structured chat log root unavailable")
	// errTranscriptTransport 表示当前接入方式拿不到受限读取能力。
	errTranscriptTransport = errors.New("structured chat transport unsupported")
)

type transcriptEntry struct {
	Rel      string
	Dir      bool
	Link     bool
	Size     int64
	Modified time.Time
}

type transcriptReadSpec struct {
	Rel    string
	Offset int64
	Length int64
}

type transcriptReadResult struct {
	Data []byte
	Size int64
	// Identity 是文件的**身份**（设备 + inode）。游标绑定它，用来区分「同一个会话继续追加」
	// 与「同一个路径被换成了另一个文件」。它必须在文件追加时保持稳定，所以不能拿内容前缀
	// 做哈希 —— 小文件一旦增长，前缀哈希就变了，会把正常的增量轮询误判成轮转。
	Identity string
	Exists   bool
	Err      error
}

type transcriptFS interface {
	// List 列出 root 下相对路径 rel 处的目录项。depth 是相对 rel 的最大递归层数。
	List(ctx context.Context, root transcriptRootKind, rel string, depth int) ([]transcriptEntry, error)
	// Read 读取若干有界区间。每个区间独立成败，不因一个失败而整体失败。
	Read(ctx context.Context, root transcriptRootKind, specs []transcriptReadSpec) ([]transcriptReadResult, error)
}

// transcriptRelParts 校验并切分相对路径。任何一段为空、`.`、`..`，或含反斜杠 / NUL /
// 前导斜杠的路径一律拒绝 —— 这是「客户端永不提供路径」之上的第二道闸门。
func transcriptRelParts(rel string) ([]string, error) {
	if rel == "" {
		return nil, nil
	}
	if strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\\x00") {
		return nil, errTranscriptDenied
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errTranscriptDenied
		}
	}
	return parts, nil
}

// transcriptRelAllowed 重新校验一个来自客户端的相对路径仍然落在「当前 pane 的范围」内。
//
// 客户端传回的只是不透明 id，服务端把它解析回**自己已经认定过的那一个文件**；这一步确认
// 解析结果仍是该 agent 日志根下的合法会话文件，因此 id 不是任意路径许可。
//
// DSH 的检查最严：三段路径必须**逐字**等于「cwd 的项目目录 + 会话 id 的规范编码 + 受支持
// 的文件名」。项目目录编码是有损的（分隔符折叠、截断），所以它只用来缩小范围；
// 真正证明归属的仍然是 header 里 cwd/id 的精确相等，在那个检查之前这里的对拍只是一道
// 闸门 —— 它保证客户端拿到的 token 不可能被改写成指向另一个会话。
func transcriptRelAllowed(agent, cwd, sessionID, rel string) bool {
	parts, err := transcriptRelParts(rel)
	if err != nil || len(parts) == 0 {
		return false
	}
	name := parts[len(parts)-1]
	switch agent {
	case agentlog.AgentClaude:
		// `<编码目录>/<会话 id>.jsonl`：直属一层。
		return len(parts) == 2 && agentlog.ClaudeSessionFile(name)
	case agentlog.AgentCodex:
		// `YYYY/MM/DD/rollout-*.jsonl`：层数有界，不做递归通配。
		return len(parts) >= 1 && len(parts) <= transcriptMaxCodexDepth+1 && agentlog.CodexSessionFile(name)
	case agentlog.AgentDSH:
		// `<项目目录>/<会话目录>/session.v3.jsonl[.zstd]`：恰好三层。
		if len(parts) != 3 {
			return false
		}
		if !agentlog.DSHSessionFile(name) && !agentlog.DSHCompressedSessionFile(name) {
			return false
		}
		expected, ok := dshRelForSession(cwd, sessionID, name)
		return ok && expected == rel
	}
	return false
}

// ── 入口 ──

// readTranscript 是共用引擎：判定 agent / cwd → 解析候选或会话 → 按游标切片 → 返回记录。
// 它从不返回 Go 错误：所有失败都表达成契约里的降级形状，客户端才能渲染诚实的空态。
func readTranscript(ctx context.Context, files transcriptFS, codec transcriptCodec, scope TranscriptScope, request TranscriptRequest) TranscriptPage {
	agent := strings.TrimSpace(scope.Agent)
	if agent == "" {
		return transcriptDegrade(ChatReasonNoAgent, "")
	}
	if !agentlog.Supported(agent) {
		// 未知 Agent 绝不猜：agent 字段只在它是受支持取值时才出现，避免前端拿到
		// 闭集合之外的字符串。
		return transcriptDegrade(ChatReasonUnsupportedAgent, "")
	}
	cwd := strings.TrimSpace(scope.CWD)
	if !strings.HasPrefix(cwd, "/") {
		return transcriptDegrade(ChatReasonCWDUnavailable, agent)
	}
	if request.Session == "" {
		return transcriptCandidates(ctx, files, codec, agent, cwd)
	}
	parts, ok := codec.open(request.Session, 3)
	if !ok || parts[0] != agent || !transcriptRelAllowed(agent, cwd, parts[1], parts[2]) {
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	return transcriptMessages(ctx, files, codec, agent, cwd, parts[1], parts[2], request)
}

func transcriptDegrade(reason, agent string) TranscriptPage {
	return TranscriptPage{Supported: false, Reason: reason, Agent: agent, Messages: []agentlog.Record{}}
}

func transcriptDegradeFromError(err error, agent string) TranscriptPage {
	switch {
	case errors.Is(err, errTranscriptTransport):
		return transcriptDegrade(ChatReasonUnsupportedTransport, agent)
	case errors.Is(err, errTranscriptRoot), errors.Is(err, errTranscriptNotFound):
		return transcriptDegrade(ChatReasonLogRootUnavailable, agent)
	case errors.Is(err, errTranscriptDenied):
		return transcriptDegrade(ChatReasonReadDenied, agent)
	default:
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
}

// transcriptCandidates 只列候选，绝不返回消息，也绝不自动认领其中一个。
func transcriptCandidates(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd string) TranscriptPage {
	candidates, foreign, err := collectCandidates(ctx, files, codec, agent, cwd)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	page := TranscriptPage{Supported: true, Agent: agent, Candidates: candidates, Messages: []agentlog.Record{}}
	switch {
	case len(candidates) > 0:
	case foreign:
		// 这个 cwd 下**确实有**会话目录，只是没有本实现支持的产物代际。如实说读不了，
		// 不能含糊成「这里没有会话」。
		page.Reason = ChatReasonUnrecognizedFormat
	default:
		page.Reason = ChatReasonNoSessionCandidates
	}
	return page
}

// transcriptSessionLost 对应契约里「session 失效」：重新给出候选让用户重选，并置 reset。
func transcriptSessionLost(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd string) TranscriptPage {
	page := transcriptCandidates(ctx, files, codec, agent, cwd)
	if !page.Supported {
		return page
	}
	page.Reason = ChatReasonSessionUnavailable
	page.Reset = true
	return page
}

// ── 候选扫描 ──

// collectCandidates 列出当前 cwd 的候选会话。第二个返回值表示「找到了会话目录，但没有
// 本实现支持的产物代际」——调用方据此区分「这里没有会话」与「这里的会话读不了」。
func collectCandidates(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd string) ([]agentlog.Candidate, bool, error) {
	root := transcriptRootFor(agent)
	var (
		entries []transcriptEntry
		err     error
	)
	switch agent {
	case agentlog.AgentClaude:
		// 目录名编码只能**缩小搜索范围**：编码是有损的（`/a-b` 与 `/a/b` 同码），
		// 所以这里只列出与当前 cwd 编码同名的那一个目录，归属仍由记录内容判定。
		entries, err = files.List(ctx, root, agentlog.ClaudeProjectDir(cwd), 1)
		if errors.Is(err, errTranscriptNotFound) {
			return nil, false, nil
		}
	case agentlog.AgentDSH:
		// `projectKey` 同样只能缩小范围（分隔符折叠 + 截断都有损），而且目录下还有一层
		// 会话目录，所以按两层列。归属仍由 header 里 cwd 的精确相等判定。
		project, ok := dshProjectDir(cwd)
		if !ok {
			return nil, false, nil
		}
		entries, err = files.List(ctx, root, project, 2)
		if errors.Is(err, errTranscriptNotFound) {
			return nil, false, nil
		}
	default:
		entries, err = files.List(ctx, root, "", transcriptMaxCodexDepth)
	}
	if err != nil {
		return nil, false, err
	}
	files_ := make([]transcriptEntry, 0, len(entries))
	foreign := false
	for _, entry := range entries {
		if entry.Dir || entry.Link {
			continue
		}
		name := baseName(entry.Rel)
		keep := false
		switch agent {
		case agentlog.AgentClaude:
			keep = agentlog.ClaudeSessionFile(name)
		case agentlog.AgentDSH:
			keep = agentlog.DSHSessionFile(name) || agentlog.DSHCompressedSessionFile(name)
			if !keep && dshForeignGeneration(name) {
				foreign = true
			}
		default:
			keep = agentlog.CodexSessionFile(name)
		}
		if !keep {
			continue
		}
		files_ = append(files_, entry)
	}
	// 新的会话更可能是当前 pane 的，但**不自动认领**：只用来决定先校验哪些。
	sort.SliceStable(files_, func(left, right int) bool {
		return files_[left].Modified.After(files_[right].Modified)
	})
	if len(files_) > transcriptMaxCandidates {
		files_ = files_[:transcriptMaxCandidates]
	}
	if len(files_) == 0 {
		return nil, foreign, nil
	}

	rels := make([]string, 0, len(files_))
	for _, entry := range files_ {
		rels = append(rels, entry.Rel)
	}
	shapes, results, err := transcriptShapes(ctx, files, root, agent, rels)
	if err != nil {
		return nil, false, err
	}
	candidates := make([]agentlog.Candidate, 0, len(files_))
	for position, entry := range files_ {
		if position >= len(results) {
			break
		}
		result := results[position]
		if result.Err != nil || !result.Exists {
			continue
		}
		shape := shapes[position]
		// 归属判定必须落到记录内容：目录名编码证明不了 cwd 归属。
		if shape.CWD != cwd {
			continue
		}
		sessionID := shape.SessionID
		switch agent {
		case agentlog.AgentClaude:
			sessionID = agentlog.SessionStem(baseName(entry.Rel))
		case agentlog.AgentDSH:
			// 会话 id 只认 header；同时要求目录段**逐字**等于该 id 的规范编码 ——
			// 这是「严格 canonical 目录规则」的一半，另一半由归属判定给出。
			if sessionID == "" || shape.DSHVersion != agentlog.DSHFormatVersion {
				foreign = true
				continue
			}
			if encoded, ok := dshEncodeSegment(sessionID); !ok || encoded != baseName(parentDir(entry.Rel)) {
				continue
			}
		default:
			if sessionID == "" {
				sessionID = agentlog.SessionStem(baseName(entry.Rel))
			}
		}
		id, err := codec.seal(agent, sessionID, entry.Rel)
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, agentlog.Candidate{
			ID:        id,
			Agent:     agent,
			SessionID: sessionID,
			UpdatedAt: entry.Modified.UTC().Format(time.RFC3339),
		})
	}
	return candidates, foreign, nil
}

func baseName(rel string) string {
	if index := strings.LastIndexByte(rel, '/'); index >= 0 {
		return rel[index+1:]
	}
	return rel
}

// parentDir 返回相对路径的父目录（没有分隔符时返回空串）。
func parentDir(rel string) string {
	if index := strings.LastIndexByte(rel, '/'); index >= 0 {
		return rel[:index]
	}
	return ""
}

// transcriptShapes 读取并解析若干文件的会话身份。
//
// 起始窗口只有 4 KiB，但日志的一行可以很长（一次粘贴的代码常常超过它），所以「窗口里还
// 没有结论」的文件会按需翻倍扩大窗口，直到 transcriptShapeMaxBytes 为止 —— 仍然是有界读取。
// 返回的 shapes / reads 与 rels 一一对应。
func transcriptShapes(ctx context.Context, files transcriptFS, root transcriptRootKind, agent string, rels []string) ([]agentlog.FileShape, []transcriptReadResult, error) {
	shapes := make([]agentlog.FileShape, len(rels))
	reads := make([]transcriptReadResult, len(rels))
	pending := make([]int, 0, len(rels))
	for index := range rels {
		pending = append(pending, index)
	}
	length := int64(transcriptHeadBytes)
	for len(pending) > 0 {
		specs := make([]transcriptReadSpec, 0, len(pending))
		for _, index := range pending {
			specs = append(specs, transcriptReadSpec{Rel: rels[index], Offset: 0, Length: length})
		}
		results, err := files.Read(ctx, root, specs)
		if err != nil {
			return nil, nil, err
		}
		grown := pending[:0]
		for position, index := range pending {
			if position >= len(results) {
				reads[index] = transcriptReadResult{Err: errTranscriptNotFound}
				continue
			}
			result := results[position]
			reads[index] = result
			if result.Err != nil || !result.Exists {
				continue
			}
			shape, decoded := transcriptShapeFromWindow(agent, rels[index], result.Data)
			shapes[index] = shape
			// decoded=false 表示窗口里还得不出结论（例如首帧还没读完），值得再扩大窗口。
			if (decoded && shapeSettled(agent, shape)) || int64(len(result.Data)) < length || length >= transcriptShapeMaxBytes {
				continue
			}
			grown = append(grown, index)
		}
		pending = grown
		length *= 2
	}
	return shapes, reads, nil
}

// shapeSettled 报告窗口里是否已经拿到足以判定归属的身份。取不到就继续扩大窗口，
// 直到上限；到上限仍然取不到就按「不匹配」处理，绝不猜。
func shapeSettled(agent string, shape agentlog.FileShape) bool {
	if agent == agentlog.AgentDSH {
		// DSH 的归属字段只在本实现支持的代际上给出，所以：
		//   - 已经报出代际（无论是否支持）就是结论，不必再扩大窗口；
		//   - 什么都没解出来说明 header 行还没读全，继续扩大窗口。
		return shape.DSHVersion != 0 || shape.CWD != "" || shape.SessionID != ""
	}
	if shape.CWD == "" {
		return false
	}
	if agent == agentlog.AgentCodex && shape.SessionID == "" {
		return false
	}
	return true
}

// transcriptShapeFromWindow 从读取窗口里解出会话身份。
//
// 明文 provider 直接交给 agentlog 逐行找；DSH 的 zstd 代际要先**只扫首帧**再解压它：
// 列目录时绝不能为了拿一个 header 就把整份会话正文解压出来。第二个返回值表示
// 「这个窗口已经足以给出结论」——false 只表示首帧还没读完，调用方应扩大窗口。
func transcriptShapeFromWindow(agent, rel string, data []byte) (agentlog.FileShape, bool) {
	if agent != agentlog.AgentDSH || !agentlog.DSHCompressedSessionFile(baseName(rel)) {
		return agentlog.ReadFileShape(agent, data), true
	}
	frames, _, err := dshScanFrames(data, 0, 1)
	if err != nil {
		// 帧结构已经坏了：再扩大窗口也只会看到同样的错误。
		return agentlog.FileShape{}, true
	}
	if len(frames) == 0 {
		return agentlog.FileShape{}, false
	}
	decoder, err := newDSHDecoder(transcriptHeadBytes)
	if err != nil {
		return agentlog.FileShape{}, true
	}
	defer decoder.Close()
	plain, err := decoder.decode(data[frames[0].Start:frames[0].End])
	if err != nil {
		return agentlog.FileShape{}, true
	}
	return agentlog.ReadFileShape(agent, plain), true
}

// ── 消息分页 ──

func transcriptMessages(ctx context.Context, files transcriptFS, codec transcriptCodec, agent, cwd, sessionID, rel string, request TranscriptRequest) TranscriptPage {
	// DSH 的 zstd 代际不能按字节切页：压缩文件里的行偏移不是可用的读取起点，只有帧边界是。
	// 明文代际（session.v3.jsonl）没有这个问题，继续走通用引擎。
	if agent == agentlog.AgentDSH && agentlog.DSHCompressedSessionFile(baseName(rel)) {
		return transcriptDSHCompressedMessages(ctx, files, codec, agent, cwd, sessionID, rel, request)
	}
	root := transcriptRootFor(agent)
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
	shape := shapes[0]
	// 空文件（或还没有任何完整行）是正常的「空会话」，不是降级，也不能因此判定归属；
	// 但只要文件里已经有完整记录，cwd 就必须精确相等 —— 这是归属的唯一证明。
	empty := len(head.Data) == 0 || !hasCompleteLine(head.Data)
	if !empty {
		if agent == agentlog.AgentDSH && shape.DSHVersion != agentlog.DSHFormatVersion {
			// 文件在名义上是 v3 产物，header 却不是本实现支持的代际：明确说不支持，
			// 不能按当前代际硬解。
			return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
		}
		if agent == agentlog.AgentDSH && !dshHeaderAdmitted(agent, sessionID, head.Data, 0) {
			// header 行本身不被准入（例如 isSeeded 的会话）：明确失败，不发布任何记录。
			// 分页通常只读尾部窗口，header 行不在其中，所以这个检查不能省。
			return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
		}
		if shape.CWD != cwd {
			return transcriptSessionLost(ctx, files, codec, agent, cwd)
		}
		if (agent == agentlog.AgentCodex || agent == agentlog.AgentDSH) && shape.SessionID != "" && shape.SessionID != sessionID {
			// 请求里的 session 与文件里解析出的会话 id 不再对应同一个文件。
			return transcriptSessionLost(ctx, files, codec, agent, cwd)
		}
	}

	size := head.Size
	// 文件身份来自 stat（设备 + inode），不是内容前缀哈希：会话正常追加时它必须不变，
	// 否则每次轮询都会被误判成轮转。
	identity := head.Identity
	token := []string{sessionID, rel, identity}

	start := size - transcriptInitialWindowBytes
	if start < 0 {
		start = 0
	}
	reset := false
	// anchor 是 before 锚点的绝对偏移（-1 表示没有）。它和「窗口起点 start」必须分开：
	// 锚点可以贴到文件开头，窗口起点却要被窗口长度往前推。
	anchor := int64(-1)
	switch {
	case request.Before != "":
		// 反向：取更早的一页。与 cursor 同时出现时以 before 为准。
		offset, ok := transcriptCursorOffset(codec, request.Before, token)
		if !ok || offset <= 0 || offset > size {
			reset = true
			break
		}
		anchor = offset
	case request.Cursor != "":
		// 正向：带 cursor 取新增记录。这是轮询的唯一方式，不做全量重读。
		offset, ok := transcriptCursorOffset(codec, request.Cursor, token)
		if !ok || offset < 0 || offset > size {
			// 游标无效、或文件被截断/轮转：从尾部窗口重新读取，客户端整体清空重建。
			reset = true
			break
		}
		start = offset
	}

	// before 锚点只读它**之前**的字节：越过锚点会把已经给过的记录再解一遍，
	// 而反向切页保留的又是窗口尾部 —— 两者叠加就会原地打转。
	anchored := anchor >= 0
	// 切页方向：正向轮询保留窗口开头（与 cursor 锚点连续），其余（初始页 / 反向页 /
	// 重来页）保留窗口结尾，让客户端先拿到最新的记录。
	backward := request.Before != "" || request.Cursor == "" || reset

	// 读取窗口。反向锚点页从锚点往回读满一页（含对齐余量），读到的字节**不超过锚点**；
	// 其余页从对齐余量的位置往后读一页。
	var base, length, splitAt int64
	if anchored {
		base = anchor - transcriptInitialWindowBytes - transcriptAlignProbeBytes
		if base < 0 {
			base = 0
		}
		length = anchor - base
		splitAt = anchor
	} else {
		base = start - transcriptAlignProbeBytes
		if base < 0 {
			base = 0
		}
		length = size - base
		if cap := start - base + transcriptInitialWindowBytes; length > cap {
			length = cap
		}
		splitAt = start
	}
	body, err := transcriptReadOne(ctx, files, root, rel, base, length)
	if err != nil {
		return transcriptDegradeFromError(err, agent)
	}
	if body.Err != nil {
		if errors.Is(body.Err, errTranscriptDenied) {
			return transcriptDegrade(ChatReasonReadDenied, agent)
		}
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}
	if !body.Exists || body.Size < size {
		// 两次读取之间文件被替换或截断。
		return transcriptSessionLost(ctx, files, codec, agent, cwd)
	}

	from, to := transcriptSlice(body.Data, int(splitAt-base), base, anchored)
	trueStart := trueStartFor(base, from)
	decoded := agentlog.RangeResult{Entries: []agentlog.RangeEntry{}, Consumed: trueStart}
	if from >= 0 && to > from {
		decoded = agentlog.DecodeRange(
			agentlog.LineContext{Agent: agent, SessionID: sessionID, ResponseItemPath: shape.ResponseItemPath},
			body.Data[from:to],
			trueStart,
		)
	}
	// Failure 是「按格式必须理解、却无法安全理解」的内容：从同一偏移重读只会得到同一个
	// Failure，继续分页只会把一份不完整的对话发布成完整的样子。所以它是终止条件。
	if decoded.Failure != nil {
		return transcriptDegrade(ChatReasonUnrecognizedFormat, agent)
	}

	// 切页：只切在**完整记录**边界上，绝不为了凑上限截断成半条记录。
	//
	// 方向决定从哪里切，这一点很关键：
	//   - 正向页从锚点向后读，保留窗口**开头**的一段，next_cursor 指向被切掉的第一条之前
	//     —— 停在完整记录之后，客户端继续往后拿。
	//   - 反向页从锚点向前读，必须保留窗口**结尾**的一段（贴着 before 锚点），否则被切掉的
	//     那几条会落在「比本页更新、比上一页更旧」的缝里，反向翻页永远拿不到它们。
	records := make([]agentlog.RangeEntry, 0, len(decoded.Entries))
	sizes := make([]int, 0, len(decoded.Entries))
	for _, entry := range decoded.Entries {
		if entry.Record == nil {
			continue
		}
		encoded, err := json.Marshal(*entry.Record)
		if err != nil {
			continue
		}
		records = append(records, entry)
		sizes = append(sizes, len(encoded))
	}
	first, last := 0, 0
	remaining := transcriptMaxResponseBytes
	count := 0
	if backward {
		// 从尾部贴着锚点向前累计，保留与 before 锚点连续的一段。
		last = len(records)
		for last > 0 {
			if count >= transcriptPageRecords || (sizes[last-1] > remaining && count > 0) {
				break
			}
			remaining -= sizes[last-1]
			count++
			last--
		}
		first = last
		last = len(records)
	} else {
		for last < len(records) {
			if count >= transcriptPageRecords || (sizes[last] > remaining && count > 0) {
				break
			}
			remaining -= sizes[last]
			count++
			last++
		}
	}

	pageStart := trueStart
	page := TranscriptPage{
		Supported: true,
		Agent:     agent,
		SessionID: sessionID,
		Messages:  agentlog.UpsertEntries(records[first:last]),
		Binding:   ChatBindingSelected,
		Reset:     reset,
		Skipped:   decoded.Skipped,
	}
	consumed := trueStart
	if last > first {
		pageStart = records[first].Start
		consumed = records[last-1].End
	}
	if backward {
		// 下面还有更早的记录：被切掉的、或窗口之前还有内容。
		page.HasMore = first > 0 || trueStart > 0
	} else {
		// 上面还有更新的记录：被切掉的、或这一页之后还有字节没读。
		page.HasMore = last < len(records) || start+transcriptInitialWindowBytes < size
	}

	next, err := codec.seal(sessionID, rel, identity, strconv.FormatInt(consumed, 10))
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, agent)
	}
	page.NextCursor = next
	// 「还有更早」只在按尾部/向前读取的页面上才有意义：初始页（无游标）、`before` 反向页、
	// `reset` 重来页。正向游标页只描述游标之后的新增记录，它解出的第一条也晚于客户端**已经
	// 持有**的最旧记录；把这个偏移当 before 锚点会让「加载更早」原地打转 —— 客户端拿回来的
	// 是它已经有的一整页（实测：260 条记录的文件点一次「加载更早」条数不变）。所以正向游标页
	// 一律不产出 previous_cursor，客户端继续沿用初始页给出的锚点。
	//
	// 反向页一条记录都没解出来时同样不给 previous：那表示已经读到文件开头，客户端必须据此
	// 收起「加载更早」（客户端把「反向请求 + 没有 previous_cursor」当作已经到底）。
	offeredPrevious := backward && last > first
	if pageStart > 0 && offeredPrevious {
		previous, err := codec.seal(sessionID, rel, identity, strconv.FormatInt(pageStart, 10))
		if err != nil {
			return transcriptDegrade(ChatReasonInternalError, agent)
		}
		page.PrevCursor = previous
	}
	return page
}

// transcriptCursorOffset 解开游标并校验它绑定的正是这个会话与这个文件身份。
func transcriptCursorOffset(codec transcriptCodec, token string, expect []string) (int64, bool) {
	parts, ok := codec.open(token, 4)
	if !ok {
		return 0, false
	}
	for index, value := range expect {
		if parts[index] != value {
			return 0, false
		}
	}
	offset, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, false
	}
	return offset, true
}

// transcriptSlice 求出这段字节里要解码的区间 [from, to)。
//
// anchored 表示这一段是以 before 为界的反向读取：
//   - 反向：只取锚点**之前**的完整行（锚点那一行属于更新的一页），所以 to 停在锚点处；
//     锚点若因外部改动落在行中间，连那一行也不完整，一并排除。
//   - 正向：游标正常情况下一定落在行边界上（服务端只推进到完整行之后）。若落在行中间，
//     回退到前一个换行符之后**整行重读** —— 由此产生的重复投递由「按 id 原地更新」安全吸收，
//     这正是契约要求的唯一安全回退方式。
//
// 返回 -1 表示这段字节里找不到行边界，调用方按「没有可解内容」处理。
func transcriptSlice(data []byte, split int, base int64, anchored bool) (int, int) {
	if split < 0 {
		return -1, -1
	}
	if split > len(data) {
		split = len(data)
	}
	if anchored {
		to := split
		if to > 0 && data[to-1] != '\n' {
			// 锚点落在行中间：这一行不完整，留给正向页。
			if position := bytes.LastIndexByte(data[:to], '\n'); position >= 0 {
				to = position + 1
			} else {
				to = 0
			}
		}
		from := 0
		if base > 0 {
			from = -1
			if position := bytes.IndexByte(data[:to], '\n'); position >= 0 {
				from = position + 1
			}
		}
		if from >= to {
			return from, from
		}
		return from, to
	}
	if split <= 0 {
		if last := bytes.LastIndexByte(data, '\n'); last >= 0 {
			return 0, last + 1
		}
		return 0, 0
	}
	from := -1
	if position := bytes.LastIndexByte(data[:split], '\n'); position >= 0 {
		from = position + 1
	} else if base > 0 {
		// 游标前 4 KiB 里都没有换行：这一行超长，从它**之后**的第一行开始解。
		if position := bytes.IndexByte(data[split:], '\n'); position >= 0 {
			from = split + position + 1
		}
	} else {
		from = 0
	}
	if from < 0 {
		return -1, -1
	}
	to := from
	if position := bytes.LastIndexByte(data, '\n'); position >= 0 {
		to = position + 1
	}
	if to < from {
		to = from
	}
	return from, to
}

// trueStartFor 把区间起点换算成绝对偏移；区间无效时退回窗口起点。
func trueStartFor(base int64, from int) int64 {
	if from < 0 {
		return base
	}
	return base + int64(from)
}

func transcriptReadOne(ctx context.Context, files transcriptFS, root transcriptRootKind, rel string, offset, length int64) (transcriptReadResult, error) {
	results, err := files.Read(ctx, root, []transcriptReadSpec{{Rel: rel, Offset: offset, Length: length}})
	if err != nil {
		return transcriptReadResult{}, err
	}
	if len(results) != 1 {
		return transcriptReadResult{}, fmt.Errorf("structured chat reader returned %d results for one request", len(results))
	}
	return results[0], nil
}

// hasCompleteLine 报告数据里是否至少有一行以换行结尾。
func hasCompleteLine(data []byte) bool { return bytes.IndexByte(data, '\n') >= 0 }
