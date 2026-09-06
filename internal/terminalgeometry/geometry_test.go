package terminalgeometry

import "testing"

func TestExactTerminalGeometry(t *testing.T) {
	for _, test := range []struct {
		name                      string
		rows, cols, width, height uint16
		invalid                   bool
	}{
		{"character only", 40, 120, 0, 0, false},
		{"pixel cells", 47, 65, 520, 752, false},
		{"one pixel dimension", 47, 65, 520, 0, false},
		{"fractional width", 47, 65, 521, 752, true},
		{"fractional height", 47, 65, 520, 753, true},
		{"saturated pixels", 47, 65, 65535, 752, true},
		{"zero columns", 40, 0, 0, 0, true},
		{"oversize", 501, 120, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, err := FromWinsize(test.rows, test.cols, test.width, test.height)
			if (err != nil) != test.invalid {
				t.Fatalf("geometry=%+v err=%v", g, err)
			}
			if err == nil && (g.Validate() != nil || uint32(g.Cols)*g.CellWidthPx != uint32(test.width) || uint32(g.Rows)*g.CellHeightPx != uint32(test.height)) {
				t.Fatalf("changed pixel geometry: %+v", g)
			}
		})
	}
	if (Geometry{Cols: 120, Rows: 40, CellWidthPx: ^uint32(0)}).Validate() == nil {
		t.Fatal("overflowing pixel size accepted")
	}
}
