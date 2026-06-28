package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// LayoutDiff is the result of the byte-level context-layout linter. It pinpoints
// where two consecutive requests' serialized prefixes first diverge — the exact
// byte offset and which message field broke prefix-stability — so a user can see
// what cache-busting context-layout churn the harness introduced.
//
// The whole value of CachePin is protecting the upstream KV Cache, whose prefix
// is valid only up to the first differing byte. Message-level diffing tells you
// *which message* broke; the layout linter tells you *why*: a reordered tool
// schema, a changed system prompt, a stray whitespace re-render, etc.
type LayoutDiff struct {
	// Diverged reports whether the prefixes differ at all. When false the
	// remaining fields are zero/empty and the cache prefix is fully intact.
	Diverged bool
	// ByteOffset is the absolute byte offset, within the concatenated wire-form
	// of the canonical message array, where the first difference appears.
	ByteOffset int
	// MessageIndex is the index of the message in which the divergence occurs,
	// or len(canonical)/len(incoming) when one side simply ran out of messages.
	MessageIndex int
	// Field names the message field that broke prefix-stability: one of
	// "role", "content", "name", "tool_calls", "tool_call_id",
	// "message-count", or "field-order" when the JSON object framing itself
	// changed.
	Field string
	// Detail is a short human-readable summary, e.g.
	// "content changed at byte 1423 (msg[3])".
	Detail string
}

// NoDivergence is the sentinel returned by LintLayout when two message arrays
// share a fully intact cache prefix — no byte-level divergence. ByteOffset and
// MessageIndex are -1 (not 0) so a clean turn's NDJSON carries the same field
// set as a divergent one: a zero value would be dropped by omitempty, which is
// exactly the bug that made a divergence at offset 0 / msg[0] lose its
// coordinates and made the first turn omit layout_msg_index. The first turn
// (no prior canonical) and any clean append both resolve to this sentinel.
var NoDivergence = LayoutDiff{ByteOffset: -1, MessageIndex: -1}

// LintLayout compares the byte-level wire form of two message arrays and reports
// the first point at which their cache prefix diverges. canonical is the prior
// ground truth; incoming is the request the harness just sent. The comparison is
// field-aware: it walks messages in lockstep, and within the first differing
// message identifies which field (role, content, tool_calls, ...) caused the
// break, mirroring the longest-common-prefix the inference server would compute.
func LintLayout(canonical, incoming []Message) LayoutDiff {
	n := len(canonical)
	if len(incoming) < n {
		n = len(incoming)
	}

	offset := 0
	for i := 0; i < n; i++ {
		cb := wireBytes(canonical[i])
		ib := wireBytes(incoming[i])
		if bytes.Equal(cb, ib) {
			offset += len(cb)
			continue
		}
		field, rel := firstDifferingField(canonical[i], incoming[i])
		return LayoutDiff{
			Diverged:     true,
			ByteOffset:   offset + rel,
			MessageIndex: i,
			Field:        field,
			Detail:       fmt.Sprintf("%s changed at byte %d (msg[%d])", field, offset+rel, i),
		}
	}

	// All shared messages were byte-identical. If incoming simply extends
	// canonical (canonical is a full prefix of incoming), the cache prefix is
	// fully intact — a clean append, not a break. Only when canonical has
	// messages beyond the shared range (the harness dropped a previously
	// established message) is the prefix broken by a message-count change.
	if len(canonical) > len(incoming) {
		return LayoutDiff{
			Diverged:     true,
			ByteOffset:   offset,
			MessageIndex: n,
			Field:        "message-count",
			Detail: fmt.Sprintf("message dropped (count %d -> %d) at byte %d (msg[%d])",
				len(canonical), len(incoming), offset, n),
		}
	}

	return NoDivergence
}

// wireBytes is the canonical serialized form of a single message — what the
// upstream effectively hashes for its prefix cache. It is deterministic for a
// given Message because the struct fields serialize in a fixed order.
func wireBytes(m Message) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		// Marshal of this struct cannot fail for well-formed inputs; fall back
		// to a stable byte representation so the linter never panics.
		return []byte(m.Role)
	}
	return b
}

// firstDifferingField names the field that broke prefix-stability between two
// messages and returns the byte offset of the difference relative to the start
// of the message's wire form. Fields are checked in serialization order so the
// reported offset is consistent with wireBytes.
func firstDifferingField(a, b Message) (field string, rel int) {
	ab := wireBytes(a)
	bb := wireBytes(b)
	rel = commonBytePrefix(ab, bb)

	switch {
	case a.Role != b.Role:
		return "role", rel
	case !bytes.Equal(a.Content, b.Content):
		return "content", rel
	case a.Name != b.Name:
		return "name", rel
	case !bytes.Equal(a.ToolCalls, b.ToolCalls):
		return "tool_calls", rel
	case a.ToolCallID != b.ToolCallID:
		return "tool_call_id", rel
	default:
		// Same logical field values but different wire bytes: the JSON framing
		// or field ordering differs (e.g. whitespace, key order from a
		// re-render).
		return "field-order", rel
	}
}

// commonBytePrefix returns the length of the longest shared leading byte run.
func commonBytePrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}
