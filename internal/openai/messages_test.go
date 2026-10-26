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

// TestMessageHashIncludesRawPassthrough is the regression test for
// fix-hash-ignores-raw-message-fields: the v0.8.0 passthrough carried unmodeled
// per-message fields (cache_control, legacy function_call, provider extensions)
// in raw but excluded them from Hash. The upstream prefix cache keys on the
// full wire bytes (which include raw via MarshalJSON), so a harness toggling or
// changing cache_control on an early message while keeping role/content
// identical made LongestCommonPrefix count the message as matching — the Turn
// reported Mutated=false / ReprocessedTokens=0 / PreservedPrefixPct=100 while
// the cache actually broke and reprocessed the whole prefix. Hash now folds a
// sorted-keys representation of raw into the digest, so any raw-field change
// mutates the hash. (This flips the pre-v0.9.0 TestMessageHashIgnoresUnknown
// Fields assertion, which encoded the bug.)
func TestMessageHashIncludesRawPassthrough(t *testing.T) {
	base := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	withExtra := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	withExtra.raw = map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)}
	if base.Hash() == withExtra.Hash() {
		t.Errorf("Hash equal for messages differing in a raw field (cache_control invisible to Hash); base=%s withExtra=%s", base.Hash(), withExtra.Hash())
	}

	// A change to the raw field's VALUE (not just its presence) must also mutate
	// the hash — this is the cache_control toggle/change the bug-hunter
	// reproduced ({"type":"ephemeral"} -> {"type":"ephemeral","ttl":"5m"}).
	withTTL := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	withTTL.raw = map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral","ttl":"5m"}`)}
	if withExtra.Hash() == withTTL.Hash() {
		t.Errorf("Hash equal for messages differing only in a raw field's value; withExtra=%s withTTL=%s", withExtra.Hash(), withTTL.Hash())
	}

	// Determinism: the same raw fields produce the same hash regardless of map
	// iteration order (sorted-key digest is stable).
	a := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	a.raw = map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`), "function_call": json.RawMessage(`{"name":"search"}`)}
	b := Message{Role: "user", Content: json.RawMessage(`"hi"`)}
	b.raw = map[string]json.RawMessage{"function_call": json.RawMessage(`{"name":"search"}`), "cache_control": json.RawMessage(`{"type":"ephemeral"}`)}
	if a.Hash() != b.Hash() {
		t.Errorf("Hash not deterministic across map iteration order; a=%s b=%s", a.Hash(), b.Hash())
	}
}

// TestMarshalPreservesTopLevelFieldOrder is the regression test for
// fix-pin-remarshal-reorders-top-level-fields: under --pin the first reconciled
// turn is re-marshaled via ChatRequest.Marshal. Before the fix Marshal built a
// map[string]json.RawMessage and json.Marshal alphabetized the keys, so a
// harness body sent as {"model":...,"messages":...,"stream":...} was re-emitted
// as {"messages":...,"model":...,"stream":...}, diverging from the verbatim
// turn-1 body at byte 3 ('o' from "model" vs 'e' from "messages"). The upstream
// prefix cache keys on request body bytes, so the cache broke at the very start
// and reprocessed everything while the tracker reported ~0 reprocessed (Hash is
// order-independent). Marshal now emits in the harness's original key order, so a
// SetMessages(same)+Marshal round-trip is byte-faithful to the original body.
func TestMarshalPreservesTopLevelFieldOrder(t *testing.T) {
	// Non-alphabetical top-level order (model before messages before stream before
	// an unknown field). Alphabetical would be messages, model, stream,
	// temperature, so the pre-fix re-marshal reordered every key.
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"stream":true,"temperature":0.5}`)
	req, err := ParseChatRequest(body)
	if err != nil {
		t.Fatalf("ParseChatRequest: %v", err)
	}

	// Simulate the --pin reconcile re-marshal exactly as the interceptor does:
	// SetMessages with the (reconciled) array then Marshal.
	req.SetMessages(req.Messages)
	out, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The re-marshaled body must be byte-identical to the harness's original:
	// same top-level key order, same per-message field order (role-first, which
	// Message.MarshalJSON already emits in declaration order), and unknown fields
	// (temperature) carried verbatim in their original position. Before the fix
	// the output was {"messages":...,"model":...,"stream":...,"temperature":0.5}.
	if string(out) != string(body) {
		t.Errorf("re-marshal did not preserve top-level field order;\n got: %s\nwant: %s", out, body)
	}
}

// TestMarshalPreservesReorderedTopLevelFields guards against a regression where
// Marshal falls back to alphabetical output: a body whose harness order is the
// REVERSE of alphabetical must survive a SetMessages+Marshal round-trip
// byte-for-byte. This is a stronger ordering invariant than the model-first
// case above (every adjacent pair is in anti-alphabetical order).
func TestMarshalPreservesReorderedTopLevelFields(t *testing.T) {
	body := []byte(`{"stream":true,"temperature":0.5,"messages":[{"role":"user","content":"u"}],"model":"m"}`)
	req, err := ParseChatRequest(body)
	if err != nil {
		t.Fatalf("ParseChatRequest: %v", err)
	}
	req.SetMessages(req.Messages)
	out, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("re-marshal did not preserve (anti-alphabetical) field order;\n got: %s\nwant: %s", out, body)
	}
}

// TestParseChatRequestRejectsTrailingData is the regression test for
// fix-parse-accepts-trailing-garbage (v0.12.0). ParseChatRequest reads the
// top-level object with a json.Decoder and, before the fix, consumed only the
// closing brace — so anything after the object was silently ignored, despite
// the code comment claiming a trailing token surfaces an error. On the v0.11.0
// tag ParseChatRequest([]byte(`{"messages":[]} trailing-garbage`)) returned
// err=nil, and in --pin mode the interceptor re-marshals the parsed struct, so
// the accepted bytes were silently deleted from the forwarded request.
func TestParseChatRequestRejectsTrailingData(t *testing.T) {
	bad := []string{
		`{"messages":[]} trailing-garbage`,
		`{"messages":[]}{"second":true}`,
		`{"messages":[]} 1`,
	}
	for _, body := range bad {
		if _, err := ParseChatRequest([]byte(body)); err == nil {
			t.Errorf("ParseChatRequest(%q) = nil error, want a trailing-data parse error", body)
		}
	}

	// A well-formed body (including trailing whitespace/newlines, which the
	// decoder skips) must still parse.
	good := []string{
		`{"messages":[]}`,
		"{\"messages\":[]}\n",
		"{\"messages\":[]}\n\t ",
		`{"model":"m","messages":[{"role":"user","content":"u"}],"stream":true}`,
	}
	for _, body := range good {
		if _, err := ParseChatRequest([]byte(body)); err != nil {
			t.Errorf("ParseChatRequest(%q) = %v, want nil (well-formed body)", body, err)
		}
	}
}
