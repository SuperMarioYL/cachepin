package session

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

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

// sessionN builds a 2-message seed whose first-user message varies, so each
// call yields a distinct session id. It returns the messages and their id.
func sessionN(i int) ([]openai.Message, string) {
	msgs := []openai.Message{
		msg("system", "you are a coding agent"),
		msg("user", fmt.Sprintf("unrelated conversation %d", i)),
	}
	return msgs, SessionID(msgs)
}

// TestTrackerEvictsOldestAtCap covers fix-unbounded-session-map-growth: past the
// max-sessions cap the least-recently-used sessions are evicted, so the map
// stays bounded instead of leaking one entry per conversation forever.
func TestTrackerEvictsOldestAtCap(t *testing.T) {
	tr := NewTrackerWithMax(3)
	var ids []string
	for i := 0; i < 5; i++ {
		msgs, id := sessionN(i)
		ids = append(ids, id)
		tr.Observe(msgs)
	}

	if got := tr.Len(); got != 3 {
		t.Errorf("after 5 observes under cap 3, Len = %d, want 3", got)
	}

	// Insertion order is also LRU order when nothing is re-touched, so the two
	// oldest (first inserted) are evicted and the three most recent survive.
	for i, id := range ids {
		got := tr.Canonical(id)
		switch {
		case i < 2:
			if got != nil {
				t.Errorf("session %d (oldest) should have been evicted, Canonical returned %d msgs", i, len(got))
			}
		default:
			if got == nil {
				t.Errorf("session %d (recent) should have survived, Canonical returned nil", i)
			}
		}
	}
}

// TestTrackerLRUTouchKeepsRecentUse verifies eviction is true LRU — re-observing
// an old session marks it recently used, so a later overflow evicts the
// genuinely-idle session, not the one that was just touched.
func TestTrackerLRUTouchKeepsRecentUse(t *testing.T) {
	tr := NewTrackerWithMax(3)

	m0, id0 := sessionN(0)
	m1, id1 := sessionN(1)
	m2, id2 := sessionN(2)
	tr.Observe(m0)
	tr.Observe(m1)
	tr.Observe(m2)

	// Touch session 0 — it becomes most-recently-used; session 1 is now oldest.
	tr.Observe(m0)

	m3, id3 := sessionN(3)
	tr.Observe(m3) // overflow by one -> evict the LRU victim (id1)

	if tr.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (capped)", tr.Len())
	}
	if tr.Canonical(id1) != nil {
		t.Error("session 1 should have been evicted as the LRU victim, but survived")
	}
	for _, id := range []string{id0, id2, id3} {
		if tr.Canonical(id) == nil {
			t.Errorf("session should have survived but was evicted: %s", id)
		}
	}
}

// TestTrackerUnboundedWhenCapDisabled confirms a non-positive cap means no
// eviction (the opt-out path used by tests and short-lived processes).
func TestTrackerUnboundedWhenCapDisabled(t *testing.T) {
	tr := NewTrackerWithMax(0)
	for i := 0; i < DefaultMaxSessions+5; i++ {
		m, _ := sessionN(i)
		tr.Observe(m)
	}
	if got := tr.Len(); got != DefaultMaxSessions+5 {
		t.Errorf("unbounded tracker Len = %d, want %d (no eviction)", got, DefaultMaxSessions+5)
	}
}

// TestTrackerDefaultCapMatchesConstant confirms NewTracker wires the documented
// default, so the shipped proxy bounds memory out of the box.
func TestTrackerDefaultCapMatchesConstant(t *testing.T) {
	tr := NewTracker()
	if tr.maxSessions != DefaultMaxSessions {
		t.Errorf("NewTracker maxSessions = %d, want %d", tr.maxSessions, DefaultMaxSessions)
	}
}

// TestTrackerIdleTTLEvictsQuietSession covers m5-idle-ttl-eviction: under an
// idle TTL, a session that has gone quiet is evicted on the next Observe even
// though no new session arrives to push it out under the LRU count cap. Without
// TTL, an unbounded-count tracker would keep the idle session pinned forever.
func TestTrackerIdleTTLEvictsQuietSession(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0).UTC()
	tr := NewTrackerWithMaxTTL(0, 5*time.Minute) // unbounded count, 5m idle TTL
	tr.now = func() time.Time { return clock }

	m0, id0 := sessionN(0)
	tr.Observe(m0) // observed at clock=t0
	if tr.Canonical(id0) == nil {
		t.Fatal("session 0 should be live right after Observe")
	}

	// Advance the clock past the TTL and observe a DIFFERENT session. The idle
	// expiry runs on the next evictIfNeeded (triggered by the new Observe) and
	// should drop session 0 even though the count cap is unbounded.
	clock = clock.Add(10 * time.Minute)
	m1, _ := sessionN(1)
	tr.Observe(m1)

	if tr.Canonical(id0) != nil {
		t.Errorf("idle session 0 should have been evicted after TTL; Canonical still non-nil")
	}
	if !tr.WasEvicted(id0) {
		t.Errorf("idle-evicted session 0 should be recorded as evicted (for the --pin warning)")
	}
}

// TestTrackerIdleTTLSparedFreshSession confirms the idle path does not evict a
// session whose lastSeen is within the TTL — the back-of-list check stops at
// the first fresh session.
func TestTrackerIdleTTLSparedFreshSession(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0).UTC()
	tr := NewTrackerWithMaxTTL(0, 5*time.Minute)
	tr.now = func() time.Time { return clock }

	m0, id0 := sessionN(0)
	tr.Observe(m0)
	clock = clock.Add(1 * time.Minute) // within TTL
	m1, _ := sessionN(1)
	tr.Observe(m1)

	if tr.Canonical(id0) == nil {
		t.Errorf("fresh session 0 (within TTL) should NOT have been evicted")
	}
}

// TestTrackerWasEvictedClearsOnReObserve covers the --pin warning contract: a
// session flagged evicted stops being "evicted" once it is observed again, so a
// future eviction re-arms the warning instead of a stale flag persisting.
func TestTrackerWasEvictedClearsOnReObserve(t *testing.T) {
	tr := NewTrackerWithMax(2)
	m0, id0 := sessionN(0)
	m1, _ := sessionN(1)
	m2, _ := sessionN(2)
	tr.Observe(m0)
	tr.Observe(m1)
	tr.Observe(m2) // overflow cap 2 -> evict id0

	if tr.Canonical(id0) != nil {
		t.Fatal("session 0 should have been evicted at the cap")
	}
	if !tr.WasEvicted(id0) {
		t.Fatal("session 0 should be flagged evicted after eviction")
	}

	// Re-observe session 0: it is live again, so the stale evicted flag clears.
	tr.Observe(m0)
	if tr.WasEvicted(id0) {
		t.Errorf("session 0 re-observed; WasEvicted should be false (flag cleared)")
	}
}
