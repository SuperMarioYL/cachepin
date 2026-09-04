// Package version is cachepin's single source of truth for the build version.
// The VERSION file at the repository root is the canonical version string; it
// is embedded at compile time so the binary carries it without depending on
// link-time injection (a `go build` from a checkout and a release build both
// report the same value the lockstep test asserts).
//
// v0.10.0 (fix-cli-missing-version-surface): .goreleaser.yaml injected the
// version via `-X main.version={{.Version}}`, but package main declared no
// `version` symbol, so the linker flag was a silent no-op (Go ignores -X on an
// absent symbol) and the release binary carried no version surface —
// `cachepin --version` was an unknown flag. Embedding VERSION here gives the
// binary a real version derived from the same file the lockstep test reads.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version is the project version, trimmed of surrounding whitespace. It is
// the single value the CLI --version flag prints and the version-lockstep test
// asserts equals the VERSION file, web/site.json content_version, and the
// CHANGELOG.md head.
var Version = strings.TrimSpace(raw)
