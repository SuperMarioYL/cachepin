// Package openai models the subset of the OpenAI-compatible chat completions
// protocol that CachePin needs: the message array, content-hashing of each
// message, and faithful re-serialization of a request body (preserving every
// top-level field the proxy does not understand, so forwarding stays
// byte-faithful except for fields CachePin deliberately rewrites).
package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
