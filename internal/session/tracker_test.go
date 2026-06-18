package session

import (
	"encoding/json"
	"testing"

	"github.com/SuperMarioYL/cachepin/internal/openai"
)

func msg(role, content string) openai.Message {
	b, _ := json.Marshal(content)
	return openai.Message{Role: role, Content: json.RawMessage(b)}
}

// seed is a 2-message conversation start (system + first user message), which
// anchors the session id.
func seed() []openai.Message {
	return []openai.Message{
		msg("system", "you are a coding agent"),
		msg("user", "refactor the proxy package"),
	}
}

func TestObserveAppendOnlyPreservesPrefix(t *testing.T) {
	tr := NewTracker()
	history := seed()

	// First turn establishes the canonical history.
	first := tr.Observe(history)
	if first.TurnNum != 1 {
		t.Fatalf("first turn num = %d, want 1", first.TurnNum)
	}
	if first.Mutated {
		t.Errorf("first turn unexpectedly flagged as mutated")
	}
	if first.PreservedPrefixPct != 100 {
		t.Errorf("first turn preserved %.0f%%, want 100", first.PreservedPrefixPct)
	}
	if first.ReprocessedTokens != 0 {
		t.Errorf("first turn reprocessed %d tokens, want 0", first.ReprocessedTokens)
	}

	// Append-only growth across several turns: nothing should be reprocessed.
	for i := 2; i <= 5; i++ {
		history = append(history,
			msg("assistant", "here is a change for turn"),
			msg("user", "looks good, next step please"),
		)
		turn := tr.Observe(history)
		if turn.TurnNum != i {
			t.Errorf("turn num = %d, want %d", turn.TurnNum, i)
		}
		if turn.Mutated {
			t.Errorf("turn %d flagged mutated on a pure append", i)
		}
		if turn.PreservedPrefixPct != 100 {
			t.Errorf("turn %d preserved %.0f%%, want 100", i, turn.PreservedPrefixPct)
		}
		if turn.ReprocessedTokens != 0 {
			t.Errorf("turn %d reprocessed %d tokens on a pure append, want 0", i, turn.ReprocessedTokens)
		}
		if turn.LCP != turn.PrevLen {
			t.Errorf("turn %d LCP %d != PrevLen %d on append-only", i, turn.LCP, turn.PrevLen)
		}
	}
}

func TestObserveDetectsMutation(t *testing.T) {
	tr := NewTracker()

	history := append(seed(),
		msg("assistant", "long tool output that the harness will later rewrite"),
		msg("user", "thanks"),
	)
	tr.Observe(history) // establish a 4-message canonical history

	// The harness rewrites message at index 2 (the assistant turn) and appends a
	// new user message — exactly the cache-busting pattern CachePin targets.
	mutated := cloneMessages(history)
	mutated[2] = msg("assistant", "DIFFERENT re-rendered tool output")
	mutated = append(mutated, msg("user", "another question"))

	turn := tr.Observe(mutated)
	if !turn.Mutated {
		t.Fatal("mutation at msg[2] not detected")
	}
	if turn.MutationIndex != 2 {
		t.Errorf("mutation index = %d, want 2", turn.MutationIndex)
	}
	if turn.LCP != 2 {
		t.Errorf("LCP = %d, want 2", turn.LCP)
	}
	if turn.PreservedPrefixPct != 50 {
		t.Errorf("preserved %.0f%%, want 50 (2 of 4 messages)", turn.PreservedPrefixPct)
	}
	if turn.ReprocessedTokens <= 0 {
		t.Errorf("reprocessed %d tokens, want > 0 after a mutation", turn.ReprocessedTokens)
	}
}

func TestSessionIDStableAndDistinct(t *testing.T) {
	a := seed()
	aLater := append(seed(), msg("assistant", "x"), msg("user", "y"))
	if SessionID(a) != SessionID(aLater) {
		t.Error("session id changed as the same conversation grew")
	}

	b := []openai.Message{
		msg("system", "you are a coding agent"),
		msg("user", "a completely different first question"),
	}
	if SessionID(a) == SessionID(b) {
		t.Error("different conversations produced the same session id")
	}
}

func TestSeparateSessionsTrackedIndependently(t *testing.T) {
	tr := NewTracker()
	s1 := seed()
	s2 := []openai.Message{
		msg("system", "you are a coding agent"),
		msg("user", "unrelated conversation"),
	}
	if got := tr.Observe(s1); got.TurnNum != 1 {
		t.Errorf("session 1 first turn = %d, want 1", got.TurnNum)
	}
	if got := tr.Observe(s2); got.TurnNum != 1 {
		t.Errorf("session 2 first turn = %d, want 1 (independent session)", got.TurnNum)
	}
	if got := tr.Observe(s1); got.TurnNum != 2 {
		t.Errorf("session 1 second turn = %d, want 2", got.TurnNum)
	}
}

func TestLongestCommonPrefix(t *testing.T) {
	a := []openai.Message{msg("system", "s"), msg("user", "u"), msg("assistant", "a")}
	b := []openai.Message{msg("system", "s"), msg("user", "u"), msg("assistant", "DIFFERENT")}
	if got := LongestCommonPrefix(a, b); got != 2 {
		t.Errorf("LCP = %d, want 2", got)
	}
	if got := LongestCommonPrefix(a, a); got != 3 {
		t.Errorf("LCP of identical = %d, want 3", got)
	}
	if got := LongestCommonPrefix(a, nil); got != 0 {
		t.Errorf("LCP with empty = %d, want 0", got)
	}
}

func TestEstimateTokensGrowsWithContent(t *testing.T) {
	small := []openai.Message{msg("user", "hi")}
	big := []openai.Message{msg("user", "hi there, this is a much longer message with more content")}
	if EstimateTokens(small) >= EstimateTokens(big) {
		t.Errorf("estimate did not grow with content: small=%d big=%d",
			EstimateTokens(small), EstimateTokens(big))
	}
	if EstimateTokens(nil) != 0 {
		t.Errorf("estimate of empty = %d, want 0", EstimateTokens(nil))
	}
}
