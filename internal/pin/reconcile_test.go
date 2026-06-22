package pin

import (
	"encoding/json"
	"testing"

	"github.com/SuperMarioYL/cachepin/internal/openai"
	"github.com/SuperMarioYL/cachepin/internal/session"
)

func msg(role, content string) openai.Message {
	b, _ := json.Marshal(content)
	return openai.Message{Role: role, Content: json.RawMessage(b)}
}

func contents(msgs []openai.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		var s string
		_ = json.Unmarshal(m.Content, &s)
		out[i] = s
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReconcileAppendOnlyForwardsUnchanged: when incoming already extends
// canonical, nothing is rewritten.
func TestReconcileAppendOnlyForwardsUnchanged(t *testing.T) {
	canonical := []openai.Message{msg("system", "s"), msg("user", "u1")}
	incoming := []openai.Message{msg("system", "s"), msg("user", "u1"), msg("assistant", "a1")}

	got, changed := Reconcile(canonical, incoming)
	if changed {
		t.Errorf("changed = true, want false for pure append")
	}
	if !eq(contents(got), []string{"s", "u1", "a1"}) {
		t.Errorf("forwarded = %v, want [s u1 a1]", contents(got))
	}
}

// TestReconcileInPlaceEditPreservesCanonical: a count-preserving in-place edit
// (what bench's mutate does) keeps the canonical prefix and re-attaches the true
// tail. This case passed under the old last-N slice too.
func TestReconcileInPlaceEditPreservesCanonical(t *testing.T) {
	canonical := []openai.Message{
		msg("system", "s"), msg("user", "u1"), msg("assistant", "a1"), msg("user", "u2"),
	}
	// Harness rewrites a1 in place and appends one new message.
	incoming := []openai.Message{
		msg("system", "s"), msg("user", "u1"), msg("assistant", "a1-REWRITTEN"), msg("user", "u2"),
		msg("assistant", "a2"),
	}

	got, changed := Reconcile(canonical, incoming)
	if !changed {
		t.Fatal("changed = false, want true (mutation present)")
	}
	// LCP is 2 (s, u1). Reconciled = canonical + incoming[2:] =
	// [s u1 a1 u2] + [a1-REWRITTEN u2 a2].
	want := []string{"s", "u1", "a1", "u2", "a1-REWRITTEN", "u2", "a2"}
	if !eq(contents(got), want) {
		t.Errorf("reconciled = %v, want %v", contents(got), want)
	}
}

// TestReconcileDropOldAppendNew is the regression test for
// fix-reconcile-wrong-tail-slice: when the harness DROPS an earlier message and
// appends new ones (context compaction), the message count stays the same or
// shrinks, so the old last-N slice (newCount = len(incoming)-len(canonical))
// undercounts the new tail and silently drops genuinely-new turns. The correct
// LCP reconstruction must preserve every message past the divergence boundary.
func TestReconcileDropOldAppendNew(t *testing.T) {
	canonical := []openai.Message{
		msg("system", "s"),
		msg("user", "u1"),
		msg("assistant", "a1"),
		msg("user", "u2"),
		msg("assistant", "a2"),
	}
	// Compaction: drop a1 (an old tool result), keep the rest shifted, and append
	// two genuinely-new turns. Message count: 5 -> 6 (newCount would be 1).
	incoming := []openai.Message{
		msg("system", "s"),
		msg("user", "u1"),
		// a1 dropped here — this is the divergence point.
		msg("user", "u2"),
		msg("assistant", "a2"),
		msg("user", "u3-NEW"),
		msg("assistant", "a3-NEW"),
	}

	got, changed := Reconcile(canonical, incoming)
	if !changed {
		t.Fatal("changed = false, want true (history was rewritten)")
	}

	lcp := session.LongestCommonPrefix(canonical, incoming)
	if lcp != 2 {
		t.Fatalf("test precondition: LCP = %d, want 2 (s, u1)", lcp)
	}

	// Correct contract: canonical + incoming[lcp:] — every post-boundary message
	// from incoming, including both new turns, is preserved.
	want := []string{"s", "u1", "a1", "u2", "a2", "u2", "a2", "u3-NEW", "a3-NEW"}
	if !eq(contents(got), want) {
		t.Errorf("reconciled = %v, want %v", contents(got), want)
	}

	// The crucial guarantee: the genuinely-new turns must survive. The old
	// last-N slice (newCount=1) would have kept only "a3-NEW" and dropped
	// "u3-NEW".
	gotContents := contents(got)
	for _, needed := range []string{"u3-NEW", "a3-NEW"} {
		found := false
		for _, c := range gotContents {
			if c == needed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("reconciled dropped genuinely-new message %q: got %v", needed, gotContents)
		}
	}
}

// TestReconcileEmptyCanonical: first turn against an empty canonical forwards
// unchanged (LCP == len(canonical) == 0).
func TestReconcileEmptyCanonical(t *testing.T) {
	incoming := []openai.Message{msg("system", "s"), msg("user", "u1")}
	got, changed := Reconcile(nil, incoming)
	if changed {
		t.Errorf("changed = true, want false against empty canonical")
	}
	if !eq(contents(got), []string{"s", "u1"}) {
		t.Errorf("forwarded = %v, want [s u1]", contents(got))
	}
}
