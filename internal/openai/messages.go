// Package openai models the subset of the OpenAI-compatible chat completions
// protocol that CachePin needs: the message array, content-hashing of each
// message, and faithful re-serialization of a request body (preserving every
// top-level field the proxy does not understand, so forwarding stays
// byte-faithful except for fields CachePin deliberately rewrites).
package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Message is one entry in a chat completion's `messages` array.
//
// Content is kept as json.RawMessage because the OpenAI schema allows it to be
// either a plain string or an array of typed content parts (multimodal). The
// remaining fields are carried so that re-serialized messages stay faithful to
// what the harness sent.
type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`

	// raw carries every per-message field the struct does not model (e.g.
	// cache_control, legacy function_call, provider-specific extensions) so a
	// round-trip through ParseChatRequest -> Marshal is faithful and the --pin
	// reconcile re-marshal no longer strips them from the body forwarded
	// upstream (v0.8.0, fix-pin-remarshal-strips-message-fields). Mirrors how
	// ChatRequest preserves unknown TOP-LEVEL fields. It IS folded into Hash
	// (v0.9.0, fix-hash-ignores-raw-message-fields) because the upstream prefix
	// cache keys on the wire bytes, which include raw; folding it in makes
	// LongestCommonPrefix detect a cache_control change on an early message as a
	// real break instead of reporting silent-green metrics.
	raw map[string]json.RawMessage
}

// knownMessageFields are the per-message fields the Message struct models; the
// remainder of a parsed message object is carried verbatim in Message.raw.
var knownMessageFields = []string{"role", "content", "name", "tool_calls", "tool_call_id"}

// messageWire is the modeled slice of a Message, used to serialize the known
// fields in their fixed declaration order (the wire form wireBytes / LintLayout
// rely on) with the same omitempty semantics the original struct tags had.
// It is a distinct type so json.Marshal(messageWire{...}) uses the default
// struct marshal (not Message.MarshalJSON), keeping the modeled-field bytes
// byte-identical to the pre-v0.8.0 output.
type messageWire struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// MarshalJSON re-serializes a Message preserving every field it was parsed
// from: the modeled fields in declaration order (byte-identical to the prior
// struct marshal when no unknown fields are present, so wireBytes / LintLayout
// and existing pin-mode forwarding tests are unchanged) followed by any
// unmodeled per-message fields carried in raw, in sorted key order for
// determinism.
func (m Message) MarshalJSON() ([]byte, error) {
	w := messageWire{
		Role: m.Role, Content: m.Content, Name: m.Name,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID,
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal message: %w", err)
	}
	if len(m.raw) == 0 {
		return b, nil
	}
	keys := make([]string, 0, len(m.raw))
	for k := range m.raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// b is "{...}" with at least the "role" field (no omitempty on role), so
	// strip the closing brace and append the unknown fields before re-closing.
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	for _, k := range keys {
		buf.WriteByte(',')
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("openai: marshal message field %q: %w", k, err)
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(m.raw[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON decodes a message object into the modeled fields and preserves
// every remaining per-message field verbatim in raw, so a subsequent
// MarshalJSON re-emits it. This is what keeps cache_control (and other
// provider-specific fields) from being stripped on the --pin reconcile
// re-marshal (cmd/cachepin/main.go calls req.SetMessages(reconciled) then
// req.Marshal(), re-serializing from the parsed structs).
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("openai: parse message: %w", err)
	}
	if v, ok := raw["role"]; ok {
		if err := json.Unmarshal(v, &m.Role); err != nil {
			return fmt.Errorf("openai: parse message role: %w", err)
		}
	}
	if v, ok := raw["content"]; ok {
		m.Content = append([]byte(nil), v...)
	}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &m.Name); err != nil {
			return fmt.Errorf("openai: parse message name: %w", err)
		}
	}
	if v, ok := raw["tool_calls"]; ok {
		m.ToolCalls = append([]byte(nil), v...)
	}
	if v, ok := raw["tool_call_id"]; ok {
		if err := json.Unmarshal(v, &m.ToolCallID); err != nil {
			return fmt.Errorf("openai: parse message tool_call_id: %w", err)
		}
	}
	for _, k := range knownMessageFields {
		delete(raw, k)
	}
	if len(raw) > 0 {
		m.raw = make(map[string]json.RawMessage, len(raw))
		for k, v := range raw {
			m.raw[k] = append([]byte(nil), v...)
		}
	}
	return nil
}

// Hash is the content-identity of a message: a stable sha256 over the role and
// the raw content/tool bytes, plus the unmodeled per-message passthrough (raw)
// folded in sorted-key order. Two messages with the same hash are treated as
// the same logical turn, which is what makes prefix-diffing the server's KV
// Cache boundary possible.
//
// raw is folded in (v0.9.0, fix-hash-ignores-raw-message-fields) because the
// upstream prefix cache keys on the full wire bytes, which include raw via
// MarshalJSON. Excluding it made LongestCommonPrefix treat a cache_control
// toggle/change on an early message (same role/content) as matching, so the
// Turn reported Mutated=false / ReprocessedTokens=0 / PreservedPrefixPct=100
// while the cache actually broke and reprocessed the whole prefix — the exact
// silent-green-while-reprocessing failure v0.4.0/v0.6.0/v0.7.0 targeted. Sorted
// keys + \x00 separators mirror MarshalJSON's deterministic ordering.
func (m Message) Hash() string {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write(m.Content)
	h.Write([]byte{0})
	h.Write([]byte(m.Name))
	h.Write([]byte{0})
	h.Write(m.ToolCalls)
	h.Write([]byte{0})
	h.Write([]byte(m.ToolCallID))
	h.Write([]byte{0})
	if len(m.raw) > 0 {
		keys := make([]string, 0, len(m.raw))
		for k := range m.raw {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write(m.raw[k])
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashAll returns the per-message content hashes in order. The session tracker
// uses these to compute the longest common prefix between the canonical history
// and an incoming request.
func HashAll(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Hash()
	}
	return out
}

// ChatRequest is a parsed chat completion request. It exposes the fields
// CachePin reasons about (model, messages, stream) while retaining every other
// top-level field verbatim in raw, so Marshal reproduces the original body with
// only the intended fields changed.
type ChatRequest struct {
	Model    string
	Messages []Message
	Stream   bool

	raw map[string]json.RawMessage
	// order is the top-level key order the harness sent, captured in
	// ParseChatRequest so Marshal can re-emit in that same order. Without it
	// Marshal built a map[string]json.RawMessage and json.Marshal alphabetized
	// the keys, so a --pin reconcile re-marshal reordered the body and broke the
	// upstream prefix cache at byte 0 (v0.9.0,
	// fix-pin-remarshal-reorders-top-level-fields).
	order []string
}

// ParseChatRequest decodes a request body into a ChatRequest. Unknown top-level
// fields are preserved for faithful re-serialization, and their original key
// order is captured in ChatRequest.order so Marshal re-emits the body in the
// harness's field order (the upstream prefix cache keys on request body bytes,
// so a re-ordered re-marshal breaks it at byte 0 — v0.9.0,
// fix-pin-remarshal-reorders-top-level-fields).
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("openai: parse request body: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("openai: parse request body: not a JSON object")
	}

	raw := make(map[string]json.RawMessage)
	var order []string
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("openai: parse request body key: %w", err)
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, fmt.Errorf("openai: parse request body: unexpected key token %v", ktok)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("openai: parse request body field %q: %w", key, err)
		}
		raw[key] = v
		order = append(order, key)
	}
	// Consume the closing brace so a trailing token surfaces an error rather than
	// being silently ignored.
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("openai: parse request body close: %w", err)
	}

	req := &ChatRequest{raw: raw, order: order}

	if v, ok := raw["model"]; ok {
		if err := json.Unmarshal(v, &req.Model); err != nil {
			return nil, fmt.Errorf("openai: parse model: %w", err)
		}
	}
	if v, ok := raw["messages"]; ok {
		if err := json.Unmarshal(v, &req.Messages); err != nil {
			return nil, fmt.Errorf("openai: parse messages: %w", err)
		}
	}
	if v, ok := raw["stream"]; ok {
		if err := json.Unmarshal(v, &req.Stream); err != nil {
			return nil, fmt.Errorf("openai: parse stream: %w", err)
		}
	}

	return req, nil
}

// SetMessages replaces the message array (used by pin-mode reconciliation).
func (r *ChatRequest) SetMessages(msgs []Message) {
	r.Messages = msgs
}

// Marshal re-serializes the request, preserving all originally-present top-level
// fields in their ORIGINAL KEY ORDER and writing back the current Model,
// Messages, and Stream values. Emitting in the harness's order (rather than via
// a map[string]json.RawMessage, which json.Marshal alphabetizes) keeps the
// re-marshaled body byte-faithful to the verbatim turn-1 body: the upstream
// prefix cache keys on request body bytes, so a --pin reconcile that re-orders
// the top-level keys breaks the cache at byte 0 even when the content is
// unchanged (v0.9.0, fix-pin-remarshal-reorders-top-level-fields).
func (r *ChatRequest) Marshal() ([]byte, error) {
	msgs, err := json.Marshal(r.Messages)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal messages: %w", err)
	}
	// Precompute the re-marshaled modeled values: messages is always written back,
	// model when it was present or was set, stream when it was present (preserves
	// the pre-v0.9.0 write-back semantics).
	modeled := map[string]json.RawMessage{"messages": msgs}
	_, hasModel := r.raw["model"]
	if hasModel || r.Model != "" {
		model, err := json.Marshal(r.Model)
		if err != nil {
			return nil, fmt.Errorf("openai: marshal model: %w", err)
		}
		modeled["model"] = model
	}
	_, hasStream := r.raw["stream"]
	if hasStream {
		stream, err := json.Marshal(r.Stream)
		if err != nil {
			return nil, fmt.Errorf("openai: marshal stream: %w", err)
		}
		modeled["stream"] = stream
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	emit := func(key string, value json.RawMessage) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(key)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(value)
	}
	// Walk keys in the harness's original order, substituting the re-marshaled
	// modeled values for messages/model/stream and the verbatim raw bytes for
	// everything else.
	emitted := make(map[string]bool, len(r.order))
	for _, k := range r.order {
		if v, ok := modeled[k]; ok {
			emit(k, v)
		} else {
			emit(k, r.raw[k])
		}
		emitted[k] = true
	}
	// Append modeled fields absent from the original body so Marshal still
	// round-trips the struct state (messages is always written; model when it was
	// set without a raw original). Stream only exists in modeled when present in
	// raw, so it is never appended here.
	for _, k := range []string{"messages", "model", "stream"} {
		if !emitted[k] {
			if v, ok := modeled[k]; ok {
				emit(k, v)
			}
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
