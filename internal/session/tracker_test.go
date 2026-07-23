package session

import (
	"encoding/json"
	"fmt"
	"sync"
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

// TestTrackerIdleTTLEvictsOnExistingSessionReobserve is the regression test for
// fix-idle-ttl-skips-existing-session-observe: the v0.4.0 path only swept idle
// sessions inside the NEW-session branch of Observe, so re-observing an EXISTING
// (returning) session — the common sparse long-uptime deployment where only
// sticky sessions come back — never triggered evictIfNeeded and idle
// back-of-list sessions leaked forever. Under `--max-sessions 0 --idle-ttl 10m`
// (the exact unbounded-count config m5 targets) this was an unbounded memory
// leak, and the shipped TestTrackerIdleTTLEvictsQuietSession only covered the
// new-session trigger (it observed a brand-new session to drive eviction). The
// v0.5.0 fix hoists evictIfNeeded out of the `if s == nil` branch so it runs on
// every Observe; the just-touched session is at the front with lastSeen=now so
// it is never a victim, but idle sessions expire on the next Observe of any
// session.
func TestTrackerIdleTTLEvictsOnExistingSessionReobserve(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0).UTC()
	tr := NewTrackerWithMaxTTL(0, 5*time.Minute) // unbounded count, 5m idle TTL
	tr.now = func() time.Time { return clock }

	m0, id0 := sessionN(0)
	m1, id1 := sessionN(1)
	tr.Observe(m0) // t0: session 0 created (front)
	tr.Observe(m1) // t0: session 1 created (front; session 0 now back of list)

	// Both sessions go idle past the TTL with NO new session arriving — only a
	// returning (existing) session is re-observed, which is the else branch the
	// v0.4.0 sweep skipped.
	clock = clock.Add(10 * time.Minute)
	tr.Observe(m0) // re-observe EXISTING session 0 — must trigger idle eviction

	// Session 1 was idle > TTL and at the back; it must be evicted on this
	// Observe even though no NEW session arrived. Before the fix it leaked.
	if tr.Canonical(id1) != nil {
		t.Errorf("idle session 1 should have been evicted when existing session 0 was re-observed; still live (leak)")
	}
	if !tr.WasEvicted(id1) {
		t.Errorf("idle-evicted session 1 should be recorded as evicted (for the --pin warning)")
	}
	// The just-touched session 0 is fresh (lastSeen=now) and at the front — it
	// must survive (eviction never touches the freshest session).
	if tr.Canonical(id0) == nil {
		t.Errorf("just-observed session 0 should NOT have been evicted (it is fresh at the front)")
	}
}

// appendReconcile mirrors pin.Reconcile's LCP contract (canonical[:lcp] +
// incoming[lcp:], taking the preserved prefix from the full canonical) without
// importing pin — the session package cannot import pin (pin imports session),
// so the ReconcileAndObserve atomicity test uses this local stand-in to
// exercise the tracker's critical-section contract directly.
func appendReconcile(prior, incoming []openai.Message) ([]openai.Message, bool) {
	lcp := LongestCommonPrefix(prior, incoming)
	if lcp == len(prior) {
		return incoming, false
	}
	tail := incoming[lcp:]
	out := make([]openai.Message, 0, len(prior)+len(tail))
	out = append(out, prior...)
	out = append(out, tail...)
	return out, true
}

// TestReconcileAndObserveSameSessionNoTurnLoss is the regression test for
// fix-same-session-pin-reconcile-race: the pre-v0.5.0 --pin path read Canonical
// (lock-clone-unlock), ran the reconcile unlocked, then Observe (re-lock-store)
// — a TOCTOU window where two concurrent same-session goroutines each read the
// same stale prior, reconciled against it, and the later Observe overwrote the
// earlier, dropping a turn from canonical so the next Reconcile used the wrong
// ground truth. ReconcileAndObserve holds t.mu across all three, so every
// appended turn survives. Run under `go test -race -run ReconcileAndObserve`.
func TestReconcileAndObserveSameSessionNoTurnLoss(t *testing.T) {
	tr := NewTracker()
	seed := []openai.Message{msg("system", "s"), msg("user", "u")}
	sid := SessionID(seed)

	// Each goroutine appends a UNIQUE assistant turn to the SAME session via the
	// pin reconcile path (a closure over appendReconcile, exactly as the proxy
	// passes a closure over pin.Reconcile).
	const n = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximize interleaving
			incoming := append(append([]openai.Message{}, seed...),
				msg("assistant", fmt.Sprintf("turn-%d", i)))
			tr.ReconcileAndObserve(sid, incoming, func(prior []openai.Message) ([]openai.Message, bool) {
				return appendReconcile(prior, incoming)
			})
		}(i)
	}
	close(start)
	wg.Wait()

	got := tr.Canonical(sid)
	// Seed (2) + one appended turn per goroutine. Turn loss from the race would
	// leave the canonical short.
	if len(got) != 2+n {
		t.Fatalf("canonical has %d messages, want %d (seed + %d turns; turns were lost to the same-session race)", len(got), 2, n)
	}
	// Every unique turn must survive — no overwrite/loss.
	seen := make(map[string]bool, n)
	for _, m := range got[2:] {
		var s string
		_ = json.Unmarshal(m.Content, &s)
		seen[s] = true
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("turn-%d", i)
		if !seen[want] {
			t.Errorf("turn %q lost from canonical (same-session race)", want)
		}
	}
}

// TestReconcileAndObserveSurfacesEvictionCoverageLoss confirms the
// ReconcileAndObserve path still computes PinCoverageLost (prior was nil
// because the session was evicted) inside the critical section, so the v0.4.0
// silent-green-while-reprocessing warning survives the v0.5.0 atomicity fix.
func TestReconcileAndObserveSurfacesEvictionCoverageLoss(t *testing.T) {
	tr := NewTrackerWithMax(1) // cap 1 so a second session evicts the first
	m0, id0 := sessionN(0)
	m1, _ := sessionN(1)
	tr.Observe(m0) // session 0 live
	tr.Observe(m1) // overflow cap 1 -> evict session 0
	if !tr.WasEvicted(id0) {
		t.Fatal("precondition: session 0 should be flagged evicted")
	}

	// Session 0 returns under --pin with a mutated tail. Reconcile(nil, …) is a
	// no-op, so coverage was lost; ReconcileAndObserve must report it.
	incoming := append(append([]openai.Message{}, m0...), msg("user", "new-after-eviction"))
	turn := tr.ReconcileAndObserve(id0, incoming, func(prior []openai.Message) ([]openai.Message, bool) {
		return appendReconcile(prior, incoming)
	})
	if !turn.PinCoverageLost {
		t.Errorf("ReconcileAndObserve should set PinCoverageLost when prior was nil because the session was evicted")
	}
}
