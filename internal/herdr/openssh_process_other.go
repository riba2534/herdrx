//go:build !unix

package herdr

import "os/exec"

// System OpenSSH is deployed on Linux and macOS. Other platforms retain
// CommandContext's process cancellation and the portable pipe-drain deadline.
func configureOpenSSHProcess(cmd *exec.Cmd) {}
