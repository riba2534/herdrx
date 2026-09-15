package agentlog

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// DSH（DeepSeek Harness）会话日志的纯解码。
//
// 这里的形状是**对照已安装源码读出来的**，不是猜的：物理行格式与 v3 codec 位于
// `@deepseek-ai/dsh@0.1.5-rc.2` 的 `dsh-session-persistence-jsonl`（文件命名、帧、
// 行扫描）、`dsh-session-format-v1-to-v2`（v2/v3 共用的物理编解码）、
// `dsh-session-format-v2-to-v3`（v3 的准入收紧）与 `dsh-session`（事件词汇表、
// surface 层）。v3 与 v2 的差别只有 generation 号、`system/message` 事件和 PTC 词汇，
// 物理行形状是同一套，所以 v3 解码直接复用了 v2 的行规则。
//
// 与其它两个 provider 一样，本文件只做「一行 JSONL → 一条记录」的纯函数转换：
// 不碰文件系统、不解压、不认识 Herdr。zstd 输入由传输层有界解压成明文字节后喂进来，
// 本包不参与帧边界、也不做任何压缩猜测。
//
// 读取源的文件布局（由传输层负责枚举与白名单）
//
//	<root>/<projectKey(cwd)>/<encodeSegment(sessionId)>/session.v3.jsonl[.zstd]
//
// 首行恒定是 header，其余行是事件；`projectKey` 的目录编码是**有损**的
// （分隔符统一变成 '-' 且会截断），因此目录名只用来缩小搜索范围，归属只能由 header
// 里真实 `cwd` 的精确相等来判定 —— 与 Claude 的项目目录同一条规矩。
//
// # 支持范围（必须如实告知用户，不要当成已实现）
//
// 产出的是**追加来源的人类对话记录**，也就是上游
// `isAppendSurfaceEvent` 明确指定的那份「人类 transcript 的durable 来源材料」：
// 只取 `surfaceOp:"append"` 的 surface 事件，落在**模型可见 surface** 上的改写
// （`surfaceOp` 的 replace 形态）一律不落地。
//
// 这条规矩是上游自己写下来的：模型可见 surface 会遮蔽被替换的区间，所以它是
// **错误**的人类 transcript 来源 —— 一次落地的替换会擦掉用户已经看过的对话。
// 两个真实场景因此在我们这里的行为是确定的：
//
//   - 压缩（`dsh-compaction-basic` 写一条 replace 形态的 `user/message` 检查点）：
//     用户仍然看到自己真实说过的话，看不到模型侧的检查点与摘要。
//   - 工具结果裁剪（`dsh-compaction-tool-result-pruner` 写 replace 形态的
//     `tool/result`）：用户看到未裁剪的原始输出，不会看到同一 callId 的第二份副本。
//
// 被跳过的 replace 记录**不是静默丢弃**：RangeResult.Replacements 会把条数报出来，
// 调用方可以如实说明「这份记录里还有 N 条模型侧改写没有应用」。
//
// 同样按设计隐藏、且不可能篡改或复活用户/助手内容的还有：
// `compaction/prune` 与 `compaction/summary`（模型侧遮蔽账目）、
// `feedback/message-put` 与 `feedback/message-delete`（只是消息评分，不携带会话正文）。
//
// # 明确不支持：seeded 会话（isSeeded:true）
//
// seeded 会话的日志里有一段**从父会话继承来的前缀**（上游要求它以一条
// `session/end-seed` 且 `data.inherited === true` 收尾，`inheritedEventCount` 就是那条的 seq）。
// 那段前缀是继承来的上下文，**不是这个会话里用户当前说的话**。要正确读出「本会话自己的
// 对话」必须先把整份文件读完、拿到 inherited 边界，再按边界切分 —— 那是文件级状态，
// 逐帧、分页的读取模型下拿不到。
//
// 所以这里的选择是**直接拒绝**：`isSeeded:true` 的 header 一律报
// FormatUnsupportedSeeded，绝不把继承来的上下文当成用户当前的提问发布出去。
// 这比「解一半」安全：宁可让调用方如实说「这个会话暂不支持」，也不能伪造一段对话。
//
// **未强制的一半（已知差距，不要声称这条不变量成立）**：上游还要求
// `header.isSeeded` 与是否存在 inherited 的 `session/end-seed` 标记互相印证
// （seeded 必须有，unseeded 必须没有）。本包只看得到单行，判定它要读完整个文件；
// 「unseeded 的 header 却带 inherited 标记」属于文件损坏，只能由读完所有帧的一方发现，
// 而这与有界读取（8 MiB 压缩输入 / 32 MiB 解压输出）的设计相冲突。故不做，如实记为差距。
const (
	// DSHFormatVersion 是本实现唯一支持的 DSH 会话格式代际。
	//
	// 旧代际（v0/v1/v2）在 DSH 里要相邻迁移链才能读成当前逻辑事件，本包只有 v3 的行解码，
	// 所以刻意不开放：遇到别的代际要清楚地说「不支持」，而不是按 v3 硬解出错误的对话。
	DSHFormatVersion = 3
)

// DSH 规范文件名。v3 的原始日志名是 `session.v3.jsonl`，默认物理编码 zstd 再加 `.zstd`。
const (
	dshSessionBaseName = "session"
	dshPlainSuffix     = ".jsonl"
	dshZstdSuffix      = ".zstd"
)

// dshLogVersion 解析规范代际文件名，返回它的会话格式版本。
//
// 只认规范名：临时名、大写、前导零、`.v0`、以及额外后缀都不算已提交的代际，
// 与上游 `parseSessionFormatLogFilename` 同规则。明文与 `.zstd` 两种物理编码都接受。
func dshLogVersion(name string) (int, bool) {
	trimmed := strings.TrimSuffix(name, dshZstdSuffix)
	if trimmed == dshSessionBaseName+dshPlainSuffix {
		return 0, true
	}
	prefix := dshSessionBaseName + ".v"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, dshPlainSuffix) {
		return 0, false
	}
	return canonicalVersionDigits(trimmed[len(prefix) : len(trimmed)-len(dshPlainSuffix)])
}

// canonicalVersionDigits 校验 `vN` 里的 N：纯数字、无前导零、且不是 0
// （v0 用 `session.jsonl`，不写 `.v0`）。
func canonicalVersionDigits(digits string) (int, bool) {
	if digits == "" || digits[0] == '0' {
		return 0, false
	}
	for index := 0; index < len(digits); index++ {
		if digits[index] < '0' || digits[index] > '9' {
			return 0, false
		}
	}
	version, err := strconv.Atoi(digits)
	if err != nil || version <= 0 {
		return 0, false
	}
	return version, true
}

// DSHSessionFile 报告文件名是否是本期支持的明文 DSH 会话日志名。
func DSHSessionFile(name string) bool {
	version, ok := dshLogVersion(name)
	return ok && version == DSHFormatVersion && !strings.HasSuffix(name, dshZstdSuffix)
}

// DSHCompressedSessionFile 报告文件名是否是本期支持的 zstd DSH 会话日志名。
//
// 认名字不等于本包能解压：解压由传输层有界完成，本包只解释解压后的明文行。
func DSHCompressedSessionFile(name string) bool {
	version, ok := dshLogVersion(name)
	return ok && version == DSHFormatVersion && strings.HasSuffix(name, dshZstdSuffix)
}

// ── header ──

// dshHeader 是校验过的 DSH 物理 header。
type dshHeader struct {
	Version int
	ID      string
	CWD     string
	Seeded  bool
}

// DSH header 的精确键集合（v1/v2/v3 三代相同）。多出来的键是硬错误：
// 上游 `decodePhysicalHeader` 用 exactKeys 拒绝未审计字段，因为无法判断它是否携带坐标。
var (
	dshHeaderRequired = []string{"type", "version", "id", "createdAt", "isSeeded", "delegationDepth"}
	dshHeaderOptional = []string{"cwd", "parentSession", "origin", "agentPreset"}
)

// parseDSHHeader 严格解析一行 header。
//
// 第二个返回值是失败原因；为空表示解析成功。拒绝条件对照上游：
// `type` 必须是 `session`，`version`/`createdAt`/`delegationDepth` 必须是非负安全整数，
// `isSeeded` 必须是布尔，`cwd` 必须是绝对路径，`origin` 只能是 `subagent`。
func parseDSHHeader(line string) (dshHeader, string) {
	record := parseJSONObject(line)
	if record == nil {
		return dshHeader{}, "header is not a JSON object"
	}
	if detail := exactKeys(record, dshHeaderRequired, dshHeaderOptional); detail != "" {
		return dshHeader{}, "header " + detail
	}
	if extractString(record["type"]) != "session" {
		return dshHeader{}, "header type is not session"
	}
	version, ok := safeInt(record["version"])
	if !ok || version <= 0 {
		return dshHeader{}, "header version is not a positive integer"
	}
	if _, ok := safeInt(record["createdAt"]); !ok {
		return dshHeader{}, "header createdAt is not a safe integer"
	}
	if _, ok := safeInt(record["delegationDepth"]); !ok {
		return dshHeader{}, "header delegationDepth is not a safe integer"
	}
	if _, ok := record["isSeeded"].(bool); !ok {
		return dshHeader{}, "header isSeeded is not a boolean"
	}
	id := extractString(record["id"])
	if id == "" {
		// 上游只要求是字符串；这里更严：磁盘上的会话目录是用 encodeSegment(id) 编的，
		// 空 id 根本写不出来，能读到空 id 说明文件已经不可信。
		return dshHeader{}, "header id is empty"
	}
	var cwd string
	if raw, present := record["cwd"]; present {
		cwd = rawString(raw)
		if !strings.HasPrefix(cwd, "/") {
			return dshHeader{}, "header cwd is not an absolute path"
		}
	}
	for _, key := range []string{"parentSession", "agentPreset"} {
		if raw, present := record[key]; present {
			if _, ok := raw.(string); !ok {
				return dshHeader{}, "header " + key + " is not a string"
			}
		}
	}
	if raw, present := record["origin"]; present && rawString(raw) != "subagent" {
		return dshHeader{}, "header origin is neither absent nor subagent"
	}
	return dshHeader{Version: int(version), ID: id, CWD: cwd, Seeded: isTrue(record["isSeeded"])}, ""
}

// dshHeaderProbe 只读 header 的身份与代际，不做完整准入。
//
// ReadFileShape 用它报告「这是哪个代际的 DSH 日志」：遇到本实现没实现的代际时要能说出
// 版本号，而不是含糊地当成「不是 DSH 日志」。归属字段（cwd/id）只在严格解析通过时才采用。
func dshHeaderProbe(line string) (version int, ok bool) {
	record := parseJSONObject(line)
	if record == nil || extractString(record["type"]) != "session" {
		return 0, false
	}
	parsed, ok := safeInt(record["version"])
	if !ok || parsed <= 0 {
		return 0, false
	}
	return int(parsed), true
}

// ── event 信封 ──

// dshSurfaceTypes 是能进入模型可见 surface 的事件类型闭集合，与上游 `SURFACE_EVENT_TYPES` 一致。
var dshSurfaceTypes = map[string]bool{
	"system/message":    true,
	"user/message":      true,
	"assistant/message": true,
	"tool/result":       true,
}

// 记录 disposition：产出对话记录，还是认识但按设计隐藏。
const (
	dshProduceRecord = iota
	dshHiddenRecord
)

// dshEventDisposition 是事件类型闭集合，对照安装源码 `dsh-session` 的
// KNOWN_SESSION_EVENT_TYPES 加上 v3 的 PTC 词汇。
//
// 这张表就是「什么算认识」的定义：表外的类型只有在 `ignorable:true` 时才允许被跳过
// （那是上游为外部插件事件留的兼容位）；表外且不可忽略的事件说明这份日志是更新的
// harness 写的，**必须报格式失败**，静默跳过它会重建出一段错误的对话。
var dshEventDisposition = map[string]int{
	// 产出记录。
	"user/message":      dshProduceRecord,
	"assistant/message": dshProduceRecord,
	"tool/call":         dshProduceRecord,
	"tool/result":       dshProduceRecord,
	"system/message":    dshProduceRecord,
	"turn/end":          dshProduceRecord,

	// 认识但不产出记录：生命周期、计量、请求信封、压缩账目与其它内部记录。
	"agent-preset/selected":                  dshHiddenRecord,
	"agent/inbox/spliced":                    dshHiddenRecord,
	"approval/asked":                         dshHiddenRecord,
	"approval/decided":                       dshHiddenRecord,
	"approval/policy":                        dshHiddenRecord,
	"assistant/attempt":                      dshHiddenRecord,
	"command/done":                           dshHiddenRecord,
	"command/run":                            dshHiddenRecord,
	"compaction/end":                         dshHiddenRecord,
	"compaction/prune":                       dshHiddenRecord,
	"compaction/start":                       dshHiddenRecord,
	"compaction/summary":                     dshHiddenRecord,
	"deliverables/presented":                 dshHiddenRecord,
	"feedback/message-delete":                dshHiddenRecord,
	"feedback/message-put":                   dshHiddenRecord,
	"feedback/record":                        dshHiddenRecord,
	"goal/change":                            dshHiddenRecord,
	"hook/invoked":                           dshHiddenRecord,
	"hook/result":                            dshHiddenRecord,
	"llm/retry":                              dshHiddenRecord,
	"llm/retry-started":                      dshHiddenRecord,
	"model/selection":                        dshHiddenRecord,
	"permission/preset":                      dshHiddenRecord,
	"plan/mode":                              dshHiddenRecord,
	"request/context":                        dshHiddenRecord,
	"request/header":                         dshHiddenRecord,
	"sandbox/mode":                           dshHiddenRecord,
	"schedule/change":                        dshHiddenRecord,
	"session-log-deepseek/delivery-accepted": dshHiddenRecord,
	"session/end-seed":                       dshHiddenRecord,
	"session/title":                          dshHiddenRecord,
	"session/title-llm-request":              dshHiddenRecord,
	"step/end":                               dshHiddenRecord,
	"step/start":                             dshHiddenRecord,
	"subagent/catalog":                       dshHiddenRecord,
	"subagent/descriptor":                    dshHiddenRecord,
	"subagent/model-selection-policy":        dshHiddenRecord,
	"team/member":                            dshHiddenRecord,
	"team/message/delivered":                 dshHiddenRecord,
	"team/message/queued":                    dshHiddenRecord,
	"team/task":                              dshHiddenRecord,
	"todo/write":                             dshHiddenRecord,
	"tool-workflow/agent-end":                dshHiddenRecord,
	"tool-workflow/agent-start":              dshHiddenRecord,
	"tool-workflow/run-end":                  dshHiddenRecord,
	"tool-workflow/run-start":                dshHiddenRecord,
	"tool/code-dispatch":                     dshHiddenRecord,
	"tool/code-dispatch-start":               dshHiddenRecord,
	"tool/ptc-dispatch":                      dshHiddenRecord,
	"tool/ptc-dispatch-start":                dshHiddenRecord,
	"turn/start":                             dshHiddenRecord,
	"web/deepseek-search-llm-request":        dshHiddenRecord,
}

// dshIgnorableRequired 是 v3 里**必须**带 `ignorable:true` 才准入的类型。
//
// 上游 `assertV3EventAdmission` 对 PTC 派发的两个类型就是这么收紧的：它们携带子调用坐标，
// 不能安全迁移，所以只允许以「可忽略」的形式出现。不带标记时我们同样报格式失败。
var dshIgnorableRequired = map[string]bool{
	"tool/code-dispatch":       true,
	"tool/code-dispatch-start": true,
}

// 信封允许的键集合（上游 EVENT_KEYS）。
var (
	dshEventRequired = []string{"type", "seq", "time", "data"}
	dshEventOptional = []string{"ignorable", "sourceEventSeqs", "surfaceOp"}
)

// dshEvent 是校验过的 DSH 物理事件行。
type dshEvent struct {
	Type      string
	Seq       int64
	Time      float64
	Ignorable bool
	SurfaceOp string
	Data      map[string]any
}

// parseDSHEvent 校验事件信封。第二个返回值是失败原因；为空表示通过。
func parseDSHEvent(record map[string]any) (dshEvent, string) {
	surface := dshSurfaceTypes[extractString(record["type"])]
	optional := []string{"ignorable"}
	if surface {
		// surface 事件才允许带 surfaceOp / sourceEventSeqs（上游 assertV3Event 的键规则）。
		optional = dshEventOptional
	}
	if detail := exactKeys(record, dshEventRequired, optional); detail != "" {
		return dshEvent{}, "row " + detail
	}
	event := dshEvent{Type: extractString(record["type"])}
	if event.Type == "" {
		return dshEvent{}, "row type is empty"
	}
	seq, ok := safeInt(record["seq"])
	if !ok || seq < 0 {
		return dshEvent{}, "row " + event.Type + " seq is not a non-negative safe integer"
	}
	event.Seq = seq
	time, ok := safeInt(record["time"])
	if !ok {
		return dshEvent{}, "row " + event.Type + " time is not a safe integer"
	}
	event.Time = float64(time)
	if raw, present := record["ignorable"]; present {
		flag, ok := raw.(bool)
		if !ok || !flag {
			// 上游只认真正的 true：`ignorable:false` 是格式错误，不是「必须理解」。
			return dshEvent{}, "row " + event.Type + " ignorable is present but not true"
		}
		event.Ignorable = true
	}
	event.Data = asRecord(record["data"])
	if event.Data == nil {
		return dshEvent{}, "row " + event.Type + " data is not an object"
	}
	if sources, present := record["sourceEventSeqs"]; present {
		if event.Type == "assistant/message" {
			// 上游 v3 的显式规则：assistant/message 自己内嵌了 stream，不允许再带溯源坐标。
			return dshEvent{}, "row assistant/message embeds its stream and cannot carry sourceEventSeqs"
		}
		if detail := validateSourceEventSeqs(sources, event.Seq); detail != "" {
			return dshEvent{}, detail
		}
	}
	operation, present := record["surfaceOp"]
	if !present {
		if surface {
			// surface 事件必须带位置标记，否则无法判断它落在 surface 的哪一段。
			return dshEvent{}, "row " + event.Type + " is surface-eligible and requires a surfaceOp marker"
		}
		return event, ""
	}
	if !surface {
		return dshEvent{}, "row " + event.Type + " is not surface-eligible and cannot carry surfaceOp"
	}
	if text, ok := operation.(string); ok {
		if text != "append" {
			return dshEvent{}, "row " + event.Type + " surfaceOp is neither append nor a replace marker"
		}
		event.SurfaceOp = "append"
		return event, ""
	}
	replace := asRecord(operation)
	if replace == nil || len(replace) != 3 || extractString(replace["op"]) != "replace" {
		return dshEvent{}, "row " + event.Type + " surfaceOp is not an exact replace marker"
	}
	for _, key := range []string{"startSeq", "endSeq"} {
		end, ok := safeInt(replace[key])
		if !ok || end < 0 || end >= seq {
			return dshEvent{}, "row " + event.Type + " surfaceOp " + key + " does not name an earlier event"
		}
	}
	event.SurfaceOp = "replace"
	return event, ""
}

// validateSourceEventSeqs 校验 `sourceEventSeqs`：它是**溯源坐标**，必须是一组指向本行之前
// 事件的序号，不能只当成一个可以不看的可选字段。
//
// 它虽然不参与本包当前的角色/块推导（工具结果与调用靠 callId 关联，不靠它），但一条
// 指向前方、重复、越界或形状不对的溯源坐标说明这份日志的生成逻辑已经和格式契约脱节，
// 继续解释下去没有意义。规则对照上游 `decodeSeqRanges` 与 v3 的 `assertV3Event`：
// 非空数组；成员要么是非负安全整数，要么是 [start,end] 且 start<=end；每个被引用的序号
// 必须严格小于本行 seq；不得重复；展开后的总量不得超过本行 seq；一旦用了区间形态，
// 展开结果必须严格递增。
func validateSourceEventSeqs(value any, seq int64) string {
	entries, ok := value.([]any)
	if !ok {
		return "row sourceEventSeqs must be an array"
	}
	if len(entries) == 0 {
		return "row sourceEventSeqs must not be empty"
	}
	seen := make(map[int64]bool, len(entries))
	hasRange := false
	ordered := true
	previous := int64(-1)
	for _, entry := range entries {
		var start, end int64
		if pair, ok := entry.([]any); ok {
			if len(pair) != 2 {
				return "row sourceEventSeqs range must be a [start, end] pair"
			}
			var okStart, okEnd bool
			start, okStart = safeInt(pair[0])
			end, okEnd = safeInt(pair[1])
			if !okStart || !okEnd || start < 0 || end < start {
				return "row sourceEventSeqs range bounds are not an ordered non-negative pair"
			}
			hasRange = true
		} else {
			var ok bool
			start, ok = safeInt(entry)
			if !ok || start < 0 {
				return "row sourceEventSeqs member is not a non-negative safe integer"
			}
			end = start
			if seen[start] {
				return "row sourceEventSeqs must not contain duplicates"
			}
			seen[start] = true
		}
		if end >= seq {
			return "row sourceEventSeqs must reference earlier events"
		}
		// Validate interval boundaries without expanding attacker-controlled ranges.
		// If any range is present, strict ordering also proves non-overlap and bounds
		// the total cardinality by seq. Scalar-only lists may be unordered.
		if start <= previous {
			ordered = false
		}
		previous = end
	}
	if hasRange && !ordered {
		return "row sourceEventSeqs ranges must be strictly increasing"
	}
	return ""
}

// decodeDSHLine 把一行 DSH v3 JSONL 解码成一条记录。
//
// Failure 非 nil 表示这一行让整份日志**不可信**：调用方必须停止并如实报错，
// 不能把剩下的内容当成完整对话继续发布。
func decodeDSHLine(context LineContext, line string) lineOutcome {
	record := parseJSONObject(line)
	if record == nil {
		// 完整的一行却不是合法 JSON：与另外两个 provider 一致，只丢这一行。
		return lineOutcome{skipped: true}
	}
	if version, ok := dshHeaderProbe(line); ok {
		// header 行不是事件。代际不对要明说，不能按当前代际硬解。
		if version != DSHFormatVersion {
			return lineOutcome{failure: &FormatFailure{
				Reason: FormatUnsupportedVersion,
				Detail: "DSH session format v" + strconv.Itoa(version) + " is not implemented; only v" + strconv.Itoa(DSHFormatVersion) + " is supported",
			}}
		}
		header, detail := parseDSHHeader(line)
		if detail != "" {
			return lineOutcome{failure: &FormatFailure{Reason: FormatInvalidRow, Detail: detail}}
		}
		if header.Seeded {
			// seeded 会话带一段继承来的上下文前缀，它不是这个会话里用户当前说的话。
			// 切分边界要读完整个文件才知道，逐帧分页读取拿不到，所以直接拒绝而不是解一半。
			return lineOutcome{failure: &FormatFailure{
				Reason: FormatUnsupportedSeeded,
				Detail: "DSH session header is seeded (isSeeded:true); inherited prefix semantics are not implemented, so its inherited context is not published as the user's dialogue",
			}}
		}
		return lineOutcome{}
	}
	event, detail := parseDSHEvent(record)
	if detail != "" {
		return lineOutcome{failure: &FormatFailure{Reason: FormatInvalidRow, Detail: detail}}
	}
	disposition, known := dshEventDisposition[event.Type]
	if !known {
		if event.Ignorable {
			// 上游为外部插件事件留的兼容位：明确标记可忽略的未知事件才允许跳过。
			return lineOutcome{}
		}
		return lineOutcome{failure: &FormatFailure{
			Reason: FormatUnsupportedEvent,
			Detail: "unknown non-ignorable DSH v3 event " + strconv.Quote(event.Type) + " at seq " + strconv.FormatInt(event.Seq, 10),
		}}
	}
	if dshIgnorableRequired[event.Type] && !event.Ignorable {
		return lineOutcome{failure: &FormatFailure{
			Reason: FormatUnsupportedEvent,
			Detail: "DSH v3 event " + strconv.Quote(event.Type) + " at seq " + strconv.FormatInt(event.Seq, 10) + " requires ignorable:true",
		}}
	}
	if disposition == dshHiddenRecord {
		return lineOutcome{}
	}
	if event.SurfaceOp == "replace" {
		// 落在模型可见 surface 上的改写：它是模型侧的材料，不是人类 transcript 的来源。
		// 应用它要跨行改写已经发布出去的记录，分页读取下做不到「撤回」；
		// 什么都不做又会让被遮蔽的旧内容继续可见。所以这里明确记数、不落地、不假装已应用。
		return lineOutcome{superseded: true}
	}

	id := dshRecordID(context, event.Seq)
	at := parseTimestamp(event.Time)
	switch event.Type {
	case "user/message":
		return dshUserMessage(event, id, at)
	case "assistant/message":
		return dshAssistantMessage(event, id, at)
	case "tool/call":
		return dshToolCall(event, id, at)
	case "tool/result":
		return dshToolResult(event, id, at)
	case "system/message":
		return dshSystemMessage(event, id, at)
	case "turn/end":
		return dshTurnEnd(event, id, at)
	default:
		// 闭集合里没有其它能产出记录的类型；走到这里说明表与分派脱节。
		return lineOutcome{}
	}
}

// dshRecordID 用「会话 id + 事件 seq」作记录身份。
//
// seq 在文件里稠密且单调（上游验证器断言 seq 就是行序号），是天然稳定的坐标：同一段内容
// 无论分几次读、从哪个字节起读，都得到同一个 id，分页与去重因此都落在同一把键上。
//
// 刻意**不用** LineContext.Offset：DSH 的压缩读取是逐 zstd 帧调用的，调用方只拿得到
// 帧边界（同一帧里的所有行共用一个偏移），拿它当身份会让同一帧内的记录 id 全部相撞，
// 被 UpsertEntries 合并成一条。会话 id 缺失时退回 seq 本身，仍然逐行唯一。
func dshRecordID(context LineContext, seq int64) string {
	identity := strconv.FormatInt(seq, 10)
	if context.SessionID == "" {
		return "dsh:" + identity
	}
	return context.SessionID + ":" + identity
}

// dshUserMessage 解码 `user/message`。
//
// data 就是消息本身：`{role:"user", id, content, source}`。source.kind 决定这条到底是不是
// 用户真的说的话：只有 `user` 才是用户输入；其余（plugin / agent-instructions /
// session-reference / skill-invocation / skill-catalog / goal / team-message / webhook /
// coordinator / subagent-report / subagent-settled / agent-message 等 relay）都是
// **注进上下文的系统内容**，必须标成 system，绝不能冒充用户提问。
func dshUserMessage(event dshEvent, id, at string) lineOutcome {
	blocks, unrecognized := dshContentBlocks(event.Data["content"], true)
	if len(blocks) == 0 {
		return lineOutcome{skipped: unrecognized}
	}
	source := asRecord(event.Data["source"])
	role := RoleSystem
	switch {
	case allToolResult(blocks):
		// 派生角色：全是 tool-result 的记录是工具输出，不是任何人的话。
		role = RoleTool
	case extractString(source["kind"]) == "user":
		role = RoleUser
	}
	return lineOutcome{record: &Record{ID: id, Role: role, At: at, Blocks: blocks}}
}

// dshAssistantMessage 解码 `assistant/message`。
//
// 规范可见文本来自 `data.message.content` 里的 text 块；reasoning 按契约省略（没有
// reasoning 角色），image / file 引用也不输出。
//
// content 里的 tool-call 块在这里**刻意丢掉**：每一次派发都会另写一条 `tool/call` 事件
// （同一 callId / name / arguments），保留两边会让同一个工具卡片出现两次。tool-call 块
// 让位于有独立 seq、独立时间戳的 `tool/call` 记录，顺序也更准。
func dshAssistantMessage(event dshEvent, id, at string) lineOutcome {
	message := asRecord(event.Data["message"])
	if message == nil {
		return lineOutcome{skipped: true}
	}
	blocks, unrecognized := dshContentBlocks(message["content"], false)
	if len(blocks) == 0 {
		// 空 content 的 assistant/message 只是 usage 的载体（上游 deriveEventMessage
		// 对它同样返回 null），不是丢消息。
		return lineOutcome{}
	}
	return lineOutcome{record: &Record{ID: id, Role: RoleAssistant, At: at, Blocks: blocks}, skipped: unrecognized}
}

// dshToolCall 解码 `tool/call`：`{turn, step, callId, name, arguments}`。
//
// role 是 assistant：工具调用是模型这一侧的动作，与 Claude/Codex 把 tool_use 放进
// assistant 记录是同一个心智模型。
func dshToolCall(event dshEvent, id, at string) lineOutcome {
	callID := extractString(event.Data["callId"])
	if callID == "" {
		return lineOutcome{skipped: true}
	}
	return lineOutcome{record: &Record{ID: id, Role: RoleAssistant, At: at, Blocks: []Block{{
		Type:   BlockToolCall,
		CallID: callID,
		Name:   toolName(event.Data["name"]),
		Input:  dshArguments(event.Data["arguments"]),
	}}}}
}

// dshToolResult 解码 `tool/result`：`{turn, step, message, error?, meta?}`。
//
// 关联靠 callId 而不是靠位置：上游要求 message 里恰好一个 tool-result 块，且
// `block.toolCallId === message.source.callId`，所以两条记录天然共用同一个 callId，
// 不需要读 `sourceEventSeqs`、也不需要跨行的状态。
func dshToolResult(event dshEvent, id, at string) lineOutcome {
	message := asRecord(event.Data["message"])
	if message == nil {
		return lineOutcome{skipped: true}
	}
	blocks, unrecognized := dshContentBlocks(message["content"], true)
	if len(blocks) == 0 {
		return lineOutcome{skipped: unrecognized}
	}
	return lineOutcome{record: &Record{ID: id, Role: RoleTool, At: at, Blocks: blocks}, skipped: unrecognized}
}

// dshSystemMessage 解码 v3 的 `system/message`。
//
// 上游 `deriveEventMessage` 把它投影成 role:"system" 的消息，v2→v3 的迁移也正是为了
// 把旧的 `request/header.system` 从请求信封里挪到这个显式事件里。角色就是 system，
// 它永远不会被渲染成用户提问。
func dshSystemMessage(event dshEvent, id, at string) lineOutcome {
	message := asRecord(event.Data["message"])
	if message == nil {
		return lineOutcome{skipped: true}
	}
	blocks, unrecognized := dshContentBlocks(message["content"], true)
	if len(blocks) == 0 {
		return lineOutcome{skipped: unrecognized}
	}
	return lineOutcome{record: &Record{ID: id, Role: RoleSystem, At: at, Blocks: blocks}}
}

// dshTurnEnd 把「这一轮被打断」写成系统状态行。
//
// 上游的 reason.kind 有 completed / blocked / max-tokens / interrupted / aborted / error。
// 只有 interrupted 与 aborted 是「用户或父会话把它掐掉了」，与 Claude 的
// interruptedMessageId、Codex 的 turn_aborted 是同一件事，所以共用同一句产品文案。
// 其余 reason 是正常生命周期，不产出记录。
func dshTurnEnd(event dshEvent, id, at string) lineOutcome {
	reason := asRecord(event.Data["reason"])
	switch extractString(reason["kind"]) {
	case "interrupted", "aborted":
		return lineOutcome{record: &Record{ID: id, Role: RoleSystem, At: at, Blocks: []Block{{Type: BlockText, Text: InterruptedNotice}}}}
	default:
		return lineOutcome{}
	}
}

// ── content 块 ──

// dshContentBlocks 把 DSH 的 content 块数组转成契约块。
//
// keepToolCalls 为假时丢弃工具调用块（assistant/message 用，理由见 dshAssistantMessage）。
// 第二个返回值表示「出现了契约里没有的块类型」：那是这一行没能完整解出来，计入 skipped。
// 上游对未归类块类型的处置比这里更硬（直接拒绝整份日志），但对**消息内部**的一个块，
// 丢掉它比让整份对话不可读更可接受，且 skipped 已经把这次丢失显式暴露出来了。
func dshContentBlocks(content any, keepToolCalls bool) ([]Block, bool) {
	items, ok := content.([]any)
	if !ok {
		// content 缺失或不是数组：这是消息形状的行却解不出东西，要计入 skipped。
		return nil, true
	}
	blocks := make([]Block, 0, len(items))
	unrecognized := false
	for _, item := range items {
		record := asRecord(item)
		if record == nil {
			unrecognized = true
			continue
		}
		switch extractString(record["type"]) {
		case "text":
			if text := rawString(record["text"]); strings.TrimSpace(text) != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
		case "reasoning", "image", "file":
			// 契约没有 reasoning 角色，也不输出图片/文件引用：按设计省略，不算未识别。
		case "tool-call":
			if !keepToolCalls {
				continue
			}
			blocks = append(blocks, Block{
				Type:   BlockToolCall,
				CallID: extractString(record["id"]),
				Name:   toolName(record["name"]),
				Input:  dshArguments(record["arguments"]),
			})
		case "tool-result":
			blocks = append(blocks, Block{
				Type:    BlockToolResult,
				CallID:  extractString(record["toolCallId"]),
				Output:  dshText(record["content"]),
				IsError: isTrue(record["isError"]),
			})
		default:
			unrecognized = true
		}
	}
	return blocks, unrecognized
}

// dshArguments 把 DSH 的 `arguments` 转成契约里的 input。
//
// DSH 的 arguments 是**字符串**（模型吐出来的 JSON 文本），而契约的 input 是 JSON 值。
// 能当 JSON 解析时按解析后的结构发布，让工具卡片能展开成对象；解析不了就原样当字符串，
// 绝不猜测或塞一个空对象。
func dshArguments(value any) json.RawMessage {
	text := rawString(value)
	if strings.TrimSpace(text) == "" {
		return rawJSON(nil)
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	return rawJSON(text)
}

// dshText 把嵌套的 content 块拍平成一段文本（工具结果用）。
func dshText(content any) string {
	blocks, _ := dshContentBlocks(content, true)
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			parts = append(parts, block.Text)
		case BlockToolResult:
			parts = append(parts, block.Output)
		}
	}
	return strings.Join(parts, "\n")
}

// allToolResult 报告块集合是否全是工具结果。
func allToolResult(blocks []Block) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != BlockToolResult {
			return false
		}
	}
	return true
}

// ── 取值辅助 ──

// exactKeys 要求对象恰好带 required 与 optional 里的键。
// 第二个返回值是失败原因；为空表示通过。
//
// 多出来的键是错误而不是忽略：无法判断它是否携带坐标，猜错就会重建出错误的对话。
func exactKeys(record map[string]any, required, optional []string) string {
	for _, key := range required {
		if _, present := record[key]; !present {
			return "lacks required field " + key
		}
	}
	for key := range record {
		allowed := false
		for _, candidate := range required {
			if key == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, candidate := range optional {
				if key == candidate {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return "has unexpected field " + key
		}
	}
	return ""
}

// safeInt 取 JSON 安全整数。
//
// JSON 数字经 encoding/json 解成 float64：只有能原样表示的整数才接受，
// 超过 2^53 的值已经丢失精度，宁可判为不合法也不能当成坐标用。
func safeInt(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, false
	}
	if number < -9007199254740992 || number > 9007199254740992 {
		return 0, false
	}
	return int64(number), true
}
