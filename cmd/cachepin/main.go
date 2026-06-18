// Command cachepin is a harness-neutral, OpenAI-compatible reverse proxy that
// keeps a coding agent's KV Cache alive across turns. Drop it between your
// coding-agent harness and your model server, point OPENAI_BASE_URL at it, and
// it reports (and, with --pin, protects) the server-side prefix cache.
//
// This file is the entry point: it parses flags and hands a validated Config to
// the proxy. The proxy itself lives in internal/proxy (milestone m1).
package main

import (
	"flag"
	"fmt"
	"os"
)

// Config holds the runtime configuration parsed from flags.
type Config struct {
	// Upstream is the base URL of the OpenAI-compatible model server, e.g.
	// http://localhost:8080.
	Upstream string
	// Listen is the address CachePin's proxy binds to, e.g. :8089.
	Listen string
	// Pin enables reconciliation of mutated requests back to append-only form
	// so the upstream KV Cache survives.
	Pin bool
	// NDJSON, when set, writes one machine-readable metrics object per turn to
	// this file path (in addition to the human-readable stdout line).
	NDJSON string
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "cachepin:", err)
		os.Exit(2)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "cachepin:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("cachepin", flag.ContinueOnError)
	var cfg Config
	fs.StringVar(&cfg.Upstream, "upstream", "", "base URL of the OpenAI-compatible model server (required), e.g. http://localhost:8080")
	fs.StringVar(&cfg.Listen, "listen", ":8089", "address to listen on")
	fs.BoolVar(&cfg.Pin, "pin", false, "reconcile mutated requests to append-only form to preserve the upstream KV cache")
	fs.StringVar(&cfg.NDJSON, "ndjson", "", "optional path to write per-turn metrics as NDJSON")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Upstream == "" {
		return Config{}, fmt.Errorf("--upstream is required (e.g. --upstream http://localhost:8080)")
	}
	return cfg, nil
}

// run starts the proxy. The transparent reverse proxy is implemented in
// milestone m1 (internal/proxy); this scaffold validates configuration and
// reports it so the binary builds and runs end to end.
func run(cfg Config) error {
	fmt.Printf("cachepin listening on %s -> upstream %s (pin=%v)\n", cfg.Listen, cfg.Upstream, cfg.Pin)
	fmt.Println("proxy not yet wired (m1) — flags parsed successfully")
	return nil
}
