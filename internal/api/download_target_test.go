package api

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestDownloadTargetCreatesPrivateSameDirectoryTemporaryAndResets(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	target, err := newDownloadTarget(destination, false, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Abort()

	if filepath.Dir(target.temporaryPath) != filepath.Dir(destination) {
		t.Fatalf("temporary path = %q", target.temporaryPath)
	}
	if runtime.GOOS == "windows" {
		file, openErr := securefile.OpenPrivate(target.temporaryPath)
		if openErr != nil {
			t.Fatalf("temporary is not private: %v", openErr)
		}
		_ = file.Close()
	} else if info, statErr := os.Stat(target.temporaryPath); statErr != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("temporary mode = %v, %v", info, statErr)
	}
	if _, err := io.WriteString(target.Writer(), "stale-attempt"); err != nil {
		t.Fatal(err)
	}
	if err := target.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target.Writer(), "final"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(target.file)
	if err != nil || string(contents) != "final" {
		t.Fatalf("contents = %q, %v", contents, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination became visible before install: %v", err)
	}
}

func TestDownloadTargetRejectsInvalidDestinationMissingParentAndExistingNoForce(t *testing.T) {
	for _, destination := range []string{"", ".", string(filepath.Separator)} {
		if target, err := newDownloadTarget(destination, false, defaultDownloadOperations()); err == nil || target != nil || !errors.Is(err, errInvalidDownloadDestination) {
			t.Fatalf("destination %q: target=%#v err=%v", destination, target, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing", "profile.alog")
	if target, err := newDownloadTarget(missing, false, defaultDownloadOperations()); err == nil || target != nil {
		t.Fatalf("missing parent: target=%#v err=%v", target, err)
	}
	existing := filepath.Join(t.TempDir(), "existing.alog")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if target, err := newDownloadTarget(existing, false, defaultDownloadOperations()); err == nil || target != nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing: target=%#v err=%v", target, err)
	}
	contents, _ := os.ReadFile(existing)
	if string(contents) != "keep" {
		t.Fatalf("existing changed to %q", contents)
	}
}

func TestDownloadTargetAbortRemovesOnlyOwnedTemporary(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	target, err := newDownloadTarget(destination, false, defaultDownloadOperations())
	if err != nil {
		t.Fatal(err)
	}
	temporary := target.temporaryPath
	if err := os.WriteFile(destination, []byte("racer"), 0o600); err != nil {
		t.Fatal(err)
	}
	target.Abort()
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary remains: %v", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "racer" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	// Abort is terminal and idempotent.
	target.Abort()
}

func TestDownloadTargetAbortRetainsReplacementWhenTemporaryNameIdentityChanges(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	var heldPath string
	ops.beforeAbort = func(source string) error {
		heldPath = source + ".held"
		if err := os.Rename(source, heldPath); err != nil {
			return err
		}
		return os.WriteFile(source, []byte("racer-replacement"), 0o600)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target.Writer(), "verified-owned"); err != nil {
		t.Fatal(err)
	}
	target.Abort()

	if contents, err := os.ReadFile(target.temporaryPath); err != nil || string(contents) != "racer-replacement" {
		t.Fatalf("replacement = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(heldPath); err != nil || string(contents) != "verified-owned" {
		t.Fatalf("held verified residue = %q, %v", contents, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination visible: %v", err)
	}
}

func TestDownloadTargetRejectsTemporaryIdentitySwapImmediatelyBeforeNativeInstall(t *testing.T) {
	for _, test := range []struct {
		name  string
		force bool
	}{
		{name: "no force"},
		{name: "force with existing destination", force: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			if test.force {
				if err := os.WriteFile(destination, []byte("existing-destination"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultDownloadOperations()
			var heldPath string
			var nativeCalls int
			defaults := ops
			ops.beforeInstall = func(source, _ string) error {
				heldPath = source + ".held"
				if err := os.Rename(source, heldPath); err != nil {
					return err
				}
				return os.WriteFile(source, []byte("unverified-racer"), 0o600)
			}
			ops.installNoReplace = func(identity *downloadFileIdentity, from, to string) (bool, error) {
				nativeCalls++
				return defaults.installNoReplace(identity, from, to)
			}
			ops.replace = func(identity *downloadFileIdentity, from, to string) (bool, error) {
				nativeCalls++
				return defaults.replace(identity, from, to)
			}
			target, err := newDownloadTarget(destination, test.force, ops)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(target.Writer(), "verified-owned"); err != nil {
				t.Fatal(err)
			}
			installed, installErr := target.Install(test.force)
			if installErr == nil || installed.Visible || nativeCalls != 0 {
				t.Fatalf("install = %#v, %v; native calls = %d", installed, installErr, nativeCalls)
			}
			target.Abort()
			if contents, err := os.ReadFile(target.temporaryPath); err != nil || string(contents) != "unverified-racer" {
				t.Fatalf("replacement = %q, %v", contents, err)
			}
			if contents, err := os.ReadFile(heldPath); err != nil || string(contents) != "verified-owned" {
				t.Fatalf("verified residue = %q, %v", contents, err)
			}
			if test.force {
				if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing-destination" {
					t.Fatalf("existing destination = %q, %v", contents, err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
		})
	}
}

func TestDownloadTargetRejectsFinalNameThatDoesNotMatchHeldIdentity(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	var verifiedPublished string
	ops.afterInstall = func(_, destination string) error {
		verifiedPublished = destination + ".verified"
		if err := os.Rename(destination, verifiedPublished); err != nil {
			return err
		}
		return os.WriteFile(destination, []byte("racer-final"), 0o600)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(target.Writer(), "verified-owned"); err != nil {
		t.Fatal(err)
	}
	installed, installErr := target.Install(false)
	if installErr == nil || installed.Visible {
		t.Fatalf("install = %#v, %v", installed, installErr)
	}
	target.Abort()
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "racer-final" {
		t.Fatalf("racer final = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(verifiedPublished); err != nil || string(contents) != "verified-owned" {
		t.Fatalf("verified publication residue = %q, %v", contents, err)
	}
}

func TestDownloadTargetInstallNoReplaceAndForceAreFinalAtomicStep(t *testing.T) {
	for _, test := range []struct {
		name  string
		force bool
	}{
		{name: "no replace"},
		{name: "force replace", force: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			if test.force {
				if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultDownloadOperations()
			defaults := ops
			var events []string
			ops.syncFile = func(file *os.File) error { events = append(events, "sync"); return defaults.syncFile(file) }
			ops.closeFile = func(file *os.File) error { events = append(events, "close"); return defaults.closeFile(file) }
			ops.installNoReplace = func(identity *downloadFileIdentity, from, to string) (bool, error) {
				events = append(events, "install-no-replace")
				return defaults.installNoReplace(identity, from, to)
			}
			ops.replace = func(identity *downloadFileIdentity, from, to string) (bool, error) {
				events = append(events, "replace")
				return defaults.replace(identity, from, to)
			}
			ops.syncParent = func(path string) error { events = append(events, "sync-parent"); return defaults.syncParent(path) }
			target, err := newDownloadTarget(destination, test.force, ops)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(target.Writer(), "new"); err != nil {
				t.Fatal(err)
			}
			if test.force {
				contents, _ := os.ReadFile(destination)
				if string(contents) != "old" {
					t.Fatalf("force changed destination early: %q", contents)
				}
			}
			installed, err := target.Install(test.force)
			if err != nil || !installed.Visible || !installed.Durable {
				t.Fatalf("install = %#v, %v", installed, err)
			}
			wantEvents := []string{"sync", "close", "install-no-replace", "sync-parent"}
			if test.force {
				wantEvents = []string{"sync", "close", "replace", "sync-parent"}
			}
			if !reflect.DeepEqual(events, wantEvents) {
				t.Fatalf("events = %v, want %v", events, wantEvents)
			}
			contents, _ := os.ReadFile(destination)
			if string(contents) != "new" {
				t.Fatalf("destination = %q", contents)
			}
			target.Abort()
			contents, _ = os.ReadFile(destination)
			if string(contents) != "new" {
				t.Fatalf("Abort removed installed destination: %q", contents)
			}
		})
	}
}

func TestDownloadTargetResetSyncAndCloseFailuresNeverExposeDestination(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*downloadOperations)
		reset  bool
	}{
		{name: "reset", reset: true, inject: func(ops *downloadOperations) {
			ops.resetFile = func(*os.File) error { return errors.New("reset failure") }
		}},
		{name: "sync", inject: func(ops *downloadOperations) {
			ops.syncFile = func(*os.File) error { return errors.New("sync failure") }
		}},
		{name: "close", inject: func(ops *downloadOperations) {
			ops.closeFile = func(*os.File) error { return errors.New("close failure") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			ops := defaultDownloadOperations()
			test.inject(&ops)
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "partial")
			if test.reset {
				err = target.Reset()
			} else {
				_, err = target.Install(false)
			}
			if err == nil {
				t.Fatal("injected failure succeeded")
			}
			target.Abort()
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
			if _, err := os.Lstat(target.temporaryPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary remains: %v", err)
			}
		})
	}
}

func TestDownloadTargetInstallReportsVisibilityAndDurabilityPrecisely(t *testing.T) {
	for _, test := range []struct {
		name        string
		install     func(*downloadFileIdentity, string, string) (bool, error)
		syncParent  func(string) error
		wantVisible bool
		wantDurable bool
	}{
		{name: "install failure", install: func(*downloadFileIdentity, string, string) (bool, error) { return false, errors.New("install") }},
		{name: "visible cleanup failure", install: func(_ *downloadFileIdentity, from, to string) (bool, error) {
			if err := os.Link(from, to); err != nil {
				return false, err
			}
			return true, errors.New("cleanup")
		}, wantVisible: true, wantDurable: true},
		{name: "parent sync failure", install: func(_ *downloadFileIdentity, from, to string) (bool, error) {
			if err := os.Rename(from, to); err != nil {
				return false, err
			}
			return true, nil
		}, syncParent: func(string) error { return errors.New("sync parent") }, wantVisible: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile.alog")
			ops := defaultDownloadOperations()
			ops.installNoReplace = test.install
			if test.syncParent != nil {
				ops.syncParent = test.syncParent
			}
			target, err := newDownloadTarget(destination, false, ops)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(target.Writer(), "complete")
			installed, installErr := target.Install(false)
			if installErr == nil || installed.Visible != test.wantVisible || installed.Durable != test.wantDurable {
				t.Fatalf("install = %#v, %v", installed, installErr)
			}
			target.Abort()
			if test.wantVisible {
				contents, err := os.ReadFile(destination)
				if err != nil || string(contents) != "complete" {
					t.Fatalf("destination = %q, %v", contents, err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination visible: %v", err)
			}
		})
	}
}

func TestDownloadTargetInstallRaceNeverClobbersAndFailuresNeverExposePartial(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile.alog")
	ops := defaultDownloadOperations()
	defaults := ops
	ops.installNoReplace = func(identity *downloadFileIdentity, from, to string) (bool, error) {
		if err := os.WriteFile(to, []byte("racer"), 0o600); err != nil {
			return false, err
		}
		return defaults.installNoReplace(identity, from, to)
	}
	target, err := newDownloadTarget(destination, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(target.Writer(), "partial")
	installed, err := target.Install(false)
	if err == nil || installed.Visible || !errors.Is(err, os.ErrExist) {
		t.Fatalf("install = %#v, %v", installed, err)
	}
	target.Abort()
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != "racer" {
		t.Fatalf("destination = %q, %v", contents, readErr)
	}
}
