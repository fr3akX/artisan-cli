package api

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadInstallResultRequiresExactVisibilityAndDurability(t *testing.T) {
	tests := []struct {
		name    string
		result  downloadInstallResult
		visible bool
		durable bool
	}{
		{"none", downloadInstallResult{Publication: publicationNone, Visibility: visibilityNotVisible, Durability: durabilityNotApplicable}, false, false},
		{"ambiguous publication", downloadInstallResult{Publication: publicationAmbiguous, Visibility: visibilityAmbiguous, Durability: durabilityUncertain}, false, false},
		{"exact but durability uncertain", downloadInstallResult{Publication: publicationExact, Visibility: visibilityExact, Durability: durabilityUncertain}, true, false},
		{"exact and durable", downloadInstallResult{Publication: publicationExact, Visibility: visibilityExact, Durability: durabilityExact}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.result.Visible() != test.visible || test.result.Durable() != test.durable {
				t.Fatalf("result %#v: visible=%v durable=%v", test.result, test.result.Visible(), test.result.Durable())
			}
		})
	}
}

func TestDownloadTargetObservationResetAndDescriptorDigestSeal(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	target, err := newDownloadTarget(destination, false, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Abort()
	_, _ = io.WriteString(target.Writer(), "discarded")
	if err := target.Reset(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "sealed")
	result, err := target.Install(false)
	if err != nil || !result.Visible() || !result.Durable() {
		t.Fatalf("install = %#v, %v", result, err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "sealed" {
		t.Fatalf("destination = %q", contents)
	}
}

func TestDownloadTargetRejectsInPlaceMutationAfterSeal(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	ops.afterSealedBeforeCandidate = func(target *downloadTarget) error {
		file := target.heldSourceFile()
		if file == nil {
			return errors.New("missing held source")
		}
		_, err := file.WriteAt([]byte("MUTATE"), 0)
		return err
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "sealed")
	result, installErr := target.Install(false)
	if installErr == nil || result.Publication != publicationNone || result.Visibility != visibilityNotVisible {
		t.Fatalf("install = %#v, %v", result, installErr)
	}
	target.Abort()
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination visible: %v", err)
	}
}

func TestObservedDownloadWriterHashesOnlySuccessfulBytes(t *testing.T) {
	hash := sha256.New()
	writer := &observedTargetWriter{destination: partialSuccessWriter{}, hash: hash}
	n, err := writer.Write([]byte("abcdef"))
	if n != 3 || err == nil || writer.count != 3 {
		t.Fatalf("write = %d, %v count=%d", n, err, writer.count)
	}
	want := sha256.Sum256([]byte("abc"))
	if string(hash.Sum(nil)) != string(want[:]) {
		t.Fatalf("hash does not describe successful bytes")
	}
}

type partialSuccessWriter struct{}

func (partialSuccessWriter) Write([]byte) (int, error) { return 3, io.ErrShortWrite }
