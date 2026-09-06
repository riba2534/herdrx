//go:build !linux

package terminalgeometry

import "fmt"

func ReadPID(pid int) (Geometry, error) {
	return Geometry{}, fmt.Errorf("preserving terminal geometry currently requires a Linux Herdr host")
}
