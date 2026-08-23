//go:build linux

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadRoastChartFenceCleanupFailureReportsLocalStorageError(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	destination := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(validRoastDetailJSON(), roastSHA256, strings.Repeat("e", 64), 1)
	client := chartClientWithDetails(t, compressed, []string{
		validRoastDetailJSON(),
		validRoastDetailJSON(),
		changed,
	}, nil)
	client.downloadOps.forceBackupReplace = true
	var cleanupNames []string
	client.downloadOps.afterCleanupCheck = func(_ *downloadTarget, name string) error {
		if strings.Contains(name, ".candidate-") || strings.Contains(name, ".backup-") {
			cleanupNames = append(cleanupNames, name)
			return errors.New("injected cleanup failure")
		}
		return nil
	}
	var nativeCalls atomic.Int32
	client.downloadOps.nativeOperation = func(operation func() error) error {
		nativeCalls.Add(1)
		return operation()
	}

	result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, true)
	if result != (RoastChartDownload{}) || failure == nil || failure.Code != "local_storage_error" || failure.ExitCode != 3 || strings.Contains(failure.Message, "revision") {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if nativeCalls.Load() != 1 {
		t.Fatalf("native calls=%d", nativeCalls.Load())
	}
	var sawCandidate, sawBackup bool
	for _, name := range cleanupNames {
		sawCandidate = sawCandidate || strings.Contains(name, ".candidate-")
		sawBackup = sawBackup || strings.Contains(name, ".backup-")
	}
	if !sawCandidate || !sawBackup {
		t.Fatalf("cleanup names=%v", cleanupNames)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing" {
		t.Fatalf("destination=%q,%v", contents, err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(destination) {
			_ = os.Remove(filepath.Join(filepath.Dir(destination), entry.Name()))
		}
	}
}
