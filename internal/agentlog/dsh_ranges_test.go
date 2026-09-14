package agentlog

import "testing"

func TestDSHSourceRangesAreValidatedWithoutExpansion(t *testing.T) {
	// A tiny row can name trillions of events. Admission must stay proportional
	// to the encoded row, never allocate or iterate once per referenced event.
	const seq = int64(1 << 50)
	for _, value := range []any{
		[]any{[]any{float64(0), float64(seq - 1)}},
		[]any{[]any{float64(0), float64(seq - 3)}, float64(seq - 2), float64(seq - 1)},
	} {
		if detail := validateSourceEventSeqs(value, seq); detail != "" {
			t.Fatal(detail)
		}
	}
	if detail := validateSourceEventSeqs([]any{[]any{float64(0), float64(seq - 1)}, float64(seq - 2)}, seq); detail == "" {
		t.Fatal("overlapping range accepted")
	}
}
