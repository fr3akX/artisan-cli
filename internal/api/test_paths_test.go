package api

import (
	"path/filepath"
	"testing"
)

// canonicalTestTempDir preserves testing's cleanup ownership while removing
// trusted platform temp-root aliases such as Darwin's /var -> /private/var.
func canonicalTestTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize test temporary directory: %v", err)
	}
	return canonical
}
