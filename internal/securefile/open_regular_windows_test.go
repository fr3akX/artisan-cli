//go:build windows

package securefile

import "testing"

func TestWindowsSnapshotPathRejectsRootRelativePath(t *testing.T) {
	for _, path := range []string{`\review.txt`, `/review.txt`} {
		if root, components, ok := windowsSnapshotPath(path); ok {
			t.Fatalf("windowsSnapshotPath(%q) = %q, %#v, true; root-relative path must not be reinterpreted relative to the current directory", path, root, components)
		}
	}
}
