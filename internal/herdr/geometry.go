package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/riba2534/herdrx/internal/terminalgeometry"
	"golang.org/x/crypto/ssh"
)

func terminalPID(ctx context.Context, endpoint Endpoint, paneID string) (int, error) {
	if !publicID.MatchString(paneID) {
		return 0, fmt.Errorf("invalid pane id")
	}
	result, err := endpoint.Call(ctx, "pane.process_info", map[string]any{"pane_id": paneID})
	if err != nil {
		return 0, err
	}
	var reply struct {
		Info struct {
			PaneID string `json:"pane_id"`
			PID    int    `json:"shell_pid"`
		} `json:"process_info"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return 0, fmt.Errorf("decode terminal process: %w", err)
	}
	if reply.Info.PaneID != paneID || reply.Info.PID <= 1 || reply.Info.PID > 1<<30 {
		return 0, fmt.Errorf("terminal process is unavailable")
	}
	return reply.Info.PID, nil
}

func terminalSocketPath(apiPath string) (string, error) {
	if !strings.HasSuffix(apiPath, "/herdr.sock") {
		return "", fmt.Errorf("unknown Herdr socket layout")
	}
	return strings.TrimSuffix(apiPath, "herdr.sock") + "herdr-client.sock", nil
}

func (e *LocalEndpoint) TerminalGeometry(ctx context.Context, paneID string) (TerminalGeometry, error) {
	pid, err := terminalPID(ctx, e, paneID)
	if err != nil {
		return TerminalGeometry{}, err
	}
	if err := ctx.Err(); err != nil {
		return TerminalGeometry{}, err
	}
	return terminalgeometry.ReadPID(pid)
}

func (e *LocalEndpoint) OpenTerminalSocket(ctx context.Context) (net.Conn, error) {
	path, err := terminalSocketPath(e.SocketPath)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

// This fixed program only opens stdin's terminal and performs TIOCGWINSZ. The
// validated integer PID is a separate argument; no remote data becomes code.
const remoteGeometryProgram = `import os,sys,fcntl,termios,struct,json
if sys.platform != "linux": raise RuntimeError("Linux Herdr host required")
pid=int(sys.argv[1])
if not 1 < pid <= 1073741824: raise RuntimeError("invalid terminal process id")
fd=os.open("/proc/%d/fd/0" % pid,os.O_RDONLY|os.O_NOCTTY|os.O_NONBLOCK)
try: rows,cols,width,height=struct.unpack("HHHH",fcntl.ioctl(fd,termios.TIOCGWINSZ,bytes(8)))
finally: os.close(fd)
if not (10<=cols<=1000 and 3<=rows<=500) or width==65535 or height==65535 or width%cols or height%rows: raise RuntimeError("terminal size cannot be preserved exactly")
print(json.dumps(dict(cols=cols,rows=rows,cell_width_px=width//cols,cell_height_px=height//rows)))`

func (e *SSHEndpoint) TerminalGeometry(ctx context.Context, paneID string) (TerminalGeometry, error) {
	pid, err := terminalPID(ctx, e, paneID)
	if err != nil {
		return TerminalGeometry{}, err
	}
	session, err := e.newSession(ctx)
	if err != nil {
		return TerminalGeometry{}, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	command := fmt.Sprintf("python3 -c '%s' %d", remoteGeometryProgram, pid)
	if e.host.Transport == "tailcat" {
		command = fmt.Sprintf("herdrx-terminal-geometry %d", pid)
	}
	// 远端读取程序失败时只在 stderr 说明原因；丢掉它就只剩退出码。
	var stderr limitedSSHLog
	session.setStderr(&stderr)
	output, err := session.Output(command)
	if err != nil {
		if ctx.Err() != nil {
			return TerminalGeometry{}, ctx.Err()
		}
		// 状态 1 表示读取程序已在远端运行并给出原因（如尺寸无法精确保持），与 CLI 版本或 Python 无关。
		// 请求被拒绝、缺少命令（127）或连接失败（255）时 stderr 不可靠，保留下方原提示。
		if reason := stderr.String(); reason != "" && remoteExitStatus(err) == 1 {
			// Python 失败时 stderr 是完整 traceback，原因在最后一行。
			reason = reason[strings.LastIndexByte(reason, '\n')+1:]
			return TerminalGeometry{}, fmt.Errorf("无法读取远程终端尺寸（远程主机报告：%s），请在远程主机的 Herdr 中直接查看此终端: %w", reason, err)
		}
		if e.host.Transport == "tailcat" {
			return TerminalGeometry{}, fmt.Errorf("无法读取远程终端尺寸，请更新远程主机上的 herdrx CLI 后重试: %w", err)
		}
		return TerminalGeometry{}, fmt.Errorf("无法读取远程终端尺寸，请确认 Herdr 主机为 Linux 且已安装 Python 3: %w", err)
	}
	var geometry TerminalGeometry
	if err := json.Unmarshal(output, &geometry); err != nil {
		return geometry, fmt.Errorf("decode remote terminal geometry: %w", err)
	}
	return geometry, geometry.Validate()
}

// remoteExitStatus 返回远端命令的退出状态；请求未执行或连接中断时为 -1。
// 内置 SSH 客户端与系统 OpenSSH 分别以 ssh.ExitError 和 exec.ExitError 报告。
func remoteExitStatus(err error) int {
	var sshExit *ssh.ExitError
	if errors.As(err, &sshExit) {
		return sshExit.ExitStatus()
	}
	var processExit *exec.ExitError
	if errors.As(err, &processExit) {
		return processExit.ExitCode()
	}
	return -1
}

func (e *SSHEndpoint) OpenTerminalSocket(ctx context.Context) (net.Conn, error) {
	path, err := terminalSocketPath(e.socketPath)
	if err != nil {
		return nil, err
	}
	return e.client.DialContext(ctx, "unix", path)
}
