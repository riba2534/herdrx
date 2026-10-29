package herdr

// transcriptReaderSource 是嵌入的受限读取器，随本机与 SSH 两种接入共用一套语义执行。
//
// 为什么用远端 python3 而不是 `test -L` 之后再 `cat`：后者是 test-then-open，中间存在
// 竞态窗口，而且 shell 拼串会把路径变成语法。这里所有路径都是**从 HOME 的目录句柄出发**
// 逐段 `os.open(..., dir_fd=..., O_NOFOLLOW)` 解析的，任何一段（含父目录）是符号链接都会
// 被 ELOOP 拒绝，最终必须是普通文件；请求经 stdin 以 JSON 传入，从不进入命令行。
//
// 远端没有 python3 时**明确返回不支持**，绝不退回不安全的读法。
const transcriptReaderSource = `import base64, errno, json, os, re, stat, sys

ROOTS = {"claude": ".claude/projects", "codex": ".codex/sessions", "traex": ".trae/cli/sessions", "dsh": ".dsh/sessions", "cursor": ".cursor/projects", "gemini": ".gemini/tmp", "agy": ".gemini/antigravity-cli/conversations", "pi": ".pi/agent/sessions", "grok": ".grok/sessions"}
AGY_DB = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\.db$")
AGY_STEP_BYTES = 1 << 20
AGY_TOTAL_BYTES = 768 << 10
CLOEXEC = getattr(os, "O_CLOEXEC", 0)
DIR_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | getattr(os, "O_DIRECTORY", 0) | CLOEXEC
FILE_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | CLOEXEC | os.O_NONBLOCK
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
        def newest_first(name):
            try:
                info = os.stat(name, dir_fd=fd, follow_symlinks=False)
                is_dir = stat.S_ISDIR(info.st_mode)
                return (is_dir, name if is_dir else "", info.st_mtime_ns, name)
            except OSError:
                return (False, "", 0, name)
        names = sorted(os.listdir(fd), key=newest_first, reverse=True)
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


def opened_inode(verified):
    for base in ("/proc/self/fd", "/dev/fd"):
        try:
            names = os.listdir(base)
        except OSError:
            continue
        for name in names:
            try:
                info = os.stat(base + "/" + name)
            except OSError:
                continue
            if (info.st_dev, info.st_ino) == verified:
                return True
        return False
    return False


def op_agy(home, root_fd, request):
    rel = request.get("rel")
    if not isinstance(rel, str) or AGY_DB.fullmatch(rel) is None:
        return {"ok": False, "error": "denied"}
    try:
        fd = open_file(root_fd, rel)
    except OSError as exc:
        return {"ok": False, "error": classify(exc)}
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            return {"ok": False, "error": "denied"}
        identity = "%x:%x" % (info.st_dev, info.st_ino)
        verified = (info.st_dev, info.st_ino)
    finally:
        os.close(fd)
    limit = request.get("agy_limit") or 64
    if isinstance(limit, bool) or not isinstance(limit, int) or limit < 1:
        limit = 64
    if limit > 200:
        limit = 200
    after = request.get("agy_after") or 0
    before = request.get("agy_before") or 0
    if isinstance(after, bool) or not isinstance(after, int) or after < 0:
        return {"ok": False, "error": "denied"}
    if isinstance(before, bool) or not isinstance(before, int) or before < 0:
        return {"ok": False, "error": "denied"}
    has_after = 1 if request.get("agy_has_after") is True else 0
    has_before = 1 if request.get("agy_has_before") is True else 0
    order = "DESC" if request.get("agy_desc") is True else "ASC"
    # sqlite3 只有 agy 用到。远端 python3 缺 _sqlite3 时只让 agy 降级，其他读取器照常。
    try:
        import sqlite3
    except ImportError:
        return {"ok": False, "error": "agy_unsupported"}
    from urllib.parse import quote
    path = os.path.join(home, ROOTS["agy"], rel)
    # 打开前再按路径名核对一次：必须仍是同一个 inode 的普通文件。
    try:
        named = os.lstat(path)
    except OSError as exc:
        return {"ok": False, "error": classify(exc)}
    if not stat.S_ISREG(named.st_mode) or (named.st_dev, named.st_ino) != verified:
        return {"ok": False, "error": "denied"}
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % quote(path, safe="/"), uri=True)
    except sqlite3.Error:
        return {"ok": False, "error": "unrecognized"}
    try:
        conn.execute("PRAGMA query_only=ON")
        conn.execute("PRAGMA busy_timeout=2000")
        try:
            fetched = conn.execute(
                "SELECT idx, IFNULL(step_type,0), IFNULL(status,0), IFNULL(step_format,0), IFNULL(length(step_payload),0), "
                "CASE WHEN IFNULL(length(step_payload),0) > ? THEN NULL ELSE step_payload END "
                "FROM steps WHERE (? = 0 OR idx > ?) AND (? = 0 OR idx < ?) ORDER BY idx " + order + " LIMIT ?",
                (AGY_STEP_BYTES, has_after, after, has_before, before, limit),
            ).fetchall()
        except sqlite3.Error:
            return {"ok": False, "error": "unrecognized"}
        # sqlite 按路径名重新打开。中间目录可能在核对之后被换成符号链接，所以查询完、连接还开着时
        # 确认本进程确实有一个 fd 指向刚才逐段核对过的那个 inode，否则丢弃结果。
        if not opened_inode(verified):
            return {"ok": False, "error": "denied"}
    finally:
        conn.close()
    steps = []
    total = 0
    stopped = False
    for idx, step_type, status, step_format, length, payload in fetched:
        size = 0 if payload is None else len(payload)
        if steps and total + size > AGY_TOTAL_BYTES:
            stopped = True
            break
        total += size
        item = {"idx": idx, "step_type": step_type, "status": status, "step_format": step_format}
        if payload is None and length > AGY_STEP_BYTES:
            item["oversize"] = True
        elif payload:
            item["data"] = base64.b64encode(payload).decode("ascii")
        steps.append(item)
    return {"ok": True, "identity": identity, "end": (not stopped) and len(fetched) < limit, "steps": steps}


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
    logical = os.path.expanduser("~")
    if not logical.startswith("/"):
        return {"ok": False, "error": "home_unavailable"}
    try:
        home = os.path.realpath(logical)
    except OSError:
        return {"ok": False, "error": "home_unavailable"}
    if not home.startswith("/"):
        return {"ok": False, "error": "home_unavailable"}
    def finish(payload):
        if isinstance(payload, dict):
            payload["home"] = logical
            payload["home_real"] = home
        return payload
    try:
        home_fd = os.open(home, DIR_FLAGS)
        try:
            root_fd = open_dir(home_fd, ROOTS[agent])
        finally:
            os.close(home_fd)
    except OSError as exc:
        return finish({"ok": False, "error": "root_unavailable" if exc.errno == errno.ENOENT else "denied"})
    try:
        op = request.get("op")
        if op == "list":
            return finish(op_list(root_fd, request))
        if op == "read":
            return finish(op_read(root_fd, request))
        if op == "probe":
            return finish({"ok": True})
        if op == "agy":
            return finish(op_agy(home, root_fd, request))
        return finish({"ok": False, "error": "unknown_op"})
    finally:
        os.close(root_fd)


try:
    sys.stdout.write(json.dumps(main()))
except Exception:
    sys.stdout.write(json.dumps({"ok": False, "error": "internal"}))
`
