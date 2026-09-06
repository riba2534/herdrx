package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

func InstallService(configPath string) error {
	if _, err := Load(configPath); err != nil {
		return fmt.Errorf("initialize the agent before installing its service: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	target := filepath.Join(home, ".local", "bin", "herdrx-agent")
	if err := copyExecutable(source, target); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		return installSystemdUser(home, target, configPath)
	case "darwin":
		return installLaunchAgent(home, target, configPath)
	default:
		return fmt.Errorf("service installation is not supported on %s; run `herdrx-agent run` manually", runtime.GOOS)
	}
}

func UninstallService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "herdrx-agent.service").Run()
		path := filepath.Join(home, ".config", "systemd", "user", "herdrx-agent.service")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil
	case "darwin":
		label := "com.riba2534.herdrx-agent"
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
		path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
}

func copyExecutable(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".herdrx-agent-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

func installSystemdUser(home, binary, configPath string) error {
	directory := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	unit := `[Unit]
Description=herdrx tailcat agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(binary) + ` run --config ` + systemdQuote(configPath) + `
Restart=on-failure
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=default.target
`
	if err := atomicWrite(filepath.Join(directory, "herdrx-agent.service"), []byte(unit), 0o600); err != nil {
		return err
	}
	if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s", bytes.TrimSpace(output))
	}
	if output, err := exec.Command("systemctl", "--user", "enable", "--now", "herdrx-agent.service").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %s", bytes.TrimSpace(output))
	}
	return nil
}

func installLaunchAgent(home, binary, configPath string) error {
	directory := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	label := "com.riba2534.herdrx-agent"
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>` + xmlEscape(binary) + `</string><string>run</string><string>--config</string><string>` + xmlEscape(configPath) + `</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>` + xmlEscape(filepath.Join(home, "Library", "Logs", "herdrx-agent.log")) + `</string>
<key>StandardErrorPath</key><string>` + xmlEscape(filepath.Join(home, "Library", "Logs", "herdrx-agent.log")) + `</string>
</dict></plist>
`
	path := filepath.Join(directory, label+".plist")
	if err := atomicWrite(path, []byte(plist), 0o600); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
	if output, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %s", bytes.TrimSpace(output))
	}
	return nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func systemdQuote(value string) string { return strconv.Quote(value) }
func xmlEscape(value string) string {
	value = bytes.NewBufferString(value).String()
	value = string(bytes.ReplaceAll([]byte(value), []byte("&"), []byte("&amp;")))
	value = string(bytes.ReplaceAll([]byte(value), []byte("<"), []byte("&lt;")))
	return string(bytes.ReplaceAll([]byte(value), []byte(">"), []byte("&gt;")))
}
