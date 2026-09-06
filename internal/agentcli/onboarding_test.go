package agentcli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupMigrationFailureDoesNotGenerateIdentity(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid legacy config", true: "running legacy service"}[running], func(t *testing.T) {
			// Keep Unix socket paths below the platform's sockaddr limit.
			dir, err := os.MkdirTemp("", "herdrx-onboard-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			legacy := filepath.Join(dir, ".config", "herdrx-agent", "config.json")
			if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte("existing invalid identity"), 0o600); err != nil {
				t.Fatal(err)
			}
			if running {
				sock := filepath.Join(dir, ".local", "state", "herdrx-agent", "control.sock")
				if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", sock)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			herdrSock := filepath.Join(dir, "herdr.sock")
			listener, err := net.Listen("unix", herdrSock)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			bin := filepath.Join(dir, "herdr")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			env := Environment{HomeDir: dir, ConfigDir: filepath.Join(dir, "new-config"), HerdrBin: bin, CommandRunner: func(_ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "--version":
					return []byte("herdr 0.8.2"), nil
				case "api":
					return []byte(`{"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"session.snapshot"}}}]}}}`), nil
				default:
					return []byte("status: running\nsocket: " + herdrSock), nil
				}
			}}
			var out, stderr bytes.Buffer
			if code := Run([]string{"setup", "--skip-service"}, &out, &stderr, env); code != 1 || !strings.Contains(stderr.String(), "configuration migration failed") {
				t.Fatalf("migration failure was hidden: code=%d, %s", code, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(env.ConfigDir, "config.json")); !os.IsNotExist(err) {
				t.Fatal("new identity created after migration failure")
			}
			data, err := os.ReadFile(legacy)
			if err != nil || string(data) != "existing invalid identity" {
				t.Fatal("legacy identity modified")
			}
		})
	}
}
