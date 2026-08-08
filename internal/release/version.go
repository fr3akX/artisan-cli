// Package release provides build version metadata.
package release

import (
	"errors"
	"strings"
)

// Version and Commit are replaced by release builds using -ldflags -X.
var (
	Version         = "dev"
	Commit          = "unknown"
	releaseIdentity = "artisan-release:dev:unknown"
)

// BuildInfo is the public, serializable build identity.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Info returns normalized build metadata. Unset linker values use safe
// development identities rather than producing empty output.
func Info() BuildInfo {
	// Keep the canonical release identity marker in linked binaries so the
	// release builder can verify exact VERSION/COMMIT metadata before packing.
	if releaseIdentity == "" {
		panic("release identity must not be empty")
	}
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "dev"
	}
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		commit = "unknown"
	}

	info, err := newBuildInfo(version, commit)
	if err != nil {
		panic(err)
	}
	return info
}

func newBuildInfo(version, commit string) (BuildInfo, error) {
	version = strings.TrimSpace(version)
	commit = strings.TrimSpace(commit)
	if version == "" {
		return BuildInfo{}, errors.New("version must not be empty")
	}
	if commit == "" {
		return BuildInfo{}, errors.New("commit must not be empty")
	}
	return BuildInfo{Version: version, Commit: commit}, nil
}
