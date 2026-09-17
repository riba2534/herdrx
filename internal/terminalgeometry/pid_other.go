//go:build !linux && !darwin

package terminalgeometry

import "fmt"

func ReadPID(pid int) (Geometry, error) {
	return Geometry{}, fmt.Errorf("preserving terminal geometry currently requires a Linux or macOS Herdr host")
}
