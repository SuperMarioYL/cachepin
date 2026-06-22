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
// cached canonical prefix is preserved and every message the harness sent beyond
// the common-prefix boundary is re-attached on top of it. This is the §2
// contract: the reconciled array is canonical[:lcp] + incoming[lcp:], except the
// preserved prefix is taken from the full canonical history (canonical[:lcp] is
// identical to incoming[:lcp] by definition of the LCP, so this equals
// canonical + incoming[lcp:]). Cache survival is prioritized over honoring an
// in-place edit, which is the explicit tradeoff of pin mode (see mvp_plan.md §2).
//
// Reconstructing from the LCP boundary — rather than slicing the last
// len(incoming)-len(canonical) messages — is what keeps genuinely-new turns
// intact when the harness drops an earlier message and appends new ones (context
// compaction): a last-N slice undercounts the new tail and silently drops needed
// turns whenever the mutation changes the message count.
func Reconcile(canonical, incoming []openai.Message) ([]openai.Message, bool) {
	lcp := session.LongestCommonPrefix(canonical, incoming)
	if lcp == len(canonical) {
		return incoming, false
	}

	tail := incoming[lcp:]
	reconciled := make([]openai.Message, 0, len(canonical)+len(tail))
	reconciled = append(reconciled, canonical...)
	reconciled = append(reconciled, tail...)
	return reconciled, true
}
