package agentcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/riba2534/herdrx/internal/updater"
	"golang.org/x/crypto/ssh"
)

// Readiness deliberately has no internet or Herdr liveness dependency. A
// network outage must not cause an executable/identity rollback.
type Readiness struct {
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	StateVersion    int    `json:"state_version"`
	ConfigPath      string `json:"config_path,omitempty"`
	Identity        string `json:"identity,omitempty"`
	Epoch           int64  `json:"epoch"`
	Revoked         bool   `json:"revoked"`
	PID             int    `json:"pid"`
}

func readiness(cfg *Config, path string) (Readiness, error) {
	result := Readiness{Version: Version, ProtocolVersion: updater.ProtocolVersion, StateVersion: updater.StateVersion, PID: os.Getpid()}
	if cfg == nil {
		return result, nil
	}
	if cfg.Version != updater.StateVersion || cfg.Node.Private.IsZero() {
		return result, fmt.Errorf("identity state is incomplete or incompatible")
	}
	if _, err := ssh.ParsePrivateKey([]byte(cfg.SSHHostPrivate)); err != nil {
		return result, fmt.Errorf("load SSH host identity: %w", err)
	}
	// Fingerprint all stable identity material, never expose the actual keys.
	raw, err := json.Marshal(struct {
		Node       any
		SSH, Setup string
		PSK        any
	}{cfg.Node.Private, cfg.SSHHostPrivate, cfg.SetupID, cfg.FormalPSK()})
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(raw)
	result.ConfigPath = path
	result.Identity = hex.EncodeToString(sum[:])
	result.Epoch = cfg.Binding.Epoch
	result.Revoked = cfg.Revoked || cfg.Binding.Status == "revoked"
	return result, nil
}

func runSelfTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("self-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "read an existing identity without changing it")
	asJSON := fs.Bool("json", false, "output compatibility and identity fingerprint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected self-test arguments")
		return 2
	}
	var cfg *Config
	if *path != "" {
		absolute, err := filepath.Abs(*path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*path = absolute
		loaded, err := SafeLoadConfig(absolute)
		if err != nil {
			fmt.Fprintf(stderr, "self-test: %v\n", err)
			return 1
		}
		cfg = &loaded
	}
	result, err := readiness(cfg, *path)
	if err != nil {
		fmt.Fprintf(stderr, "self-test: %v\n", err)
		return 1
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "herdrx %s self-test passed (protocol %d, state %d)\n", result.Version, result.ProtocolVersion, result.StateVersion)
	}
	return 0
}

func (s *IPCServer) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.checkConfigPathHeader(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := s.store.Snapshot()
	if err != nil {
		http.Error(w, "identity is not readable", http.StatusServiceUnavailable)
		return
	}
	result, err := readiness(&cfg, s.store.ConfigPath())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
