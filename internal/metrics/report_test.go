package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuperMarioYL/cachepin/internal/openai"
	"github.com/SuperMarioYL/cachepin/internal/session"
)

func mj(role, content string) openai.Message {
	b, _ := json.Marshal(content)
	return openai.Message{Role: role, Content: b}
}

// ndjsonFor reports turn through a Reporter with a fixed clock and returns the
// raw NDJSON line plus its decoded field map. The clock is pinned so two turns
// differ only by their per-turn fields, not by timestamp.
func ndjsonFor(t *testing.T, turn session.Turn) (string, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	r := NewReporter(nil, &buf)
	r.now = func() time.Time { return time.Unix(1750000000, 0).UTC() }
	if err := r.Report(turn); err != nil {
		t.Fatalf("Report: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("parse ndjson %q: %v", line, err)
	}
	return line, rec
}

// keySet returns the set of top-level keys in a decoded NDJSON record.
func keySet(m map[string]any) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// TestNDJSONLayoutCoordinatesAtZero covers m4 at the serialization layer: a
// divergence whose coordinates land at 0 (byte 0 / msg[0]) must emit
// layout_byte_offset:0 and layout_msg_index:0, not drop them via omitempty.
func TestNDJSONLayoutCoordinatesAtZero(t *testing.T) {
	turn := session.Turn{
		TurnNum: 2,
		Layout: openai.LayoutDiff{
			Diverged:     true,
			ByteOffset:   0,
			MessageIndex: 0,
			Field:        "content",
		},
	}
	_, rec := ndjsonFor(t, turn)

	if v, ok := rec["layout_byte_offset"]; !ok {
		t.Error("layout_byte_offset omitted at offset 0 (omitempty regressed)")
	} else if v.(float64) != 0 {
		t.Errorf("layout_byte_offset = %v, want 0", v)
	}
	if v, ok := rec["layout_msg_index"]; !ok {
		t.Error("layout_msg_index omitted at msg[0] (omitempty regressed)")
	} else if v.(float64) != 0 {
		t.Errorf("layout_msg_index = %v, want 0", v)
	}
	if v, ok := rec["layout_diverged"]; !ok || v != true {
		t.Errorf("layout_diverged = %v, want true", v)
	}
}

// TestNDJSONRealDivergenceEmitsCoordinates drives the real tracker through a
// within-session mutation (the system + first-user anchors stay put so the
// session id is stable; the rewrite lands at msg[2]) and asserts the NDJSON
// carries the linter's coordinate fields — they must always be present for a
// real divergence, never dropped by omitempty.
func TestNDJSONRealDivergenceEmitsCoordinates(t *testing.T) {
	tr := session.NewTracker()
	canonical := []openai.Message{mj("system", "s"), mj("user", "anchor"), mj("assistant", "original tool output")}
	tr.Observe(canonical)

	// Rewrite msg[2] (a non-anchor message) — a stable-session divergence.
	turn := tr.Observe([]openai.Message{
		mj("system", "s"), mj("user", "anchor"), mj("assistant", "RE-RENDERED tool output"),
	})
	if !turn.Layout.Diverged {
		t.Fatalf("expected a divergence at msg[2], got %+v", turn.Layout)
	}
	if turn.Layout.MessageIndex != 2 {
		t.Fatalf("MessageIndex = %d, want 2", turn.Layout.MessageIndex)
	}

	_, rec := ndjsonFor(t, turn)
	if v, ok := rec["layout_msg_index"]; !ok {
		t.Error("layout_msg_index omitted for a real divergence")
	} else if v.(float64) != 2 {
		t.Errorf("layout_msg_index = %v, want 2", v)
	}
	if _, ok := rec["layout_byte_offset"]; !ok {
		t.Error("layout_byte_offset omitted; the coordinate field must always be present")
	}
	if v, ok := rec["layout_diverged"]; !ok || v != true {
		t.Errorf("layout_diverged = %v, want true", v)
	}
	if v, ok := rec["layout_field"]; !ok || v == "" {
		t.Errorf("layout_field = %v, want the breaking field name", v)
	}
}

// TestNDJSONNoDivergenceConsistentShape covers m4's consistency goal: the first
// turn (no prior canonical) and a clean turn 2+ must emit identical field
// shapes with the coordinate sentinel at -1. Before the fix the first turn
// omitted layout_msg_index while clean turn 2+ emitted -1.
func TestNDJSONNoDivergenceConsistentShape(t *testing.T) {
	tr := session.NewTracker()
	hist := []openai.Message{mj("system", "s"), mj("user", "u1")}

	first := tr.Observe(hist) // first turn: no prior canonical
	hist = append(hist, mj("assistant", "a1"), mj("user", "u2"))
	clean := tr.Observe(hist) // clean turn 2+: append-only, no divergence

	for _, turn := range []session.Turn{first, clean} {
		if turn.Layout.Diverged {
			t.Errorf("turn %d flagged diverged, want clean", turn.TurnNum)
		}
		if turn.Layout.ByteOffset != -1 || turn.Layout.MessageIndex != -1 {
			t.Errorf("turn %d coords = (%d,%d), want (-1,-1) sentinel",
				turn.TurnNum, turn.Layout.ByteOffset, turn.Layout.MessageIndex)
		}
	}

	j1, r1 := ndjsonFor(t, first)
	j2, r2 := ndjsonFor(t, clean)

	for _, want := range []string{`"layout_diverged":false`, `"layout_byte_offset":-1`, `"layout_msg_index":-1`} {
		if !strings.Contains(j1, want) {
			t.Errorf("first-turn NDJSON missing %s; got %s", want, j1)
		}
		if !strings.Contains(j2, want) {
			t.Errorf("clean-turn NDJSON missing %s; got %s", want, j2)
		}
	}

	// Identical field SET: every key present in one must be present in the other.
	// Values legitimately differ (turn, prev_len, incoming_len, lcp, total_tokens).
	k1, k2 := keySet(r1), keySet(r2)
	if len(k1) != len(k2) {
		t.Errorf("field count differs: first=%d clean=%d\nfirst=%s\nclean=%s", len(k1), len(k2), j1, j2)
	}
	for k := range k1 {
		if _, ok := k2[k]; !ok {
			t.Errorf("first-turn has field %q but clean-turn does not\nfirst=%s\nclean=%s", k, j1, j2)
		}
	}
}
