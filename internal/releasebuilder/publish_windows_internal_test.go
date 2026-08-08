//go:build windows

package releasebuilder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHeldPublicationShareDeleteStateMachine(t *testing.T) {
	root := fakeRoot(t)
	dist, err := openHeldDist(filepath.Join(root, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	defer dist.close()
	assertDirectoryRejectsDeleteProbe(t, dist.path)

	stage, err := dist.createStaging()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stage.close()
		_ = os.RemoveAll(stage.path)
	}()
	assertDirectorySharesDelete(t, stage.path)
	if !dist.stageMatches(stage) || !dist.pathMatches() {
		t.Fatal("staging or held dist identity did not match after DELETE-sharing probe")
	}

	payloadPath := filepath.Join(stage.path, "payload")
	if err := os.Mkdir(payloadPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payloadPath, "complete"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stage.preparePayload(); err != nil {
		t.Fatal(err)
	}
	assertDirectorySharesDelete(t, payloadPath)
	if !stage.payloadMatches() || !dist.stageMatches(stage) || !dist.pathMatches() {
		t.Fatal("publication identities did not all match after DELETE-sharing probe")
	}

	const leaf = "release-share-delete-test"
	finalPath := filepath.Join(dist.path, leaf)
	defer os.RemoveAll(finalPath)
	if err := dist.publish(stage, leaf, nil, nil); err != nil {
		t.Fatalf("publish held payload: %v", err)
	}
	entries, err := os.ReadDir(finalPath)
	if err != nil {
		t.Fatalf("path-based validation with transitioned payload handle: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "complete" {
		t.Fatalf("published entries = %v, want complete", entries)
	}
	if !dist.publishedMatches(stage, leaf) {
		t.Fatal("published payload identity did not match held payload")
	}
	if _, err := stage.payloadPath(); err != nil {
		t.Fatalf("resolve held published payload: %v", err)
	}

	if err := dist.cleanup(stage, true); err != nil {
		t.Fatalf("cleanup staging while published payload handle remains held: %v", err)
	}
	if stage.handle != windows.InvalidHandle {
		t.Fatal("cleanup left staging handle open")
	}
	if stage.payload == windows.InvalidHandle {
		t.Fatal("cleanup closed held published payload handle")
	}
	if _, err := os.Lstat(stage.path); !os.IsNotExist(err) {
		t.Fatalf("cleanup left staging path: %v", err)
	}
	if !dist.publishedMatches(stage, leaf) {
		t.Fatal("cleanup changed published payload identity")
	}
	if _, err := stage.payloadPath(); err != nil {
		t.Fatalf("published payload handle is not usable after staging cleanup: %v", err)
	}
	if err := stage.closePayload(); err != nil {
		t.Fatalf("close held published payload handle: %v", err)
	}
	if stage.payload != windows.InvalidHandle {
		t.Fatal("published payload handle remained open")
	}
	if err := os.RemoveAll(finalPath); err != nil {
		t.Fatalf("remove published payload after closing handle: %v", err)
	}
}

func assertDirectoryRejectsDeleteProbe(t *testing.T, path string) {
	t.Helper()
	probe, err := openDirectoryHandle(
		path,
		windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
	)
	if err == nil {
		_ = windows.CloseHandle(probe)
		t.Fatalf("held dist %q admitted a DELETE-access probe", path)
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("held dist DELETE-access probe error = %v, want ERROR_SHARING_VIOLATION", err)
	}
}

func assertDirectorySharesDelete(t *testing.T, path string) {
	t.Helper()
	probe, err := openDirectoryHandle(
		path,
		windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
	)
	if err != nil {
		t.Fatalf("open second DELETE handle for %q: %v", path, err)
	}
	if err := windows.CloseHandle(probe); err != nil {
		t.Fatalf("close DELETE-sharing probe for %q: %v", path, err)
	}
}
