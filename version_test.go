package version

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestVersionLockstep is the regression test for fix-site-content-version-drift
// and fix-cli-missing-version-surface: every version surface the project
// exposes MUST agree on one value — the embedded version.Version, the VERSION
// file, web/site.json content_version, and the CHANGELOG.md head. On the
// v0.9.0 tag this test fails two ways (content_version "v0.7.0" != VERSION
// "0.9.0", and the version package did not exist), which proves the drift was
// real and unguarded. It runs from the repository root (the version package's
// directory), so the version surfaces are read by their repo-relative paths.
//
// quality-lockstep-guard-site-changelog (v0.12.0) adds the site's own changelog
// head: web/site.json carries a `changelog` array whose first entry is the
// version the live site renders in its changelog list — a second, independently
// editable copy of the same fact. It is the surface that historically drifted
// (the whole site sat at v0.7.0 across the v0.8.0 and v0.9.0 releases), so a
// release that bumped content_version but forgot this entry would ship an
// internally inconsistent site with green CI. Asserting it here makes the guard
// cover every copy of the version, not three of four.
func TestVersionLockstep(t *testing.T) {
	// 1. The canonical VERSION file (the embed source).
	versionFile := mustReadTrim(t, "VERSION")

	// 2. web/site.json content_version — the version surface the live Pages
	//    site reads. It uses the same no-v-prefix format as the VERSION file
	//    (a v prefix here is itself drift; the fix normalized it).
	contentVersion := siteContentVersion(t, "web/site.json")

	// 3. CHANGELOG.md head — the topmost "## [X.Y.Z]" entry.
	changelogHead := changelogHeadVersion(t, "CHANGELOG.md")

	// 4. version.Version — what the binary embeds and --version prints.
	embedded := Version

	want := versionFile
	for label, got := range map[string]string{
		"VERSION file":                  versionFile,
		"web/site.json content_version": contentVersion,
		"CHANGELOG.md head":             changelogHead,
		"embedded version.Version":      embedded,
	} {
		if got != want {
			t.Errorf("version drift: %s = %q, want %q (matches VERSION)", label, got, want)
		}
		if got == "" {
			t.Errorf("version surface %s is empty", label)
		}
	}

	// 5. web/site.json changelog head — the entry the live site renders. It
	//    carries the v prefix (a site-facing label), so it compares as
	//    "v" + VERSION.
	if got := siteChangelogHeadVersion(t, "web/site.json"); got != "v"+want {
		t.Errorf("version drift: web/site.json changelog head = %q, want %q (\"v\" + VERSION)", got, "v"+want)
	}
}

// TestVersionIsSemverShape guards against a malformed version sneaking through
// every surface at once (the lockstep test above would pass on empty/garbage if
// all surfaces were equally broken). A leading "v" is rejected so a v-prefix
// regression on content_version (the historical drift) is caught independently.
func TestVersionIsSemverShape(t *testing.T) {
	if strings.HasPrefix(Version, "v") {
		t.Errorf("version.Version = %q must not carry a leading 'v' (matches VERSION file format); the v prefix is exactly the site.json drift that was fixed", Version)
	}
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		t.Fatalf("version.Version = %q is not a three-part semver (X.Y.Z); got %d parts", Version, len(parts))
	}
	for _, p := range parts {
		if p == "" {
			t.Errorf("version.Version = %q has an empty semver segment", Version)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				t.Errorf("version.Version = %q has a non-numeric semver segment %q", Version, p)
			}
		}
	}
}

func mustReadTrim(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

func siteContentVersion(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var s struct {
		ContentVersion string `json:"content_version"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse %s as JSON: %v", path, err)
	}
	return strings.TrimSpace(s.ContentVersion)
}

// siteChangelogHeadVersion returns the version label of the newest entry in
// web/site.json's `changelog` array — the value the live site renders. It is a
// distinct surface from content_version: the site shows content_version in its
// header and the changelog array in its changelog list, and only the former was
// guarded before v0.12.0.
func siteChangelogHeadVersion(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var s struct {
		Changelog []struct {
			Version string `json:"version"`
		} `json:"changelog"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse %s as JSON: %v", path, err)
	}
	if len(s.Changelog) == 0 {
		t.Fatalf("%s has an empty changelog array; the newest release must be listed", path)
	}
	return strings.TrimSpace(s.Changelog[0].Version)
}

func changelogHeadVersion(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "## [") {
			continue
		}
		start := strings.Index(l, "[")
		end := strings.Index(l, "]")
		if start < 0 || end < 0 || end < start {
			continue
		}
		return strings.TrimSpace(l[start+1 : end])
	}
	t.Fatalf("no `## [version]` entry found in %s", path)
	return ""
}
