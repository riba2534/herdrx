//go:build unix

package herdr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/store"
)

// Run as the real ssh client's ProxyCommand. The independent probe connection
// lets the test observe this child's lifetime without inspecting a reused PID.
func TestOpenSSHProxyProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--openssh-proxy-fixture" || i+2 >= len(os.Args) {
			continue
		}
		if os.Args[i+2] == "detached" {
			if _, err := syscall.Setsid(); err != nil {
				os.Exit(2)
			}
		}
		conn, err := net.Dial("unix", os.Args[i+1])
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(conn, "%d\n", os.Getpid())
		_, _ = io.Copy(io.Discard, conn)
		os.Exit(0)
	}
}

func TestOpenSSHDialReleasesProxyOnCancellation(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cancel", "timeout", "detached"} {
		t.Run(mode, func(t *testing.T) {
			// Keep Unix socket paths short on macOS as well as Linux.
			dir, err := os.MkdirTemp("", "hx-proxy-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			probePath := filepath.Join(dir, "probe")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: probePath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			proxy := quote(testBinary) + " -test.run='^TestOpenSSHProxyProcess$' -- --openssh-proxy-fixture " + quote(probePath) + " " + mode
			wrapper := filepath.Join(dir, "ssh")
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(ssh)+" -F /dev/null -o "+quote("ProxyCommand="+proxy)+" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}

			unrelated := exec.Command("/bin/sh", "-c", "exec sleep 30")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dialTimeout := time.Second
			if mode == "cancel" {
				dialTimeout = 30 * time.Second
			}
			result := make(chan error, 1)
			go func() {
				endpoint, err := DialOpenSSHEndpoint(ctx, wrapper, SSHOptions{Host: store.Host{Hostname: "fixture.example.test"}, Timeout: dialTimeout})
				if endpoint != nil {
					_ = endpoint.Close()
				}
				result <- err
			}()
			_ = listener.SetDeadline(time.Now().Add(5 * time.Second))
			probe, err := listener.AcceptUnix()
			if err != nil {
				t.Fatalf("proxy did not start: %v", err)
			}
			defer probe.Close()
			_ = probe.SetReadDeadline(time.Now().Add(5 * time.Second))
			line, err := bufio.NewReader(probe).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			defer child.Release()
			// Also clean up a deliberately detached fixture, or the old buggy
			// implementation's child if the regression assertion below fails.
			defer child.Kill()
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-result:
				want := context.DeadlineExceeded
				if mode == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("dial error = %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("SSH cancellation is waiting for ProxyCommand's inherited stderr")
			}
			if mode != "detached" {
				_ = probe.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := probe.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatalf("proxy survived SSH cancellation: %v", err)
				}
			}
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("cancellation affected an unrelated process: %v", err)
			}
		})
	}
}
