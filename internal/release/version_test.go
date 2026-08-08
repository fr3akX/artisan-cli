package release

import "testing"

func TestInfoDevelopmentDefaults(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = oldVersion, oldCommit })
	Version, Commit = "", ""

	got := Info()
	if got.Version != "dev" || got.Commit != "unknown" {
		t.Fatalf("Info() = %#v, want development defaults", got)
	}
}

func TestNewBuildInfoRejectsEmptyValues(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
	}{
		{name: "empty version", commit: "abc123"},
		{name: "empty commit", version: "v1.0.0"},
		{name: "blank version", version: "  ", commit: "abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newBuildInfo(tt.version, tt.commit); err == nil {
				t.Fatal("newBuildInfo() error = nil, want error")
			}
		})
	}
}

func TestInfoUsesBuildValues(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = oldVersion, oldCommit })
	Version, Commit = "v1.2.3", "abc123"

	got := Info()
	if got.Version != Version || got.Commit != Commit {
		t.Fatalf("Info() = %#v, want version %q and commit %q", got, Version, Commit)
	}
}
