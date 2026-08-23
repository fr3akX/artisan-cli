//go:build windows

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWindowsDownloadInventoryImageFlushesExactRenamedHandleWithoutParentSync(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "durable.webp")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "durable")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	defaults := client.downloadOps
	var events []string
	client.downloadOps.syncFile = func(file *os.File) error { events = append(events, "sync-file"); return defaults.syncFile(file) }
	client.downloadOps.closeFile = func(file *os.File) error { events = append(events, "close-writer"); return defaults.closeFile(file) }
	client.downloadOps.nativeOperation = func(operation func() error) error { events = append(events, "native"); return operation() }
	client.downloadOps.flushFile = func(file *os.File) error {
		events = append(events, "flush-exact-handle")
		// The writer close has already run, so a successful Stat proves the
		// independently retained publication handle is the one being flushed.
		_, err := file.Stat()
		return err
	}
	client.downloadOps.syncParent = func(string) error { return errors.New("syncParent must not run on Windows") }
	if _, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false); failure != nil {
		t.Fatal(failure)
	}
	if !reflect.DeepEqual(events, []string{"sync-file", "close-writer", "native", "flush-exact-handle"}) {
		t.Fatalf("events=%v", events)
	}
}

func TestWindowsDownloadInventoryImageFlushUncertaintyPreservesVisibleResult(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "visible.webp")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "installed")
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "secret", time.Second)
	client.downloadOps.flushFile = func(*os.File) error { return errors.New("flush") }
	result, failure := client.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false)
	assertLocalStorageFailure(t, failure, "The image download is installed, but storage durability is uncertain")
	if result.Path != destination || result.Bytes != int64(len("installed")) {
		t.Fatalf("result=%#v", result)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "installed" {
		t.Fatalf("destination=%q,%v", contents, err)
	}
}
