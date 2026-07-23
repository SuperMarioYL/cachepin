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
	"time"

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
	// PinCoverageLost is set by the --pin interceptor (not the tracker itself)
	// when the session's canonical was lost to eviction, so the turn was forwarded
	// unreconciled despite --pin. It surfaces the silent-green-while-reprocessing
	// case in both the human line and NDJSON (v0.4.0).
	PinCoverageLost bool
}

// Session is the append-only ground truth for one conversation.
type Session struct {
	ID         string
	canonical  []openai.Message
	lastPrefix int
	turns      int
	// lastSeen is updated on every Observe and read by idle-TTL eviction so the
	// tracker can drop sessions that have gone quiet even when no new session
	// arrives to push them out under the LRU count cap (v0.4.0 --idle-ttl).
	lastSeen time.Time
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
//
// v0.4.0 adds two eviction refinements: an idle TTL (idleTTL) so sessions that
// have gone quiet are dropped on the next Observe even without new traffic, and
// a bounded evicted set so --pin can warn (via WasEvicted) when a returning
// session's canonical was lost to eviction instead of silently voiding the pin
// guarantee with green metrics.
type Tracker struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	order       *list.List    // front = most recently used; back = next eviction victim
	maxSessions int           // 0 = unbounded
	idleTTL     time.Duration // 0 = no idle expiry; only the count cap applies
	// evicted records session ids that were dropped by eviction but may return;
	// it lets --pin distinguish "first-ever request" from "returning after
	// eviction" so it can warn instead of reporting silent green metrics. It is
	// bounded (evictedCap) so churn does not leak it.
	evicted    map[string]struct{}
	evictedCap int
	now        func() time.Time
}

// NewTracker returns a Tracker with the default max-sessions cap and no idle TTL.
func NewTracker() *Tracker {
	return NewTrackerWithMaxTTL(DefaultMaxSessions, 0)
}

// NewTrackerWithMax returns a Tracker whose sessions map is bounded to max
// sessions via LRU eviction. A non-positive max disables eviction (unbounded),
// which is useful for tests and short-lived processes that never risk the leak.
// Idle TTL is disabled (0); use NewTrackerWithMaxTTL to enable it.
func NewTrackerWithMax(max int) *Tracker {
	return NewTrackerWithMaxTTL(max, 0)
}

// NewTrackerWithMaxTTL returns a Tracker bounded by both a max-sessions LRU cap
// and a per-session idle TTL. Either may be zero to disable that bound: a
// non-positive max means no count cap, and a non-positive idleTTL means idle
// sessions are only evicted when a new session pushes them out under the count
// cap. The now hook lets tests pin the clock.
func NewTrackerWithMaxTTL(max int, idleTTL time.Duration) *Tracker {
	cap := max
	if cap <= 0 {
		cap = DefaultMaxSessions
	}
	return &Tracker{
		sessions:    make(map[string]*Session),
		order:       list.New(),
		maxSessions: max,
		idleTTL:     idleTTL,
		evicted:     make(map[string]struct{}),
		evictedCap:  cap,
		now:         time.Now,
	}
}

// Observe records an incoming request's message array and returns the per-turn
// report. The request's messages become the new canonical history, so the next
// turn is diffed against what the harness most recently sent.
func (t *Tracker) Observe(msgs []openai.Message) Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observeLocked(SessionID(msgs), msgs)
}

// observeLocked is the lock-held core of Observe (and ReconcileAndObserve): it
// looks up (or creates) the session for sid, updates its LRU recency, diffs msgs
// against the prior canonical history, builds the per-turn report, and stores
// msgs as the new canonical. The caller MUST hold t.mu. Extracted (v0.5.0) so
// the --pin path can observe the reconciled array under the same lock it read
// the canonical under (the same-session race fix) without re-locking or
// duplicating this body.
func (t *Tracker) observeLocked(sid string, msgs []openai.Message) Turn {
	now := t.now()
	s := t.sessions[sid]
	if s == nil {
		s = &Session{ID: sid, lastSeen: now}
		s.elem = t.order.PushFront(s)
		t.sessions[sid] = s
		// A returning-after-eviction session is no longer "evicted" now that it
		// is live again — clear the flag so a future eviction re-arms the
		// --pin warning rather than a stale one persisting.
		delete(t.evicted, sid)
	} else {
		// Mark this session most-recently-used so the LRU victim is the one
		// idle the longest, not merely the oldest insertion.
		t.order.MoveToFront(s.elem)
		s.lastSeen = now
	}
	// Idle-TTL + count-cap eviction runs on EVERY Observe (v0.5.0 fix
	// fix-idle-ttl-skips-existing-session-observe): the v0.4.0 path only swept
	// inside the new-session branch, so a deployment where only sticky sessions
	// return (no new session ever arrives) never evicted idle back-of-list
	// sessions — an unbounded leak under `--max-sessions 0 --idle-ttl 10m` (the
	// exact unbounded-count config m5 targets). The just-touched session is at
	// the front with lastSeen=now, so it is never a victim; idle sessions
	// expire on the next Observe of any session, meeting the m5 idle-TTL
	// contract.
	t.evictIfNeeded(now)

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
		SessionID:          sid,
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

// ReconcileAndObserve runs the --pin interceptor's Canonical read, the reconcile
// callback, and the Observe store in a single critical section (v0.5.0,
// fix-same-session-pin-reconcile-race). The pre-v0.5.0 --pin path read
// Canonical (lock-clone-unlock), ran pin.Reconcile unlocked, then Observe
// (re-lock-store): two concurrent goroutines for the SAME session id could
// interleave in that window — both read the same stale prior, each reconciled
// against it, and the later Observe overwrote the earlier, dropping a turn from
// the canonical history so the NEXT turn's Reconcile was computed against the
// wrong ground truth (silent turn-loss / wrong pin rewrite). Holding t.mu across
// all three closes the TOCTOU; this is the "real but narrow same-session
// concurrent race" the v0.4.0 changelog deferred to v0.5.0 as too big for the
// time budget, scoped here to one new method (no architecture change).
//
// reconcile is called with a CLONE of the session's current canonical (nil if
// the session is unknown or was evicted) and returns the reconciled messages
// plus whether it rewrote the request. The interceptor passes a closure over
// pin.Reconcile — pin is a pure function, so passing it as a callback keeps the
// pin -> session import edge the only one (no session -> pin cycle). The
// closure MUST NOT call back into the tracker's public locking methods
// (Canonical/WasEvicted/Observe): this method holds t.mu for the whole call, so
// a re-entrant lock would deadlock. PinCoverageLost (prior was nil because the
// session was evicted since the last turn) is computed here, inside the
// critical section, and set on the returned Turn — so the v0.4.0
// silent-green-while-reprocessing warning stays correct under concurrency.
//
// The reconciled array is what's observed and stored as canonical, matching
// bench/benchmark.go's pinned tracker semantics, so the next turn's Reconcile
// is computed against the just-stored ground truth. Done = a -race run under
// two concurrent same-session --pin requests keeps both turns in canonical (no
// overwrite, no turn loss).
func (t *Tracker) ReconcileAndObserve(sid string, incoming []openai.Message, reconcile func(prior []openai.Message) (reconciled []openai.Message, changed bool)) Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.sessions[sid]
	var prior []openai.Message
	wasEvicted := false
	if s != nil {
		prior = cloneMessages(s.canonical)
	} else if _, ok := t.evicted[sid]; ok {
		wasEvicted = true
	}

	// incoming is the interceptor's reference array (the closure may close over
	// it); the reconciled array the callback returns is what we store + observe.
	reconciled, _ := reconcile(prior)
	coverageLost := prior == nil && wasEvicted

	// Observe the reconciled array under the same lock we read the canonical
	// under — observeLocked does the session lookup/recency/Turn/store, so this
	// is the single critical section that closes the same-session TOCTOU.
	turn := t.observeLocked(sid, reconciled)
	turn.PinCoverageLost = coverageLost
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

// WasEvicted reports whether sid was dropped by eviction and has not yet been
// re-observed. It lets the --pin interceptor distinguish a first-ever request
// (no prior canonical, no warning) from a returning-after-eviction request
// (prior canonical lost — pin coverage was just voided, warn the user). It is
// best-effort: the evicted set is bounded, so under extreme churn a very old
// eviction may have aged out of the set.
func (t *Tracker) WasEvicted(sid string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.evicted[sid]
	return ok
}

// evictIfNeeded enforces the idle-TTL and max-sessions bounds by dropping
// least-recently-used sessions. Called with t.mu held. Idle-TTL eviction runs
// whenever idleTTL > 0 (even under an unbounded count cap) so quiet sessions
// expire without new traffic; the count cap runs whenever maxSessions > 0. Each
// victim's id is recorded in the evicted set (bounded) so WasEvicted can warn
// --pin on return.
func (t *Tracker) evictIfNeeded(now time.Time) {
	// Idle TTL: drop back-of-list sessions whose lastSeen is older than idleTTL.
	// The order list is least-recently-observed-ordered thanks to MoveToFront,
	// so the back is always the idlest live session.
	if t.idleTTL > 0 {
		for {
			back := t.order.Back()
			if back == nil {
				break
			}
			victim := back.Value.(*Session)
			if now.Sub(victim.lastSeen) <= t.idleTTL {
				break // back is still fresh; everything in front of it is fresher
			}
			t.order.Remove(back)
			delete(t.sessions, victim.ID)
			t.recordEvicted(victim.ID)
		}
	}
	// Count cap: drop LRU sessions until the map is at or below the cap.
	if t.maxSessions > 0 {
		for len(t.sessions) > t.maxSessions {
			back := t.order.Back()
			if back == nil {
				return
			}
			victim := back.Value.(*Session)
			t.order.Remove(back)
			delete(t.sessions, victim.ID)
			t.recordEvicted(victim.ID)
		}
	}
}

// recordEvicted adds sid to the bounded evicted set. If the set is at its cap,
// an arbitrary entry is dropped first so churn does not leak it (map iteration
// order is unspecified in Go, so the victim is non-deterministic — acceptable
// for a best-effort warning set).
func (t *Tracker) recordEvicted(sid string) {
	if t.evictedCap <= 0 {
		return
	}
	if len(t.evicted) >= t.evictedCap {
		for k := range t.evicted {
			delete(t.evicted, k)
			break
		}
	}
	t.evicted[sid] = struct{}{}
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
