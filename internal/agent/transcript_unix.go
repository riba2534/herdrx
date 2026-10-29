//go:build unix

package agent

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/riba2534/herdrx/internal/agentlog"
	"golang.org/x/sys/unix"
)

const (
	dirFlags  = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_DIRECTORY | unix.O_CLOEXEC
	fileFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
)

func serveTranscript(home string, raw []byte) (response transcriptWireResponse, err error) {
	logical := home
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil || !strings.HasPrefix(resolved, "/") {
		return transcriptWireResponse{Error: "home_unavailable", Home: logical, HomeReal: logical}, nil
	}
	defer func() {
		response.Home = logical
		response.HomeReal = resolved
	}()
	home = resolved
	request, err := decodeTranscriptRequest(raw)
	if err != nil {
		return transcriptWireResponse{Error: "bad_request"}, nil
	}
	rootName, ok := transcriptRoots[request.Agent]
	if !ok {
		return transcriptWireResponse{Error: "unknown_agent"}, nil
	}
	rootFD, failure := openTranscriptRoot(home, rootName)
	if failure != "" {
		return transcriptWireResponse{Error: failure}, nil
	}
	defer unix.Close(rootFD)
	switch request.Op {
	case "list":
		return listTranscript(rootFD, request), nil
	case "read":
		return readTranscript(rootFD, request), nil
	case "probe":
		return transcriptWireResponse{OK: true}, nil
	case "agy":
		return serveAgy(home, rootName, rootFD, request), nil
	default:
		return transcriptWireResponse{Error: "unknown_op"}, nil
	}
}

func openTranscriptRoot(home, root string) (int, string) {
	homeFD, err := unix.Open(home, dirFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, "root_unavailable"
		}
		return -1, "denied"
	}
	defer unix.Close(homeFD)
	rootFD, err := openDir(homeFD, root)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, "root_unavailable"
		}
		return -1, "denied"
	}
	return rootFD, ""
}

func openDir(rootFD int, rel string) (int, error) {
	parts, err := splitRel(rel)
	if err != nil {
		return -1, err
	}
	fd, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		child, err := unix.Openat(fd, part, dirFlags, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = child
	}
	return fd, nil
}

func openRegular(rootFD int, rel string) (int, error) {
	parts, err := splitRel(rel)
	if err != nil || len(parts) == 0 {
		if err == nil {
			err = errBadRel
		}
		return -1, err
	}
	parent, err := openDir(rootFD, strings.Join(parts[:len(parts)-1], "/"))
	if err != nil {
		return -1, err
	}
	defer unix.Close(parent)
	return unix.Openat(parent, parts[len(parts)-1], fileFlags, 0)
}

func listTranscript(rootFD int, request transcriptWireRequest) transcriptWireResponse {
	entries := make([]transcriptWireEntry, 0)
	err := collectTranscript(rootFD, strings.TrimSpace(request.Rel), clampDepth(request.Depth), &entries)
	if err != nil {
		return transcriptWireResponse{Error: classifyTranscript(err)}
	}
	return transcriptWireResponse{OK: true, Entries: entries}
}

func collectTranscript(rootFD int, rel string, depth int, entries *[]transcriptWireEntry) error {
	if depth < 1 || len(*entries) >= transcriptEntryLimit {
		return nil
	}
	fd, err := openDir(rootFD, rel)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	names, err := transcriptNames(fd)
	if err != nil {
		return err
	}
	for _, item := range names {
		if len(*entries) >= transcriptEntryLimit {
			return nil
		}
		child := item.name
		if rel != "" {
			child = rel + "/" + item.name
		}
		entry := transcriptWireEntry{Rel: child}
		dirFD, err := unix.Openat(fd, item.name, dirFlags, 0)
		if err != nil {
			if errors.Is(err, unix.ELOOP) {
				entry.Link = true
				*entries = append(*entries, entry)
				continue
			}
			fileFD, err := unix.Openat(fd, item.name, fileFlags, 0)
			if err != nil {
				if errors.Is(err, unix.ELOOP) {
					entry.Link = true
					*entries = append(*entries, entry)
				}
				continue
			}
			var st unix.Stat_t
			err = unix.Fstat(fileFD, &st)
			unix.Close(fileFD)
			if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
				continue
			}
			entry.Size = st.Size
			entry.Modified = int64(st.Mtim.Sec)
			*entries = append(*entries, entry)
			continue
		}
		unix.Close(dirFD)
		entry.Dir = true
		*entries = append(*entries, entry)
		if depth > 1 {
			if err := collectTranscript(rootFD, child, depth-1, entries); err != nil {
				return err
			}
		}
	}
	return nil
}

type transcriptName struct {
	name    string
	isDir   bool
	mtimeNS int64
}

func transcriptNames(fd int) ([]transcriptName, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dup), "transcript")
	defer file.Close()
	listed, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]transcriptName, 0, len(listed))
	for _, item := range listed {
		name := transcriptName{name: item.Name()}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, item.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			name.isDir = st.Mode&unix.S_IFMT == unix.S_IFDIR
			name.mtimeNS = st.Mtim.Nano()
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := names[i], names[j]
		if left.isDir != right.isDir {
			return left.isDir
		}
		leftKey, rightKey := "", ""
		if left.isDir {
			leftKey = left.name
		}
		if right.isDir {
			rightKey = right.name
		}
		if leftKey != rightKey {
			return leftKey > rightKey
		}
		if left.mtimeNS != right.mtimeNS {
			return left.mtimeNS > right.mtimeNS
		}
		return left.name > right.name
	})
	return names, nil
}

func readTranscript(rootFD int, request transcriptWireRequest) transcriptWireResponse {
	specs := request.Reads
	if len(specs) > transcriptMaxReads {
		specs = specs[:transcriptMaxReads]
	}
	results := make([]transcriptWireRead, 0, len(specs))
	for _, spec := range specs {
		results = append(results, readTranscriptOne(rootFD, spec))
	}
	return transcriptWireResponse{OK: true, Reads: results}
}

func readTranscriptOne(rootFD int, spec transcriptWireSpec) transcriptWireRead {
	result := transcriptWireRead{}
	fd, err := openRegular(rootFD, spec.Rel)
	if err != nil {
		result.Error = classifyTranscript(err)
		return result
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		result.Error = "denied"
		return result
	}
	result.Exists = true
	result.Size = st.Size
	result.Identity = fmt.Sprintf("%x:%x", uint64(st.Dev), uint64(st.Ino))
	offset, length := spec.Offset, spec.Length
	if offset < 0 {
		offset = 0
	}
	if length < 0 {
		length = 0
	}
	if length > transcriptMaxRead {
		length = transcriptMaxRead
	}
	if offset >= st.Size || length == 0 {
		return result
	}
	if remaining := st.Size - offset; length > remaining {
		length = remaining
	}
	buffer := make([]byte, length)
	got := 0
	for got < len(buffer) {
		n, err := unix.Pread(fd, buffer[got:], offset+int64(got))
		if n > 0 {
			got += n
		}
		if err != nil || n == 0 {
			break
		}
	}
	result.Data = base64.StdEncoding.EncodeToString(buffer[:got])
	return result
}

func serveAgy(home, rootName string, rootFD int, request transcriptWireRequest) transcriptWireResponse {
	if !agentlog.AgyDBRel(request.Rel) {
		return transcriptWireResponse{Error: "denied"}
	}
	fd, err := openRegular(rootFD, request.Rel)
	if err != nil {
		return transcriptWireResponse{Error: classifyTranscript(err)}
	}
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	unix.Close(fd)
	if statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return transcriptWireResponse{Error: "denied"}
	}
	limit := request.AgyLimit
	if limit <= 0 {
		limit = 64
	}
	rows, end, err := agentlog.QueryAgyDB(nil, filepath.Join(home, filepath.FromSlash(rootName), request.Rel), agentlog.AgyBounds{
		HasAfter: request.AgyHasAfter, After: request.AgyAfter,
		HasBefore: request.AgyHasBefore, Before: request.AgyBefore,
		Desc: request.AgyDesc, Limit: limit,
	})
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return transcriptWireResponse{Error: "not_found"}
		case errors.Is(err, agentlog.ErrAgyUnrecognized):
			return transcriptWireResponse{Error: "unrecognized"}
		default:
			return transcriptWireResponse{Error: "denied"}
		}
	}
	steps := make([]transcriptWireStep, 0, len(rows))
	for _, row := range rows {
		step := transcriptWireStep{Idx: row.Idx, StepType: row.StepType, Status: row.Status, Format: row.StepFormat, Oversize: row.Oversize}
		if len(row.Payload) > 0 {
			step.Data = base64.StdEncoding.EncodeToString(row.Payload)
		}
		steps = append(steps, step)
	}
	return transcriptWireResponse{OK: true, Identity: fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), End: end, Steps: steps}
}

func classifyTranscript(err error) string {
	switch {
	case errors.Is(err, errBadRel):
		return "denied"
	case errors.Is(err, unix.ENOENT):
		return "not_found"
	default:
		return "denied"
	}
}
