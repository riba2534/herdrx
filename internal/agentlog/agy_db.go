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
// 不要加 immutable，否则会漏掉还没 checkpoint 的写入。

const (
	agyDefaultLimit     = 64
	agyMaxLimit         = 200
	agyDefaultStepBytes = 1 << 20
	agyDefaultTotal     = 768 << 10
)

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
func QueryAgyDB(ctx context.Context, path string, bounds AgyBounds) ([]AgyRow, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
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
	return out, !stopped && scanned < limit, nil
}
