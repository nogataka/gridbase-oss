package buildinfo

import "testing"

func TestCurrentDefaults(t *testing.T) {
	info := Current()
	if info.Version != "dev" {
		t.Fatalf("expected default version 'dev', got %q", info.Version)
	}
	if info.CommitSHA != "unknown" {
		t.Fatalf("expected default commit 'unknown', got %q", info.CommitSHA)
	}
	if info.BuildTime != "unknown" {
		t.Fatalf("expected default build time 'unknown', got %q", info.BuildTime)
	}
}
