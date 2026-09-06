package agent

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/terminalgeometry"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)
var safePairToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
var validImageExt = regexp.MustCompile(`^(png|jpe?g|webp|gif)$`)

type CommandSpec struct {
	Internal string
	Args     []string
}
type commandSpec = CommandSpec

func ParseCommand(raw string) (CommandSpec, error) {
	if raw == `sh -c 'printf "%s\n%s\n" "$HOME" "${XDG_CONFIG_HOME:-}"'` {
		return CommandSpec{Internal: "config-path"}, nil
	}
	if raw == "uname -sm" {
		return CommandSpec{Internal: "uname"}, nil
	}
	if strings.ContainsAny(raw, "\n\r\x00;&|`$<>") {
		return CommandSpec{}, fmt.Errorf("command contains forbidden shell syntax")
	}
	fields := strings.Fields(raw)
	if len(fields) == 2 && fields[0] == "herdrx-terminal-geometry" {
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 1 || pid > 1<<30 || strconv.Itoa(pid) != fields[1] {
			return CommandSpec{}, fmt.Errorf("invalid terminal process id")
		}
		return CommandSpec{Internal: "terminal-geometry", Args: fields[1:]}, nil
	}
	if len(fields) == 2 && fields[0] == "confirm-pair" && safePairToken.MatchString(fields[1]) {
		return CommandSpec{Internal: "confirm-pair", Args: fields[1:]}, nil
	}
	if len(fields) == 2 && fields[0] == "herdrx-stage-image" && validImageExt.MatchString(fields[1]) {
		return CommandSpec{Internal: "stage-image", Args: fields[1:]}, nil
	}
	if len(fields) < 8 || fields[0] != "herdr" {
		return CommandSpec{}, fmt.Errorf("command is not allowed")
	}
	index := 1
	if fields[index] == "--session" {
		if len(fields) < 10 || !safeID.MatchString(fields[index+1]) {
			return CommandSpec{}, fmt.Errorf("invalid herdr session")
		}
		index += 2
	}
	if len(fields) < index+7 || fields[index] != "terminal" || fields[index+1] != "session" || (fields[index+2] != "observe" && fields[index+2] != "control") || !safeID.MatchString(fields[index+3]) {
		return CommandSpec{}, fmt.Errorf("only terminal session observe/control is allowed")
	}
	index += 4
	if index < len(fields) && fields[index] == "--takeover" {
		index++
	}
	if len(fields) != index+4 || fields[index] != "--cols" || fields[index+2] != "--rows" {
		return CommandSpec{}, fmt.Errorf("terminal dimensions are required")
	}
	cols, colsErr := strconv.Atoi(fields[index+1])
	rows, rowsErr := strconv.Atoi(fields[index+3])
	if colsErr != nil || rowsErr != nil || cols < 10 || cols > 1000 || rows < 3 || rows > 500 {
		return CommandSpec{}, fmt.Errorf("invalid terminal dimensions")
	}
	return CommandSpec{Args: fields}, nil
}

func parseCommand(raw string) (commandSpec, error) {
	return ParseCommand(raw)
}

func ConfigPathOutput() string {
	return configPathOutput()
}

func WriteTerminalGeometry(w io.Writer, pidText string) error {
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return fmt.Errorf("invalid terminal process id")
	}
	geometry, err := terminalgeometry.ReadPID(pid)
	if err != nil {
		return err
	}
	return json.NewEncoder(w).Encode(geometry)
}

func AllowedSocket(path string) bool {
	return allowedSocket(path)
}

func ExecutableCommand(spec CommandSpec, herdrBin string) *exec.Cmd {
	return executableCommand(spec, herdrBin)
}

func configPathOutput() string {
	home, _ := os.UserHomeDir()
	return home + "\n" + os.Getenv("XDG_CONFIG_HOME") + "\n"
}

func allowedSocket(path string) bool {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return false
	}
	root := filepath.Join(configDir, "herdr")
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 1 {
		return parts[0] == "herdr.sock" || parts[0] == "herdr-client.sock"
	}
	return len(parts) == 3 && parts[0] == "sessions" && safeID.MatchString(parts[1]) && (parts[2] == "herdr.sock" || parts[2] == "herdr-client.sock")
}

func confirmPair(configPath, token string) error {
	config, err := Load(configPath)
	if err != nil {
		return err
	}
	got := base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))
	if config.PairTokenHash == "" || len(got) != len(config.PairTokenHash) || subtle.ConstantTimeCompare([]byte(got), []byte(config.PairTokenHash)) != 1 || time.Now().After(config.PairExpiresAt) {
		return fmt.Errorf("pairing token is invalid or expired")
	}
	config.PairTokenHash = ""
	config.PairExpiresAt = time.Time{}
	config.Paired = true
	config.Revoked = false
	return Save(configPath, config)
}

func ConfirmPairForTest(configPath, token string) error {
	return confirmPair(configPath, token)
}

func Unpair(configPath string) error {
	config, err := Load(configPath)
	if err != nil {
		return err
	}
	config.Paired = false
	config.Revoked = true
	config.PairTokenHash = ""
	config.PairExpiresAt = time.Time{}
	return Save(configPath, config)
}

func executableCommand(spec commandSpec, herdrBin string) *exec.Cmd {
	binName := spec.Args[0]
	if binName == "herdr" && herdrBin != "" {
		binName = herdrBin
	}
	return exec.Command(binName, spec.Args[1:]...)
}
