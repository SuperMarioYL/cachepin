package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessageRoundTripPreservesUnknownFields covers
// fix-pin-remarshal-strips-message-fields: the Message struct models only
// role/content/name/tool_calls/tool_call_id, so before the fix an unmodeled
// per-message field (cache_control, legacy function_call, provider-specific
// extensions) was dropped on ParseChatRequest and therefore lost on the --pin
// reconcile re-marshal (cmd/cachepin/main.go calls req.SetMessages(reconciled)
// then req.Marshal(), re-serializing from the already-stripped structs). With
// the v0.8.0 passthrough, Message carries unmodeled fields in raw and
// re-emits them, so the round-trip is faithful — dropping cache_control would
// defeat the upstream's own prompt-caching breakpoint, the opposite of
// cachepin's mission.
func TestMessageRoundTripPreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"s","cache_control":{"type":"ephemeral"}},` +
		`{"role":"user","content":"hi","function_call":{"name":"search"}}` +
		`]}`)

	req, err := ParseChatRequest(body)
	if err != nil {
		t.Fatalf("ParseChatRequest: %v", err)
	}

	// Simulate the --pin reconcile re-marshal exactly as the interceptor does:
	// SetMessages with the (reconciled) array then Marshal. Before the fix this
	// is where cache_control / function_call disappeared from the forwarded body.
	req.SetMessages(req.Messages)
	out, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The marshaled body itself must still carry the unmodeled fields.
	for _, want := range []string{`"cache_control"`, `"ephemeral"`, `"function_call"`, `"search"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("re-marshaled body lost %q; got %s", want, out)
		}
	}

	// And a re-parse must re-populate the passthrough so a subsequent reconcile
	// round-trip stays faithful too.
	req2, err := ParseChatRequest(out)
	if err != nil {
		t.Fatalf("re-parse marshaled body: %v", err)
	}
	if len(req2.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(req2.Messages))
	}
	cc, ok := req2.Messages[0].raw["cache_control"]
	if !ok {
		t.Fatalf("cache_control not preserved on re-parse; got %s", out)
	}
	var ccMap map[string]any
	if err := json.Unmarshal(cc, &ccMap); err != nil {
		t.Fatalf("cache_control not valid JSON: %v (raw=%s)", err, cc)
	}
	if ccMap["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", ccMap["type"])
	}
	fc, ok := req2.Messages[1].raw["function_call"]
	if !ok {
		t.Fatalf("function_call not preserved on re-parse; got %s", out)
	}
	var fcMap map[string]any
	if err := json.Unmarshal(fc, &fcMap); err != nil {
		t.Fatalf("function_call not valid JSON: %v (raw=%s)", err, fc)
	}
	if fcMap["name"] != "search" {
		t.Errorf("function_call.name = %v, want search", fcMap["name"])
	}
}

// TestMessageMarshalKnownFieldsUnchanged guards the linter's wireBytes (which
// hashes json.Marshal(m)) and LintLayout (which compares those bytes in
// declaration order): a Message with no unknown fields must serialize
// byte-identically to the pre-v0.8.0 struct marshal, in the same
// role/content/name/tool_calls/tool_call_id order with the same omitempty
// semantics. A regression here would silently shift every layout_byte_offset.
func TestMessageMarshalKnownFieldsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		m    Message
		want string
	}{
		{"role+content", Message{Role: "user", Content: json.RawMessage(`"hi"`)}, `{"role":"user","content":"hi"}`},
		{"role only", Message{Role: "system"}, `{"role":"system"}`},
		{"name", Message{Role: "user", Name: "search"}, `{"role":"user","name":"search"}`},
		{"tool_calls", Message{Role: "assistant", Content: json.RawMessage(`null`), ToolCalls: json.RawMessage(`[{"id":"1"}]`)}, `{"role":"assistant","content":null,"tool_calls":[{"id":"1"}]}`},
		{"tool_call_id", Message{Role: "tool", ToolCallID: "call_1"}, `{"role":"tool","tool_call_id":"call_1"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.m)
		if err != nil {
			t.Fatalf("%s: json.Marshal: %v", c.name, err)
		}
		if string(b) != c.want {
			t.Errorf("%s: got %s, want %s (modeled-field byte order/omitempty regressed)", c.name, b, c.want)
		}
	}
}

// TestMessageHashIgnoresUnknownFields confirms the v0.8.0 passthrough did not
// pull unmodeled fields into Hash: two messages that differ ONLY in an
// unmodeled field must hash equally, so LongestCommonPrefix / prefix-diffing is
// unchanged (the reconcile LCP boundary is still computed over modeled content).
func TestMessageHashIgnoresUnknownFields(t *testing.T) {
	base := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	withExtra := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	withExtra.raw = map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)}
	if base.Hash() != withExtra.Hash() {
		t.Errorf("Hash differs for messages equal in modeled fields (raw leaked into Hash)")
	}
}
