// Package pin implements CachePin's milestone-m3 reconciliation: rewriting a
// request whose history the harness mutated back into an append-only extension
// of the canonical history, so the upstream server's KV Cache for the canonical
// prefix survives instead of being invalidated at the first changed message.
package pin

import (
	"github.com/SuperMarioYL/cachepin/internal/openai"
	"github.com/SuperMarioYL/cachepin/internal/session"
)

// Reconcile rewrites incoming so that it is an append-only extension of
// canonical, returning the reconciled messages and whether a rewrite occurred.
//
// When incoming already extends canonical (the LCP covers the whole canonical
// history), it is forwarded unchanged — the cache is intact and there is nothing
// to fix.
//
// When the harness rewrote or dropped a previously established message, the
// cached canonical prefix is preserved and only the genuinely new tail (the
// messages beyond the canonical length) is re-attached. A mutation that does not
// grow the message count has no recoverable new tail, so canonical is forwarded
// as-is: cache survival is prioritized over honoring an in-place edit, which is
// the explicit tradeoff of pin mode (see mvp_plan.md §2).
func Reconcile(canonical, incoming []openai.Message) ([]openai.Message, bool) {
	lcp := session.LongestCommonPrefix(canonical, incoming)
	if lcp == len(canonical) {
		return incoming, false
	}

	newCount := len(incoming) - len(canonical)
	if newCount < 0 {
		newCount = 0
	}

	reconciled := make([]openai.Message, 0, len(canonical)+newCount)
	reconciled = append(reconciled, canonical...)
	if newCount > 0 {
		reconciled = append(reconciled, incoming[len(incoming)-newCount:]...)
	}
	return reconciled, true
}
