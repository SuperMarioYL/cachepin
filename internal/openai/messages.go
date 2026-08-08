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
	// ChatRequest preserves unknown TOP-LEVEL fields. It is not part of Hash,
	// so LongestCommonPrefix / prefix-diffing is unchanged.
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
// the raw content/tool bytes. Two messages with the same hash are treated as
// the same logical turn, which is what makes prefix-diffing the server's KV
// Cache boundary possible.
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
}

// ParseChatRequest decodes a request body into a ChatRequest. Unknown top-level
// fields are preserved for faithful re-serialization.
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("openai: parse request body: %w", err)
	}

	req := &ChatRequest{raw: raw}

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
// fields and writing back the current Model, Messages, and Stream values.
func (r *ChatRequest) Marshal() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(r.raw)+3)
	for k, v := range r.raw {
		out[k] = v
	}

	msgs, err := json.Marshal(r.Messages)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal messages: %w", err)
	}
	out["messages"] = msgs

	if _, present := r.raw["model"]; present || r.Model != "" {
		model, err := json.Marshal(r.Model)
		if err != nil {
			return nil, fmt.Errorf("openai: marshal model: %w", err)
		}
		out["model"] = model
	}

	if _, present := r.raw["stream"]; present {
		stream, err := json.Marshal(r.Stream)
		if err != nil {
			return nil, fmt.Errorf("openai: marshal stream: %w", err)
		}
		out["stream"] = stream
	}

	return json.Marshal(out)
}
