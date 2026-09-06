// Package terminalgeometry reads a terminal's existing size without changing it.
package terminalgeometry

import "fmt"

type Geometry struct {
	Cols         uint16 `json:"cols"`
	Rows         uint16 `json:"rows"`
	CellWidthPx  uint32 `json:"cell_width_px"`
	CellHeightPx uint32 `json:"cell_height_px"`
}

func FromWinsize(rows, cols, width, height uint16) (Geometry, error) {
	if cols < 10 || cols > 1000 || rows < 3 || rows > 500 || width == 65535 || height == 65535 || width%cols != 0 || height%rows != 0 {
		return Geometry{}, fmt.Errorf("terminal size cannot be preserved exactly")
	}
	return Geometry{Cols: cols, Rows: rows, CellWidthPx: uint32(width / cols), CellHeightPx: uint32(height / rows)}, nil
}

func (g Geometry) Validate() error {
	if g.Cols < 10 || g.Cols > 1000 || g.Rows < 3 || g.Rows > 500 || uint64(g.Cols)*uint64(g.CellWidthPx) >= 65535 || uint64(g.Rows)*uint64(g.CellHeightPx) >= 65535 {
		return fmt.Errorf("terminal size cannot be preserved exactly")
	}
	return nil
}
