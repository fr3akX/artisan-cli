package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyHeldDownloadCandidateRegistersBeforePartialCopySyncAndHashFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*downloadOperations)
	}{
		{
			name: "partial copy",
			inject: func(ops *downloadOperations) {
				ops.copyCandidate = func(destination io.Writer, source io.Reader) (int64, error) {
					buffer := make([]byte, 2)
					count, _ := source.Read(buffer)
					written, _ := destination.Write(buffer[:count])
					return int64(written), errors.New("partial-copy")
				}
			},
		},
		{
			name: "sync",
			inject: func(ops *downloadOperations) {
				ops.syncCandidate = func(*os.File) error { return errors.New("candidate-sync") }
			},
		},
		{
			name: "hash",
			inject: func(ops *downloadOperations) {
				ops.digestCandidate = func(*os.File) (int64, [32]byte, error) {
					return 0, [32]byte{}, errors.New("candidate-hash")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			source, err := os.CreateTemp(directory, "source-")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err := source.WriteString("candidate-bytes"); err != nil {
				t.Fatal(err)
			}
			candidatePath := filepath.Join(directory, "candidate")
			candidate, err := os.OpenFile(candidatePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			ops := defaultDownloadOperations()
			test.inject(&ops)
			registered := false
			_, _, copyErr := copyHeldDownloadCandidate(source, candidate, func(info os.FileInfo) error {
				registered = true
				if !info.Mode().IsRegular() {
					t.Fatalf("registered non-regular candidate: %v", info.Mode())
				}
				return nil
			}, ops)
			if copyErr == nil || !registered {
				t.Fatalf("registered=%v err=%v", registered, copyErr)
			}
			if err := os.Remove(candidatePath); err != nil {
				t.Fatalf("registered candidate cannot be cleaned: %v", err)
			}
		})
	}
}
