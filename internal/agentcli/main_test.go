package agentcli

import (
	"os"
	"testing"
)

// TestMain keeps every `herdrx serve` started by these tests from launching a
// real Herdr server: subprocesses inherit this variable through os.Environ().
func TestMain(m *testing.M) {
	_ = os.Setenv(herdrAutostartEnv, "0")
	os.Exit(m.Run())
}
