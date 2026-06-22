// Package metrics turns a session.Turn into output: a one-line human-readable
// summary for the proxy's terminal and an optional machine-readable NDJSON
// record (one JSON object per line) that the benchmark and dashboards consume.
package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/SuperMarioYL/cachepin/internal/session"
)

// Reporter writes per-turn metrics. The human writer receives the terminal
// line; the optional nd writer receives NDJSON. Either may be nil.
type Reporter struct {
	human io.Writer
	nd    io.Writer
	now   func() time.Time
}

// NewReporter builds a Reporter. Pass nil for nd to skip NDJSON output, or nil
// for human to skip the terminal line.
func NewReporter(human, nd io.Writer) *Reporter {
	return &Reporter{human: human, nd: nd, now: time.Now}
}

// record is the NDJSON shape emitted per turn.
type record struct {
	TS                 string  `json:"ts"`
	SessionID          string  `json:"session_id"`
	Turn               int     `json:"turn"`
	PreservedPrefixPct float64 `json:"preserved_prefix_pct"`
	ReprocessedTokens  int     `json:"reprocessed_tokens"`
	TotalTokens        int     `json:"total_tokens"`
	Mutated            bool    `json:"mutated"`
	MutationIndex      int     `json:"mutation_index"`
	PrevLen            int     `json:"prev_len"`
	IncomingLen        int     `json:"incoming_len"`
	LCP                int     `json:"lcp"`
	// Layout fields are the m4 context-layout linter output: the exact byte
	// offset and message field where the cache prefix first diverged. They are
	// omitted on a clean append (LayoutDiverged false).
	LayoutDiverged   bool   `json:"layout_diverged"`
	LayoutByteOffset int    `json:"layout_byte_offset,omitempty"`
	LayoutMsgIndex   int    `json:"layout_msg_index,omitempty"`
	LayoutField      string `json:"layout_field,omitempty"`
}

// Report writes the turn to both configured sinks.
func (r *Reporter) Report(t session.Turn) error {
	if r.human != nil {
		if _, err := fmt.Fprintln(r.human, HumanLine(t)); err != nil {
			return fmt.Errorf("metrics: write human line: %w", err)
		}
	}
	if r.nd != nil {
		rec := record{
			TS:                 r.now().UTC().Format(time.RFC3339),
			SessionID:          t.SessionID,
			Turn:               t.TurnNum,
			PreservedPrefixPct: round1(t.PreservedPrefixPct),
			ReprocessedTokens:  t.ReprocessedTokens,
			TotalTokens:        t.TotalTokens,
			Mutated:            t.Mutated,
			MutationIndex:      t.MutationIndex,
			PrevLen:            t.PrevLen,
			IncomingLen:        t.IncomingLen,
			LCP:                t.LCP,
			LayoutDiverged:     t.Layout.Diverged,
			LayoutByteOffset:   t.Layout.ByteOffset,
			LayoutMsgIndex:     t.Layout.MessageIndex,
			LayoutField:        t.Layout.Field,
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("metrics: marshal ndjson: %w", err)
		}
		if _, err := fmt.Fprintln(r.nd, string(b)); err != nil {
			return fmt.Errorf("metrics: write ndjson: %w", err)
		}
	}
	return nil
}

// HumanLine renders the terminal summary, e.g.
//
//	turn 12 | prefix preserved 100% | 0 tokens reprocessed
//	turn 13 | prefix preserved 41% | ~31k tokens reprocessed | MUTATION at msg[3]
func HumanLine(t session.Turn) string {
	line := fmt.Sprintf("turn %d | prefix preserved %.0f%% | %s tokens reprocessed",
		t.TurnNum, t.PreservedPrefixPct, humanizeTokens(t.ReprocessedTokens))
	if t.Mutated {
		line += fmt.Sprintf(" | MUTATION at msg[%d]", t.MutationIndex)
	}
	// m4 linter: when the layout diff pinpoints a within-message break, name the
	// exact byte offset and the field that broke prefix-stability.
	if t.Layout.Diverged && t.Layout.Field != "" && t.Layout.Field != "message-count" {
		line += fmt.Sprintf(" | %s broke prefix at byte %d", t.Layout.Field, t.Layout.ByteOffset)
	}
	return line
}

// humanizeTokens formats a token count compactly: exact below 1000, "~1.2k"
// between 1k and 10k, "~31k" above.
func humanizeTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10000:
		return fmt.Sprintf("~%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("~%dk", n/1000)
	}
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
