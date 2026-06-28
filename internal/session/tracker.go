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
	"container/list"
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
	// Layout is the byte-level context-layout diff against the prior canonical
	// history: the exact byte offset and message field where the cache prefix
	// first diverged. On a clean append Layout.Diverged is false. It is the m4
	// linter output and deepens the message-level Mutated/MutationIndex with the
	// precise field (system prompt, tool schema, ordering, whitespace) that broke
	// prefix-stability.
	Layout openai.LayoutDiff
}

// Session is the append-only ground truth for one conversation.
type Session struct {
	ID         string
	canonical  []openai.Message
	lastPrefix int
	turns      int
	// elem is this session's node in the Tracker's LRU order list, kept so
	// eviction and recency updates are O(1).
	elem *list.Element
}

// DefaultMaxSessions bounds the number of conversations a long-lived proxy
// tracks at once. Past it the least-recently-used session is evicted, so memory
// stays bounded under a shared/team deployment that starts fresh sessions per
// task. Each entry pins the full canonical message history (hundreds of KB to
// MB for a long coding conversation), so without a cap the map leaks forever.
const DefaultMaxSessions = 1024

// Tracker observes chat-completions requests and maintains a Session per
// conversation. It is safe for concurrent use; the proxy may serve overlapping
// requests for different sessions. Its sessions map is bounded by an LRU cap
// (maxSessions) so a long-lived proxy does not leak memory, and it is the single
// owner of the reconciled-canonical store that pin mode reads from — folding
// that store in here (v0.3.0) removed a parallel, unguarded map from main that
// crashed under concurrent multi-session traffic.
type Tracker struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	order       *list.List // front = most recently used; back = next eviction victim
	maxSessions int        // 0 = unbounded
}

// NewTracker returns a Tracker with the default max-sessions cap.
func NewTracker() *Tracker {
	return NewTrackerWithMax(DefaultMaxSessions)
}

// NewTrackerWithMax returns a Tracker whose sessions map is bounded to max
// sessions via LRU eviction. A non-positive max disables eviction (unbounded),
// which is useful for tests and short-lived processes that never risk the leak.
func NewTrackerWithMax(max int) *Tracker {
	return &Tracker{
		sessions:    make(map[string]*Session),
		order:       list.New(),
		maxSessions: max,
	}
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
		s.elem = t.order.PushFront(s)
		t.sessions[id] = s
		t.evictIfNeeded()
	} else {
		// Mark this session most-recently-used so the LRU victim is the one
		// idle the longest, not merely the oldest insertion.
		t.order.MoveToFront(s.elem)
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

	// m4 context-layout linter: byte-level diff against the prior canonical
	// history. Always call it — the first turn (empty canonical) and any clean
	// append both resolve to the NoDivergence sentinel, so every turn emits the
	// same NDJSON field set. The v0.2.0 path skipped the first turn and left a
	// zero-value LayoutDiff, which dropped layout_msg_index from turn 1's NDJSON.
	layout := openai.LintLayout(s.canonical, msgs)

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
		Layout:             layout,
	}

	s.canonical = cloneMessages(msgs)
	s.lastPrefix = len(msgs)
	return turn
}

// Canonical returns a clone of the canonical message history the tracker holds
// for sid, or nil if the session is unknown (never observed, or evicted). It is
// the pin reconciler's source of the pre-mutation ground truth: reading it from
// the tracker rather than a parallel map gives eviction a single owner and
// removes the unguarded map that crashed under concurrent multi-session traffic.
func (t *Tracker) Canonical(sid string) []openai.Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[sid]
	if s == nil {
		return nil
	}
	return cloneMessages(s.canonical)
}

// Len returns the number of sessions currently tracked, which the max-sessions
// cap keeps bounded. Intended for ops/diagnostics and tests.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}

// evictIfNeeded enforces the max-sessions cap by dropping least-recently-used
// sessions until the map is at or below the cap. Called with t.mu held.
func (t *Tracker) evictIfNeeded() {
	if t.maxSessions <= 0 {
		return
	}
	for len(t.sessions) > t.maxSessions {
		back := t.order.Back()
		if back == nil {
			return
		}
		victim := back.Value.(*Session)
		t.order.Remove(back)
		delete(t.sessions, victim.ID)
	}
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
