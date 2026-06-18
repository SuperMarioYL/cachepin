// Command benchmark demonstrates milestone m3. It replays a fixed multi-turn
// coding-agent transcript in which the harness rewrites an early message every
// turn (simulating a re-rendered tool result or context compaction), and runs it
// twice through CachePin's tracker:
//
//   - no-pin: the mutated history is forwarded verbatim, so the upstream prefix
//     (KV Cache) breaks at the rewritten message and a growing number of tokens
//     are reprocessed every turn;
//   - pin: each request is reconciled to an append-only extension of the
//     canonical history first, so the cache survives and reprocessing stays ~0.
//
// It writes per-turn chart data as CSV (turn, reprocessed_no_pin,
// reprocessed_pin, and running cumulative totals) and prints a savings summary
// to stderr. Run it with: go run ./bench  (optionally -turns N -out chart.csv).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SuperMarioYL/cachepin/internal/openai"
	"github.com/SuperMarioYL/cachepin/internal/pin"
	"github.com/SuperMarioYL/cachepin/internal/session"
)

type row struct {
	turn        int
	reprocNoPin int
	reprocPin   int
	cumNoPin    int
	cumPin      int
}

func main() {
	turns := flag.Int("turns", 50, "number of conversation turns to replay")
	out := flag.String("out", "", "CSV output path (default: stdout)")
	flag.Parse()

	if *turns < 1 {
		fmt.Fprintln(os.Stderr, "benchmark: -turns must be >= 1")
		os.Exit(2)
	}

	rows := run(*turns)

	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "benchmark:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	writeCSV(w, rows)
	writeSummary(os.Stderr, rows)
}

func run(turns int) []row {
	noPin := session.NewTracker()
	pinned := session.NewTracker()

	history := seedHistory()           // grows append-only as the "true" conversation
	var canonical []openai.Message     // pin's append-only ground truth
	rows := make([]row, 0, turns)
	var cumNo, cumPin int

	for i := 1; i <= turns; i++ {
		// The model answers and the user follows up: two messages appended.
		history = append(history,
			assistant(fmt.Sprintf("Here is my response for turn %d. %s", i, filler())),
			user(fmt.Sprintf("Thanks — follow-up question %d. %s", i, filler())),
		)

		// The harness rewrites an early message before resending (the cache-buster).
		mutated := mutate(history, i)

		// No pin: forward the mutated history as-is.
		tn := noPin.Observe(clone(mutated))
		cumNo += tn.ReprocessedTokens

		// Pin: reconcile to an append-only extension of canonical first.
		reconciled, _ := pin.Reconcile(canonical, mutated)
		tp := pinned.Observe(clone(reconciled))
		cumPin += tp.ReprocessedTokens
		canonical = clone(reconciled)

		rows = append(rows, row{
			turn:        i,
			reprocNoPin: tn.ReprocessedTokens,
			reprocPin:   tp.ReprocessedTokens,
			cumNoPin:    cumNo,
			cumPin:      cumPin,
		})
	}
	return rows
}

// mutate rewrites the assistant message at index 2 (an early, large tool result)
// with a turn-specific revision, breaking any prefix cache at that boundary.
func mutate(msgs []openai.Message, turn int) []openai.Message {
	out := clone(msgs)
	if len(out) > 2 {
		out[2] = assistant(fmt.Sprintf("re-rendered tool output, revision %d. %s", turn, filler()))
	}
	return out
}

func writeCSV(w io.Writer, rows []row) {
	fmt.Fprintln(w, "turn,reprocessed_no_pin,reprocessed_pin,cumulative_no_pin,cumulative_pin")
	for _, r := range rows {
		fmt.Fprintf(w, "%d,%d,%d,%d,%d\n", r.turn, r.reprocNoPin, r.reprocPin, r.cumNoPin, r.cumPin)
	}
}

func writeSummary(w io.Writer, rows []row) {
	if len(rows) == 0 {
		return
	}
	last := rows[len(rows)-1]
	saved := last.cumNoPin - last.cumPin
	pct := 0.0
	if last.cumNoPin > 0 {
		pct = float64(saved) / float64(last.cumNoPin) * 100
	}
	fmt.Fprintf(w, "\n%d turns replayed\n", len(rows))
	fmt.Fprintf(w, "  reprocessed tokens (no pin): %d\n", last.cumNoPin)
	fmt.Fprintf(w, "  reprocessed tokens (pin):    %d\n", last.cumPin)
	fmt.Fprintf(w, "  saved by --pin:              %d (%.1f%%)\n", saved, pct)
}

func seedHistory() []openai.Message {
	return []openai.Message{
		system("You are a helpful coding agent working in a Go repository. " + filler()),
		user("Help me refactor the proxy package. " + filler()),
	}
}

func filler() string {
	// Pad messages so reprocessed-token counts are on a realistic scale; the
	// exact text is irrelevant — only its size and stability matter.
	return strings.Repeat("context line that occupies space in the prompt. ", 40)
}

func system(s string) openai.Message    { return openai.Message{Role: "system", Content: jsonString(s)} }
func user(s string) openai.Message      { return openai.Message{Role: "user", Content: jsonString(s)} }
func assistant(s string) openai.Message { return openai.Message{Role: "assistant", Content: jsonString(s)} }

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func clone(msgs []openai.Message) []openai.Message {
	out := make([]openai.Message, len(msgs))
	copy(out, msgs)
	return out
}
