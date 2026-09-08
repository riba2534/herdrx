package agentcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSubprocessSmoke_RealCLIAndIPC(t *testing.T) {
	tempDir := shortTempDir(t)
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "herdrx")

	// 1. 动态从源码编译 herdrx 到受控临时目录（使用当前测试环境 GOROOT/bin/go 工具链，绝不硬编码路径或 Skip）
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		var lookErr error
		goBin, lookErr = exec.LookPath("go")
		if lookErr != nil {
			t.Fatalf("cannot locate go binary: %v", lookErr)
		}
	}

	buildCmd := exec.Command(goBin, "build", "-o", binPath, "../../cmd/herdrx")
	var buildErr bytes.Buffer
	buildCmd.Stderr = &buildErr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build herdrx binary from source failed: %v (stderr: %s)", err, buildErr.String())
	}

	configDir := filepath.Join(tempDir, "config")
	runtimeDir := filepath.Join(tempDir, "run")
	stateDir := filepath.Join(tempDir, "state")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}

	// 2. 构造独立运行的 Mock Herdr 后台服务（Unix socket），验证生命周期解耦
	mockHerdrSockPath := filepath.Join(tempDir, "mock_herdr.sock")
	mockHerdrListener, err := net.Listen("unix", mockHerdrSockPath)
	if err != nil {
		t.Fatalf("listen mock herdr socket: %v", err)
	}
	defer mockHerdrListener.Close()

	go func() {
		for {
			c, err := mockHerdrListener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 64)
				n, _ := conn.Read(buf)
				_, _ = conn.Write(buf[:n])
			}(c)
		}
	}()

	fakeHerdrPath := filepath.Join(tempDir, "fake_herdr.sh")
	fakeScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "herdr 0.8.2"
    exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "schema" ]; then
    echo '{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}}]}}}'
    exit 0
fi
if [ "$1" = "status" ] && [ "$2" = "server" ]; then
    echo "status: running"
    echo "version: 0.8.2"
    echo "socket: %s"
    exit 0
fi
exit 0
`, mockHerdrSockPath)

	if err := os.WriteFile(fakeHerdrPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatalf("write fake herdr script: %v", err)
	}

	configPath := filepath.Join(configDir, "herdrx", "config.json")

	// 步骤 1: 真实执行 herdrx setup 初始化配置并持久化 herdr-bin
	{
		cmd := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", configPath,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup command failed: %v, stderr: %s", err, errOut.String())
		}

		cfg1, err := SafeLoadConfig(configPath)
		if err != nil || cfg1.SetupID == "" {
			t.Fatalf("config not initialized properly: %+v", cfg1)
		}
		if cfg1.HerdrBin != fakeHerdrPath {
			t.Fatalf("herdr-bin path was not persisted in config: got %s, want %s", cfg1.HerdrBin, fakeHerdrPath)
		}

		// 重复执行 setup：断言身份（NodeKey / SetupID）保持一致未漂移（幂等性）
		cmd2 := exec.Command(binPath, "setup",
			"--herdr-bin", fakeHerdrPath,
			"--config", configPath,
			"--runtime-dir", runtimeDir,
			"--skip-service")
		if err := cmd2.Run(); err != nil {
			t.Fatalf("second setup failed: %v", err)
		}
		cfg2, err := SafeLoadConfig(configPath)
		if err != nil || cfg2.Node.Public.Addr() != cfg1.Node.Public.Addr() || cfg2.SetupID != cfg1.SetupID {
			t.Fatalf("setup must be idempotent: identity drifted")
		}
	}

	// 步骤 2: 启动独立后台子进程 herdrx serve
	serveCmd := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", runtimeDir)
	var serveOut, serveErr bytes.Buffer
	serveCmd.Stdout = &serveOut
	serveCmd.Stderr = &serveErr

	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve subprocess failed: %v", err)
	}
	daemonPid := serveCmd.Process.Pid
	t.Logf("Started herdrx serve subprocess with PID: %d", daemonPid)

	defer func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
		}
	}()

	// 轮询等待 control.sock 就绪
	sockPath := filepath.Join(runtimeDir, "control.sock")
	sockReady := false
	for i := 0; i < 30; i++ {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			sockReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sockReady {
		t.Fatalf("control socket %s failed to become ready. Stderr: %s", sockPath, serveErr.String())
	}

	// 步骤 3: 核心验证！同 config 不同 runtime-dir 的第二个 daemon 启动尝试必须被拒绝
	otherRuntimeDir := filepath.Join(tempDir, "run_other")
	_ = os.MkdirAll(otherRuntimeDir, 0o700)
	secondServeCmd := exec.Command(binPath, "serve", "--config", configPath, "--runtime-dir", otherRuntimeDir)
	var secondErr bytes.Buffer
	secondServeCmd.Stderr = &secondErr
	if err := secondServeCmd.Run(); err == nil {
		t.Fatalf("expected second daemon on same config to be rejected by file lock, but succeeded")
	}
	t.Logf("Second daemon rejected as expected by config lock: %s", strings.TrimSpace(secondErr.String()))

	// 步骤 4: 从外部执行真实 CLI 命令 herdrx status --json 验证 IPC 通信
	{
		statusCmd := exec.Command(binPath, "status", "--json", "--runtime-dir", runtimeDir)
		out, err := statusCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("status command failed: %v, out: %s", err, string(out))
		}
		var statusResp map[string]any
		if err := json.Unmarshal(out, &statusResp); err != nil {
			t.Fatalf("unmarshal status json: %v (raw: %s)", err, string(out))
		}
		if statusResp["daemon_running"] != true {
			t.Fatalf("expected daemon_running=true")
		}
		daemonObj, ok := statusResp["daemon"].(map[string]any)
		if !ok || int(daemonObj["pid"].(float64)) != daemonPid {
			t.Fatalf("pid mismatch in status: %+v", statusResp)
		}
		if daemonObj["version"] != Version {
			t.Fatalf("version mismatch: %v", daemonObj["version"])
		}
	}

	// 步骤 5: 核心验证！Leader 复现的致命 Bug：在 daemon 运行期间通过 IPC 执行更新！
	// 验证运行中的 daemon 接受 POST /unpair 并成功执行 RMW 事务（杜绝重入死锁与 HTTP 500！）
	{
		unpairCmd := exec.Command(binPath, "unpair", "--config", configPath, "--runtime-dir", runtimeDir)
		var unpairOut, unpairErr bytes.Buffer
		unpairCmd.Stdout = &unpairOut
		unpairCmd.Stderr = &unpairErr
		if err := unpairCmd.Run(); err != nil {
			t.Fatalf("IPC unpair during active daemon run failed (Leader bug reproduction): %v, stderr: %s", err, unpairErr.String())
		}
		if !strings.Contains(unpairOut.String(), "revoked via active daemon IPC") {
			t.Fatalf("expected unpair via active daemon IPC, got: %s", unpairOut.String())
		}
		t.Logf("IPC unpair transaction succeeded during active daemon run!")

		// 验证之后再次查询 status，配置已实时生效为 revoked
		statusCmd := exec.Command(binPath, "status", "--json", "--runtime-dir", runtimeDir)
		out, _ := statusCmd.CombinedOutput()
		var statusResp map[string]any
		_ = json.Unmarshal(out, &statusResp)
		daemonObj := statusResp["daemon"].(map[string]any)
		if daemonObj["revoked"] != true || daemonObj["paired"] != false {
			t.Fatalf("daemon status did not reflect unpair transaction: %+v", daemonObj)
		}
	}

	// 步骤 6: 优雅退出并验证 control.sock 彻底消失
	if err := serveCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	waitErr := serveCmd.Wait()
	t.Logf("Subprocess exited gracefully: %v", waitErr)

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("control socket file %s must be cleanly deleted upon server shutdown", sockPath)
	}

	// 步骤 7: 核心断言：停止 herdrx serve 绝不影响独立运行的 Mock Herdr 进程
	conn, dialErr := net.DialTimeout("unix", mockHerdrSockPath, 500*time.Millisecond)
	if dialErr != nil {
		t.Fatalf("independent herdr service must NOT be terminated by herdrx exit: %v", dialErr)
	}
	testMsg := []byte("ping-herdr")
	_, _ = conn.Write(testMsg)
	echoBuf := make([]byte, len(testMsg))
	_, _ = conn.Read(echoBuf)
	conn.Close()
	if !bytes.Equal(echoBuf, testMsg) {
		t.Fatalf("independent herdr service echo failed")
	}

	_ = stateDir
}
