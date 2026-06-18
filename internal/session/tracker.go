// Package session is CachePin's milestone-m2 brain: it reconstructs the
// canonical, append-only history of each chat session and measures how much of
// the upstream server's prefix (KV Cache) survives from one turn to the next.
//
// The insight (see mvp_plan.md §2) is that an OpenAI-compatible server's prefix
// cache is valid up to the first message that differs from what it processed
// before. By content-hashing each message and computing the longest common
// prefix between the canonical history and an incoming request, the tracker
// knows exactly where the cache breaks — and therefore how many previously
// processed tokens the server must throw away and recompute.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/SuperMarioYL/cachepin/internal/openai"
)

// Turn is the per-request report the tracker produces. It is the input to the
// metrics reporter and the benchmark.
type Turn struct {
	// SessionID identifies the conversation (derived from its system + first
	// user message).
	SessionID string
	// TurnNum is the 1-based count of requests seen for this session.
	TurnNum int
	// PrevLen is how many messages the canonical history held before this turn.
	PrevLen int
	// IncomingLen is the number of messages in this request.
	IncomingLen int
	// LCP is the longest common prefix (in messages) between the prior canonical
	// history and this request — i.e. where the upstream prefix cache stays valid.
	LCP int
	// Mutated reports whether the harness rewrote or dropped a previously
	// established message (LCP < PrevLen).
	Mutated bool
	// MutationIndex is the first differing message index when Mutated, else -1.
	MutationIndex int
	// PreservedPrefixPct is LCP/PrevLen as a percentage (100 on the first turn,
	// since there is nothing to preserve yet).
	PreservedPrefixPct float64
	// ReprocessedTokens estimates how many already-processed tokens the upstream
	// must recompute because the prefix broke. Zero on a clean append.
	ReprocessedTokens int
	// TotalTokens estimates the size of this request.
	TotalTokens int
}

// Session is the append-only ground truth for one conversation.
type Session struct {
	ID         string
	canonical  []openai.Message
	lastPrefix int
	turns      int
}

// Tracker observes chat-completions requests and maintains a Session per
// conversation. It is safe for concurrent use; the proxy may serve overlapping
// requests for different sessions.
type Tracker struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{sessions: make(map[string]*Session)}
}

// Observe records an incoming request's message array and returns the per-turn
// report. The request's messages become the new canonical history, so the next
// turn is diffed against what the harness most recently sent.
func (t *Tracker) Observe(msgs []openai.Message) Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := SessionID(msgs)
	s := t.sessions[id]
	if s == nil {
		s = &Session{ID: id}
		t.sessions[id] = s
	}

	prevLen := len(s.canonical)
	lcp := LongestCommonPrefix(s.canonical, msgs)
	mutated := lcp < prevLen

	pct := 100.0
	if prevLen > 0 {
		pct = float64(lcp) / float64(prevLen) * 100
	}

	reprocessed := 0
	mutIndex := -1
	if mutated {
		// Everything the server had cached beyond the break must be recomputed.
		reprocessed = EstimateTokens(s.canonical[lcp:prevLen])
		mutIndex = lcp
	}

	s.turns++
	turn := Turn{
		SessionID:          id,
		TurnNum:            s.turns,
		PrevLen:            prevLen,
		IncomingLen:        len(msgs),
		LCP:                lcp,
		Mutated:            mutated,
		MutationIndex:      mutIndex,
		PreservedPrefixPct: pct,
		ReprocessedTokens:  reprocessed,
		TotalTokens:        EstimateTokens(msgs),
	}

	s.canonical = cloneMessages(msgs)
	s.lastPrefix = len(msgs)
	return turn
}

// LongestCommonPrefix returns the number of leading messages a and b share, by
// content hash. This is the boundary up to which an upstream prefix cache built
// from a stays valid for b.
func LongestCommonPrefix(a, b []openai.Message) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i].Hash() == b[i].Hash() {
		i++
	}
	return i
}

// SessionID derives a stable identifier from the conversation's system message
// and first user message — the parts that anchor a session and rarely change.
// Falls back to the first message when neither role is present.
func SessionID(msgs []openai.Message) string {
	h := sha256.New()
	gotSystem, gotUser := false, false
	for _, m := range msgs {
		if !gotSystem && m.Role == "system" {
			h.Write([]byte("system\x00"))
			h.Write(m.Content)
			gotSystem = true
		}
		if !gotUser && m.Role == "user" {
			h.Write([]byte("user\x00"))
			h.Write(m.Content)
			gotUser = true
		}
		if gotSystem && gotUser {
			break
		}
	}
	if !gotSystem && !gotUser && len(msgs) > 0 {
		h.Write(msgs[0].Content)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// EstimateTokens approximates the token count of a message slice. Per
// mvp_plan.md §6, exact per-model tokenization is out of scope: the cache breaks
// at the first differing message regardless of token count, so a stable
// byte-based estimate (~4 bytes/token plus a small per-message overhead) is
// enough to size the wasted work.
func EstimateTokens(msgs []openai.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateMessageTokens(m openai.Message) int {
	n := len(m.Role) + len(m.Content) + len(m.Name) + len(m.ToolCalls) + len(m.ToolCallID)
	return n/4 + 4
}

func cloneMessages(msgs []openai.Message) []openai.Message {
	out := make([]openai.Message, len(msgs))
	copy(out, msgs)
	return out
}
