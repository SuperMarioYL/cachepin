package openai

import (
	"encoding/json"
	"testing"
)

func m(role, content string) Message {
	b, _ := json.Marshal(content)
	return Message{Role: role, Content: json.RawMessage(b)}
}

// TestLintLayoutCleanAppendNoDivergence: when incoming extends canonical with
// new messages and the shared prefix is byte-identical, the layout linter
// reports no within-prefix divergence.
func TestLintLayoutCleanAppendNoDivergence(t *testing.T) {
	canonical := []Message{m("system", "s"), m("user", "u1")}
	incoming := []Message{m("system", "s"), m("user", "u1"), m("assistant", "a1")}

	d := LintLayout(canonical, incoming)
	if d.Diverged {
		t.Errorf("Diverged = true, want false for a clean append")
	}
}

// TestLintLayoutContentChange pinpoints a content-field break and reports the
// byte offset within the concatenated wire form.
func TestLintLayoutContentChange(t *testing.T) {
	canonical := []Message{m("system", "you are a coding agent"), m("user", "hello"), m("assistant", "original answer")}
	incoming := []Message{m("system", "you are a coding agent"), m("user", "hello"), m("assistant", "DIFFERENT answer")}

	d := LintLayout(canonical, incoming)
	if !d.Diverged {
		t.Fatal("Diverged = false, want true for a content change")
	}
	if d.MessageIndex != 2 {
		t.Errorf("MessageIndex = %d, want 2", d.MessageIndex)
	}
	if d.Field != "content" {
		t.Errorf("Field = %q, want content", d.Field)
	}
	// The offset must fall inside msg[2]: at least the byte length of the two
	// identical preceding messages.
	prefixLen := len(wireBytes(canonical[0])) + len(wireBytes(canonical[1]))
	if d.ByteOffset < prefixLen {
		t.Errorf("ByteOffset = %d, want >= %d (inside msg[2])", d.ByteOffset, prefixLen)
	}
}

// TestLintLayoutRoleChange identifies the role field as the cause.
func TestLintLayoutRoleChange(t *testing.T) {
	canonical := []Message{m("system", "s"), m("user", "same text")}
	incoming := []Message{m("system", "s"), m("assistant", "same text")}

	d := LintLayout(canonical, incoming)
	if !d.Diverged || d.Field != "role" {
		t.Errorf("got Diverged=%v Field=%q, want true/role", d.Diverged, d.Field)
	}
	if d.MessageIndex != 1 {
		t.Errorf("MessageIndex = %d, want 1", d.MessageIndex)
	}
}

// TestLintLayoutToolCallsChange identifies a tool-schema/tool_calls break, the
// canonical "tool schema reordered" cache-buster.
func TestLintLayoutToolCallsChange(t *testing.T) {
	a := Message{Role: "assistant", Content: json.RawMessage(`null`), ToolCalls: json.RawMessage(`[{"id":"1","type":"function"}]`)}
	b := Message{Role: "assistant", Content: json.RawMessage(`null`), ToolCalls: json.RawMessage(`[{"id":"2","type":"function"}]`)}
	canonical := []Message{m("system", "s"), a}
	incoming := []Message{m("system", "s"), b}

	d := LintLayout(canonical, incoming)
	if !d.Diverged || d.Field != "tool_calls" {
		t.Errorf("got Diverged=%v Field=%q, want true/tool_calls", d.Diverged, d.Field)
	}
}

// TestLintLayoutMessageCountOnly: identical shared prefix but different message
// count is reported as a message-count change, not a within-message break.
func TestLintLayoutMessageCountOnly(t *testing.T) {
	canonical := []Message{m("system", "s"), m("user", "u1"), m("assistant", "a1")}
	incoming := []Message{m("system", "s"), m("user", "u1")} // a1 dropped

	d := LintLayout(canonical, incoming)
	if !d.Diverged {
		t.Fatal("Diverged = false, want true for a message-count change")
	}
	if d.Field != "message-count" {
		t.Errorf("Field = %q, want message-count", d.Field)
	}
}

// TestLintLayoutByteOffsetIsExact: for a divergence in the very first message,
// the offset equals the common byte prefix of that message's wire form.
func TestLintLayoutByteOffsetIsExact(t *testing.T) {
	canonical := []Message{m("system", "abcXdef")}
	incoming := []Message{m("system", "abcYdef")}

	d := LintLayout(canonical, incoming)
	if !d.Diverged {
		t.Fatal("Diverged = false, want true")
	}
	if d.MessageIndex != 0 {
		t.Errorf("MessageIndex = %d, want 0 (divergence in the first message)", d.MessageIndex)
	}
	want := commonBytePrefix(wireBytes(canonical[0]), wireBytes(incoming[0]))
	if d.ByteOffset != want {
		t.Errorf("ByteOffset = %d, want %d (exact common-prefix byte)", d.ByteOffset, want)
	}
}

// TestLintLayoutNoDivergenceSentinel covers m4: a clean append must return the
// NoDivergence sentinel with coordinates at -1 (not 0), so the NDJSON field set
// is stable across turns and a divergence at offset 0 / msg[0] is not
// indistinguishable from "no divergence".
func TestLintLayoutNoDivergenceSentinel(t *testing.T) {
	canonical := []Message{m("system", "s"), m("user", "u1")}
	incoming := []Message{m("system", "s"), m("user", "u1"), m("assistant", "a1")}

	d := LintLayout(canonical, incoming)
	if d.Diverged {
		t.Error("Diverged = true, want false for a clean append")
	}
	if d != NoDivergence {
		t.Errorf("clean append = %+v, want NoDivergence sentinel %+v", d, NoDivergence)
	}

	// The first turn has no prior canonical; LintLayout must resolve it to the
	// same sentinel (the v0.2.0 path left a zero-value LayoutDiff instead).
	firstTurn := LintLayout(nil, incoming)
	if firstTurn != NoDivergence {
		t.Errorf("empty-canonical LintLayout = %+v, want NoDivergence", firstTurn)
	}
}

// TestLintLayoutDivergenceAtFirstByte preserves offset 0: when the only message
// is dropped, the message-count divergence lands at byte 0 / msg[0], and those
// coordinates must survive (not collapse to the no-divergence sentinel).
func TestLintLayoutDivergenceAtFirstByte(t *testing.T) {
	canonical := []Message{m("system", "s")}
	incoming := []Message{} // the only message dropped

	d := LintLayout(canonical, incoming)
	if !d.Diverged {
		t.Fatal("Diverged = false, want true for a dropped message")
	}
	if d.Field != "message-count" {
		t.Errorf("Field = %q, want message-count", d.Field)
	}
	if d.ByteOffset != 0 || d.MessageIndex != 0 {
		t.Errorf("dropped-only-message coords = (%d,%d), want (0,0) preserved", d.ByteOffset, d.MessageIndex)
	}
}
