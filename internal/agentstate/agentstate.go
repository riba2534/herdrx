// Package agentstate maps Herdr agent status plus the Web user's read marker
// onto the attention states shown by the Agent overview, push and badges.
//
// Herdr owns the agent lifecycle. "done" in Herdr already means "finished and
// not yet looked at in Herdr"; the Web marker additionally hides a round the
// user has opened in the Web client. Every state change takes a new value from
// Herdr's instance-wide state_change_seq, so a round is seen only when the
// marker equals the pane's current value. The counter restarts at 0 when
// Herdr restarts and pane IDs repeat, so a marker above the current value is a
// previous Herdr run, not a newer round.
package agentstate

// Attention states, ordered by Rank.
const (
	NeedsYou   = "needs_you"   // Herdr reports blocked: a prompt, permission or plan waits for input.
	UnseenDone = "unseen_done" // Finished and not yet opened in the Web client.
	Working    = "working"
	Seen       = "seen" // Finished and opened, or idle.
	Unknown    = "unknown"
)

// Classify returns the attention state. seenSeq is the user's marker for the
// pane, or -1 when the user has never opened it in the Web client.
func Classify(status string, stateChangeSeq, seenSeq int64) string {
	switch status {
	case "blocked":
		return NeedsYou
	case "working":
		return Working
	case "done":
		if seenSeq == stateChangeSeq {
			return Seen
		}
		return UnseenDone
	case "idle":
		return Seen
	default:
		return Unknown
	}
}

// Rank orders attention states for lists: lower comes first.
func Rank(state string) int {
	switch state {
	case NeedsYou:
		return 0
	case UnseenDone:
		return 1
	case Working:
		return 2
	case Seen:
		return 3
	default:
		return 4
	}
}

// NeedsAttention reports whether the state counts toward badges.
func NeedsAttention(state string) bool { return state == NeedsYou || state == UnseenDone }
