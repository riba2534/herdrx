package agentstate

import "testing"

func TestClassify(t *testing.T) {
	for _, test := range []struct {
		status       string
		seq, seenSeq int64
		want         string
	}{
		{"blocked", 4, 9, NeedsYou},
		{"working", 4, -1, Working},
		{"done", 4, -1, UnseenDone},
		{"done", 4, 3, UnseenDone},
		{"done", 4, 4, Seen},
		{"done", 0, 0, Seen},
		// Herdr restarted: the counter began again below the old marker.
		{"done", 4, 9, UnseenDone},
		{"idle", 4, -1, Seen},
		{"", 0, -1, Unknown},
	} {
		if got := Classify(test.status, test.seq, test.seenSeq); got != test.want {
			t.Errorf("Classify(%q, %d, %d) = %q, want %q", test.status, test.seq, test.seenSeq, got, test.want)
		}
	}
	if !(Rank(NeedsYou) < Rank(UnseenDone) && Rank(UnseenDone) < Rank(Working) && Rank(Working) < Rank(Seen) && Rank(Seen) < Rank(Unknown)) {
		t.Fatal("rank order")
	}
	if !NeedsAttention(NeedsYou) || !NeedsAttention(UnseenDone) || NeedsAttention(Working) {
		t.Fatal("attention")
	}
}
