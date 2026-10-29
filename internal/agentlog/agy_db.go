package agentlog

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

// 只读打开 Antigravity 的 sqlite。mode=ro 仍会看见同目录的 -wal / -shm；
// 不要加 immutable，否则会漏掉还没 checkpoint 的写入，Agent 正在写时还可能读到半页。
// 代价：库处于 WAL 模式而 `-shm` 不存在时（Agent 没在运行且文件被清掉），sqlite 会在同目录
// 建一个 `-shm` 索引文件，与 Agent 自己打开时建的相同；库文件和 `-wal` 内容不会被改写
// （mode=ro + query_only）。
//
// sqlite 只能按路径名打开文件，而调用方是按 fd 逐段核对过的。为了不让「核对之后把中间目录
// 换成符号链接」的竞态生效，查询结束、连接还开着时确认本进程确实有一个 fd 指向核对过的
// inode（/proc/self/fd 或 /dev/fd），否则丢弃结果。

const (
	agyDefaultLimit     = 64
	agyMaxLimit         = 200
	agyDefaultStepBytes = 1 << 20
	agyDefaultTotal     = 768 << 10
)

// agyBeforeOpen 只给测试用：在核对之后、sqlite 按路径名打开之前模拟一次换文件。
var agyBeforeOpen = func(string) {}

// ErrAgyDenied 表示路径不是普通文件（含符号链接）。
var ErrAgyDenied = errors.New("agy transcript denied")

// ErrAgyUnrecognized 表示文件不是带 steps 表的会话库。
var ErrAgyUnrecognized = errors.New("agy transcript unrecognized")

// AgyBounds 是一次有界的 steps 扫描。After / Before 都是不含端点的 idx。
type AgyBounds struct {
	HasAfter      bool
	After         int64
	HasBefore     bool
	Before        int64
	Desc          bool
	Limit         int
	MaxStepBytes  int
	MaxTotalBytes int
}

// AgyRow 是 steps 表的一行。Oversize 为真时 Payload 为空，调用方跳过这一行。
type AgyRow struct {
	Idx        int64
	StepType   int
	Status     int
	StepFormat int
	Payload    []byte
	Oversize   bool
}

// QueryAgyDB 按 idx 读取若干 step。end 为真表示这个方向已经没有更多行。
// 返回的错误不会带上 payload 正文。
// verified 是调用方按 fd 核对过的文件信息；为 nil 时（调试命令读用户指定的文件）只做 Lstat 检查。
func QueryAgyDB(ctx context.Context, path string, verified os.FileInfo, bounds AgyBounds) ([]AgyRow, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || verified != nil && !os.SameFile(info, verified) {
		return nil, false, ErrAgyDenied
	}
	limit := bounds.Limit
	if limit <= 0 {
		limit = agyDefaultLimit
	}
	if limit > agyMaxLimit {
		limit = agyMaxLimit
	}
	maxStep := bounds.MaxStepBytes
	if maxStep <= 0 {
		maxStep = agyDefaultStepBytes
	}
	maxTotal := bounds.MaxTotalBytes
	if maxTotal <= 0 {
		maxTotal = agyDefaultTotal
	}
	agyBeforeOpen(path)
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, false, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 2000"); err != nil {
		return nil, false, err
	}
	direction := "ASC"
	if bounds.Desc {
		direction = "DESC"
	}
	hasAfter, hasBefore := 0, 0
	if bounds.HasAfter {
		hasAfter = 1
	}
	if bounds.HasBefore {
		hasBefore = 1
	}
	rows, err := db.QueryContext(ctx, `
SELECT idx, IFNULL(step_type, 0), IFNULL(status, 0), IFNULL(step_format, 0),
       IFNULL(length(step_payload), 0),
       CASE WHEN IFNULL(length(step_payload), 0) > ? THEN NULL ELSE step_payload END
FROM steps
WHERE (? = 0 OR idx > ?) AND (? = 0 OR idx < ?)
ORDER BY idx `+direction+` LIMIT ?`,
		maxStep, hasAfter, bounds.After, hasBefore, bounds.Before, limit)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, false, ErrAgyUnrecognized
		}
		return nil, false, err
	}
	defer rows.Close()
	out := make([]AgyRow, 0, limit)
	total := 0
	scanned := 0
	stopped := false
	for rows.Next() {
		var row AgyRow
		var length int64
		var payload []byte
		if err := rows.Scan(&row.Idx, &row.StepType, &row.Status, &row.StepFormat, &length, &payload); err != nil {
			return nil, false, err
		}
		scanned++
		if length > int64(maxStep) {
			row.Oversize = true
			payload = nil
		}
		size := len(payload)
		if len(out) > 0 && total+size > maxTotal {
			stopped = true
			break
		}
		total += size
		row.Payload = payload
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if verified != nil && !agyOpenedInode(verified) {
		return nil, false, ErrAgyDenied
	}
	return out, !stopped && scanned < limit, nil
}

// agyOpenedInode 报告本进程是否有 fd 指向 verified 那个 inode。sqlite 在连接关闭前一直持有
// 库文件的 fd；调用方核对用的 fd 在查询前已经关闭，不会自我匹配。
func agyOpenedInode(verified os.FileInfo) bool {
	for _, base := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := os.Stat(base + "/" + entry.Name())
			if err == nil && os.SameFile(info, verified) {
				return true
			}
		}
		return false
	}
	return false
}
