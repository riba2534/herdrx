package herdr

// transcriptReaderSource 是嵌入的受限读取器，随本机与 SSH 两种接入共用一套语义执行。
//
// 为什么用远端 python3 而不是 `test -L` 之后再 `cat`：后者是 test-then-open，中间存在
// 竞态窗口，而且 shell 拼串会把路径变成语法。这里所有路径都是**从 HOME 的目录句柄出发**
// 逐段 `os.open(..., dir_fd=..., O_NOFOLLOW)` 解析的，任何一段（含父目录）是符号链接都会
// 被 ELOOP 拒绝，最终必须是普通文件；请求经 stdin 以 JSON 传入，从不进入命令行。
//
// 远端没有 python3 时**明确返回不支持**，绝不退回不安全的读法。
const transcriptReaderSource = `import base64, errno, json, os, stat, sys

ROOTS = {"claude": ".claude/projects", "codex": ".codex/sessions"}
CLOEXEC = getattr(os, "O_CLOEXEC", 0)
DIR_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | getattr(os, "O_DIRECTORY", 0) | CLOEXEC
FILE_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | CLOEXEC
ENTRY_LIMIT = 500
MAX_READ = 1 << 20


def split_rel(rel):
    if not isinstance(rel, str):
        raise ValueError("rel")
    text = rel.strip()
    if text in ("", "."):
        return []
    if text.startswith("/") or "\\" in text or "\x00" in text:
        raise ValueError("rel")
    parts = text.split("/")
    for part in parts:
        if part in ("", ".", ".."):
            raise ValueError("rel")
    return parts


def open_dir(root_fd, rel):
    fd = os.dup(root_fd)
    try:
        for part in split_rel(rel):
            child = os.open(part, DIR_FLAGS, dir_fd=fd)
            os.close(fd)
            fd = child
        return fd
    except BaseException:
        os.close(fd)
        raise


def open_file(root_fd, rel):
    parts = split_rel(rel)
    if not parts:
        raise ValueError("rel")
    parent = open_dir(root_fd, "/".join(parts[:-1]))
    try:
        return os.open(parts[-1], FILE_FLAGS, dir_fd=parent)
    finally:
        os.close(parent)


def classify(exc):
    if getattr(exc, "errno", None) == errno.ENOENT:
        return "not_found"
    return "denied"


def collect(root_fd, rel, depth, entries):
    if depth < 1 or len(entries) >= ENTRY_LIMIT:
        return
    fd = open_dir(root_fd, rel)
    try:
        names = sorted(os.listdir(fd))
        for name in names:
            if len(entries) >= ENTRY_LIMIT:
                return
            child = (rel + "/" + name) if rel else name
            entry = {"rel": child, "dir": False, "link": False, "size": 0, "mtime": 0}
            try:
                handle = os.open(name, DIR_FLAGS, dir_fd=fd)
            except OSError as exc:
                if exc.errno == errno.ELOOP:
                    entry["link"] = True
                    entries.append(entry)
                    continue
                try:
                    file_fd = os.open(name, FILE_FLAGS, dir_fd=fd)
                except OSError as inner:
                    if inner.errno == errno.ELOOP:
                        entry["link"] = True
                        entries.append(entry)
                    continue
                try:
                    info = os.fstat(file_fd)
                finally:
                    os.close(file_fd)
                if not stat.S_ISREG(info.st_mode):
                    continue
                entry["size"] = info.st_size
                entry["mtime"] = int(info.st_mtime)
                entries.append(entry)
                continue
            os.close(handle)
            entry["dir"] = True
            entries.append(entry)
            if depth > 1:
                collect(root_fd, child, depth - 1, entries)
    finally:
        os.close(fd)


def op_list(root_fd, request):
    rel = request.get("rel", "")
    depth = request.get("depth", 1)
    if not isinstance(depth, int) or depth < 1:
        depth = 1
    if depth > 5:
        depth = 5
    entries = []
    try:
        collect(root_fd, rel, depth, entries)
    except ValueError:
        return {"ok": False, "error": "denied"}
    except OSError as exc:
        return {"ok": False, "error": classify(exc)}
    return {"ok": True, "entries": entries}


def op_read(root_fd, request):
    reads = request.get("reads", [])
    if not isinstance(reads, list):
        return {"ok": False, "error": "bad_request"}
    results = []
    for spec in reads[:64]:
        entry = {"exists": False, "size": 0, "identity": "", "data": "", "error": ""}
        if not isinstance(spec, dict):
            entry["error"] = "denied"
            results.append(entry)
            continue
        try:
            fd = open_file(root_fd, spec.get("rel", ""))
        except ValueError:
            entry["error"] = "denied"
            results.append(entry)
            continue
        except OSError as exc:
            entry["error"] = classify(exc)
            results.append(entry)
            continue
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode):
                entry["error"] = "denied"
                results.append(entry)
                continue
            entry["exists"] = True
            entry["size"] = info.st_size
            entry["identity"] = "%x:%x" % (info.st_dev, info.st_ino)
            offset = spec.get("offset", 0)
            length = spec.get("length", 0)
            if not isinstance(offset, int) or not isinstance(length, int) or isinstance(offset, bool) or isinstance(length, bool):
                entry["error"] = "denied"
                results.append(entry)
                continue
            if offset < 0:
                offset = 0
            if length < 0:
                length = 0
            if length > MAX_READ:
                length = MAX_READ
            if offset < info.st_size and length > 0:
                remaining = info.st_size - offset
                if length > remaining:
                    length = remaining
                entry["data"] = base64.b64encode(os.pread(fd, length, offset)).decode("ascii")
        finally:
            os.close(fd)
        results.append(entry)
    return {"ok": True, "reads": results}


def main():
    try:
        raw = sys.stdin.read()
        request = json.loads(raw) if raw.strip() else {}
    except Exception:
        return {"ok": False, "error": "bad_request"}
    if not isinstance(request, dict):
        return {"ok": False, "error": "bad_request"}
    agent = request.get("agent")
    if agent not in ROOTS:
        return {"ok": False, "error": "unknown_agent"}
    home = os.path.expanduser("~")
    if not home.startswith("/"):
        return {"ok": False, "error": "home_unavailable"}
    try:
        root_fd = os.open(os.path.join(home, ROOTS[agent]), DIR_FLAGS)
    except OSError as exc:
        return {"ok": False, "error": "root_unavailable" if exc.errno == errno.ENOENT else "denied"}
    try:
        op = request.get("op")
        if op == "list":
            return op_list(root_fd, request)
        if op == "read":
            return op_read(root_fd, request)
        if op == "probe":
            return {"ok": True}
        return {"ok": False, "error": "unknown_op"}
    finally:
        os.close(root_fd)


try:
    sys.stdout.write(json.dumps(main()))
except Exception:
    sys.stdout.write(json.dumps({"ok": False, "error": "internal"}))
`
