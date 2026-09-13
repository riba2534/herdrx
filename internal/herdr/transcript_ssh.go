package herdr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// transcriptRemoteMaxBytes 是远端一次回答的字节上限。远端输出**必须**有界：
// `ssh.Session.Output` 会无限缓冲，这里用 LimitReader 卡住，超限按可重试错误处理，
// 绝不当成「能力缺失」缓存下来。
const transcriptRemoteMaxBytes = 2 << 20

// TranscriptDiagnostics 让上层能把「为什么这个接入方式不支持」写进审计与日志。
// 响应字段是冻结的，没有承载诊断文本的位置，所以诊断走这条侧信道，不污染契约。
type TranscriptDiagnostics interface {
	TranscriptUnavailableReason() string
}

// Transcript 实现 `TranscriptEndpoint`。
//
// 本机之外的三种接入共用同一个 `*SSHEndpoint`：原生 SSH 与 Tailcat 走 `cryptoSSHClient`，
// 系统 OpenSSH 走 ControlMaster 上的 exec。只缓存已知 transport 的能力限制；
// 超时、空输出、命令失败均可重试，不能污染同一主机其他 pane 的读取能力。
func (e *SSHEndpoint) Transcript(ctx context.Context, scope TranscriptScope, request TranscriptRequest) (TranscriptPage, error) {
	codec, err := transcriptTokens()
	if err != nil {
		return transcriptDegrade(ChatReasonInternalError, ""), nil
	}
	if e.transcriptUnavailable() {
		return transcriptDegrade(ChatReasonUnsupportedTransport, ""), nil
	}
	files := &sshTranscriptFS{endpoint: e}
	page := readTranscript(ctx, files, codec, scope, request)
	if files.transientErr != nil {
		return TranscriptPage{}, files.transientErr
	}
	return page, nil
}

// TranscriptUnavailableReason 返回能力缺失的原因（供审计与日志），未缺失时返回空串。
func (e *SSHEndpoint) TranscriptUnavailableReason() string {
	e.transcriptMu.Lock()
	defer e.transcriptMu.Unlock()
	return e.transcriptReason
}

func (e *SSHEndpoint) transcriptUnavailable() bool {
	e.transcriptMu.Lock()
	defer e.transcriptMu.Unlock()
	return e.transcriptUnsupported
}

func (e *SSHEndpoint) markTranscriptUnsupported(reason string) {
	e.transcriptMu.Lock()
	defer e.transcriptMu.Unlock()
	// 第一次的结论最有信息量，后续失败不再覆盖。
	if !e.transcriptUnsupported {
		e.transcriptUnsupported = true
		e.transcriptReason = strings.TrimSpace(reason)
	}
}

// sshTranscriptFS 把受限文件访问映射成一次远端受限读取器调用。
//
// 远端缺 python3 时返回可重试的执行错误，不退回 `test -L` 后 `cat`
// 这类竞态读法；安装工具后可直接重试，无需重建共享连接。
type sshTranscriptFS struct {
	endpoint     *SSHEndpoint
	transientErr error
}

func (f *sshTranscriptFS) rememberReadError(err error) {
	if err != nil && !errors.Is(err, errTranscriptTransport) && !errors.Is(err, errTranscriptRoot) && !errors.Is(err, errTranscriptNotFound) && !errors.Is(err, errTranscriptDenied) {
		f.transientErr = err
	}
}

func (f *sshTranscriptFS) List(ctx context.Context, root transcriptRootKind, rel string, depth int) ([]transcriptEntry, error) {
	response, err := f.endpoint.execTranscriptReader(ctx, transcriptRemoteRequest{
		Op:    "list",
		Agent: string(root),
		Rel:   rel,
		Depth: depth,
	})
	if err != nil {
		f.rememberReadError(err)
		return nil, err
	}
	entries := make([]transcriptEntry, 0, len(response.Entries))
	for _, entry := range response.Entries {
		entries = append(entries, transcriptEntry{
			Rel:      entry.Rel,
			Dir:      entry.Dir,
			Link:     entry.Link,
			Size:     entry.Size,
			Modified: time.Unix(entry.Modified, 0),
		})
	}
	return entries, nil
}

func (f *sshTranscriptFS) Read(ctx context.Context, root transcriptRootKind, specs []transcriptReadSpec) ([]transcriptReadResult, error) {
	request := transcriptRemoteRequest{Op: "read", Agent: string(root)}
	request.Reads = make([]transcriptRemoteRead, 0, len(specs))
	for _, spec := range specs {
		request.Reads = append(request.Reads, transcriptRemoteRead{Rel: spec.Rel, Offset: spec.Offset, Length: spec.Length})
	}
	response, err := f.endpoint.execTranscriptReader(ctx, request)
	if err != nil {
		f.rememberReadError(err)
		return nil, err
	}
	results := make([]transcriptReadResult, 0, len(specs))
	for index := range specs {
		result := transcriptReadResult{}
		if index < len(response.Reads) {
			remote := response.Reads[index]
			result.Exists = remote.Exists
			result.Size = remote.Size
			result.Identity = remote.Identity
			switch remote.Error {
			case "":
				result.Data, _ = base64.StdEncoding.DecodeString(remote.Data)
			case "not_found":
				result.Err = errTranscriptNotFound
			default:
				result.Err = errTranscriptDenied
			}
		}
		results = append(results, result)
	}
	return results, nil
}

// transcriptRemoteRequest / transcriptRemoteResponse 是远端读取器的线协议。
// 请求经 stdin 以 JSON 传递，**不拼进命令行**：远端命令字符串里只有服务端自己的常量，
// 任何用户可控的字节都不会成为 shell 语法。
type transcriptRemoteRequest struct {
	Op    string                 `json:"op"`
	Agent string                 `json:"agent,omitempty"`
	Rel   string                 `json:"rel,omitempty"`
	Depth int                    `json:"depth,omitempty"`
	Reads []transcriptRemoteRead `json:"reads,omitempty"`
}

type transcriptRemoteRead struct {
	Rel    string `json:"rel"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type transcriptRemoteEntry struct {
	Rel      string `json:"rel"`
	Dir      bool   `json:"dir"`
	Link     bool   `json:"link"`
	Size     int64  `json:"size"`
	Modified int64  `json:"mtime"`
}

type transcriptRemoteResult struct {
	Exists bool   `json:"exists"`
	Size   int64  `json:"size"`
	Data   string `json:"data"`
	Error  string `json:"error"`
	// Identity 是远端文件的设备 + inode，语义与本地 fileIdentity 一致。
	Identity string `json:"identity"`
}

type transcriptRemoteResponse struct {
	OK      bool                     `json:"ok"`
	Error   string                   `json:"error"`
	Entries []transcriptRemoteEntry  `json:"entries"`
	Reads   []transcriptRemoteResult `json:"reads"`
}

func (e *SSHEndpoint) execTranscriptReader(ctx context.Context, request transcriptRemoteRequest) (transcriptRemoteResponse, error) {
	if e.host.Transport == "tailcat" {
		// Tailcat 的 access daemon 只接受严格白名单的命令语法（herdr 与少量内部子命令），
		// 没有任意 exec，也不允许把自定义读取器投送过去。所以这里给明确的不支持，
		// 而不是把 `python3 -c ...` 发过去等它拒绝。
		e.markTranscriptUnsupported("tailcat agent command grammar has no arbitrary exec")
		return transcriptRemoteResponse{}, errTranscriptTransport
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return transcriptRemoteResponse{}, errTranscriptDenied
	}
	session, err := e.newSession(ctx)
	if err != nil {
		return transcriptRemoteResponse{}, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()

	stdin, err := session.StdinPipe()
	if err != nil {
		return transcriptRemoteResponse{}, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return transcriptRemoteResponse{}, err
	}
	var stderr limitedSSHLog
	session.setStderr(&stderr)
	if err := session.Start(transcriptReaderCommand); err != nil {
		return transcriptRemoteResponse{}, err
	}
	go func() {
		_, _ = stdin.Write(payload)
		_ = stdin.Close()
	}()
	data, readErr := io.ReadAll(io.LimitReader(stdout, transcriptRemoteMaxBytes+1))
	if int64(len(data)) > transcriptRemoteMaxBytes {
		_ = session.Close()
		return transcriptRemoteResponse{}, fmt.Errorf("会话记录响应超过大小限制，请重试")
	}
	if readErr != nil {
		_ = session.Close()
	}
	waitErr := session.Wait()
	if ctx.Err() != nil {
		return transcriptRemoteResponse{}, fmt.Errorf("会话记录读取已取消或超时，请重试: %w", ctx.Err())
	}
	if readErr != nil {
		return transcriptRemoteResponse{}, fmt.Errorf("会话记录读取中断，请重试")
	}
	if waitErr != nil {
		return transcriptRemoteResponse{}, fmt.Errorf("远端会话读取器执行失败，请确认 python3 可用后重试")
	}
	response := transcriptRemoteResponse{}
	if err := json.Unmarshal(data, &response); err != nil {
		// 空输出、截断或非 JSON 都不能证明主机永久缺失能力。
		return transcriptRemoteResponse{}, fmt.Errorf("远端会话读取器返回空或无效响应，请重试")
	}
	if !response.OK {
		switch response.Error {
		case "root_unavailable", "home_unavailable":
			return transcriptRemoteResponse{}, errTranscriptRoot
		case "not_found":
			return transcriptRemoteResponse{}, errTranscriptNotFound
		case "denied", "bad_request", "unknown_agent", "unknown_op":
			return transcriptRemoteResponse{}, errTranscriptDenied
		}
		return transcriptRemoteResponse{}, fmt.Errorf("远端会话读取器暂时失败，请重试")
	}
	return response, nil
}

// transcriptReaderCommand 在远端执行受限读取器。
//
// 脚本用 base64 常量内联在 `-c` 里：base64 的字母表不含单引号或其它 shell 元字符，
// 所以引号是平凡安全的，也不需要在远端落任何临时文件（本能力只读，不写远端磁盘）。
// 请求体走 stdin，从不进入命令行。
var transcriptReaderCommand = "python3 -c 'import base64,sys;exec(base64.b64decode(\"" +
	base64.StdEncoding.EncodeToString([]byte(transcriptReaderSource)) + "\").decode())'"
