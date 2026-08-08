package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func assertCommittedCheckpointMatchesCurrent(t *testing.T, dir string) loginTransactionJournal {
	t.Helper()
	journal, err := readLoginJournal(dir)
	if err != nil {
		t.Fatalf("readLoginJournal() error = %v", err)
	}
	if journal.State != loginTransactionCommitted || journal.Version != authCheckpointVersion {
		t.Fatalf("journal state/version = %q/%d, want committed/%d", journal.State, journal.Version, authCheckpointVersion)
	}
	pair, err := readStoredLoginPair(dir)
	if err != nil {
		t.Fatalf("readStoredLoginPair() error = %v", err)
	}
	if !checkpointMatchesPair(journal, pair) {
		t.Fatal("committed checkpoint does not fingerprint the current stored pair")
	}
	return journal
}

func assertStoredLoginPairEquals(t *testing.T, dir string, want storedLoginPair) {
	t.Helper()
	got, err := readStoredLoginPair(dir)
	if err != nil || got != want {
		t.Fatalf("stored pair = %#v, %v, want %#v", got, err, want)
	}
}

func TestCommittedAuthCheckpointContainsOnlyFingerprints(t *testing.T) {
	dir := t.TempDir()
	const oldServer = "http://127.0.0.1:46001"
	const oldToken = "old-credential-secret"
	const newServer = "http://127.0.0.1:46002"
	if err := config.SaveServer(dir, oldServer); err != nil {
		t.Fatalf("SaveServer() error = %v", err)
	}
	if err := auth.NewFileStore(dir).Save(oldToken); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if failure := persistExplicitLogin(dir, commandTestToken, newServer, nil); failure != nil {
		t.Fatalf("persistExplicitLogin() failure = %#v", failure)
	}

	checkpoint := assertCommittedCheckpointMatchesCurrent(t, dir)
	if checkpoint.ServerFingerprint == "" || checkpoint.TokenFingerprint == "" {
		t.Fatal("committed checkpoint omitted a state fingerprint")
	}
	contents, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, raw := range []string{oldServer, oldToken, newServer, commandTestToken, `"server_url"`, `"token"`, `"intended_token"`} {
		if bytes.Contains(contents, []byte(raw)) {
			t.Fatalf("committed checkpoint contains protected raw value or field %q", raw)
		}
	}
}

func TestCommittedCheckpointSupportsLoginStatusReloginAndLogout(t *testing.T) {
	dir := t.TempDir()
	server := identityServer(t, nil)
	defer server.Close()

	login := runAuthCommand(t, Runtime{In: strings.NewReader(commandTestToken + "\n"), ConfigDir: dir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if login.code != 0 {
		t.Fatalf("initial login = %#v", login)
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)

	status := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "status")
	if status.code != 0 {
		t.Fatalf("status = %#v", status)
	}
	assertCommittedCheckpointMatchesCurrent(t, dir)

	const replacementToken = "replacement-credential"
	relogin := runAuthCommand(t, Runtime{In: strings.NewReader(replacementToken + "\n"), ConfigDir: dir},
		"--server", server.URL, "auth", "login", "--token-stdin")
	if relogin.code != 0 {
		t.Fatalf("re-login = %#v", relogin)
	}
	assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: server.URL, TokenPresent: true, Token: replacementToken})
	assertCommittedCheckpointMatchesCurrent(t, dir)

	logout := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "logout")
	if logout.code != 0 {
		t.Fatalf("logout = %#v", logout)
	}
	assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: server.URL})
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestLogoutTransactionRecoversCrashesAtPendingDataAndCommitTransitions(t *testing.T) {
	const serverURL = "http://127.0.0.1:46051"
	const token = "logout-crash-credential"
	for _, tc := range []struct {
		stage        string
		wantLoggedIn bool
	}{
		{stage: loginStageJournalWritten, wantLoggedIn: true},
		{stage: loginStageTokenSaved},
		{stage: loginStageServerSaved},
		{stage: loginStageBeforeCommit},
		{stage: loginStageCommitted},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			dir := t.TempDir()
			if failure := persistExplicitLogin(dir, token, serverURL, nil); failure != nil {
				t.Fatalf("persistExplicitLogin() failure = %#v", failure)
			}
			failure := persistLogoutWithOperations(dir, func(stage string) error {
				if stage == tc.stage {
					return errSimulatedLoginCrash
				}
				return nil
			}, defaultLoginTransactionOperations())
			if failure == nil {
				t.Fatal("persistLogoutWithOperations() succeeded, want simulated crash")
			}
			if err := recoverLoginTransaction(dir); err != nil {
				t.Fatalf("recoverLoginTransaction() error = %v", err)
			}
			want := storedLoginPair{ServerPresent: true, ServerURL: serverURL}
			if tc.wantLoggedIn {
				want.TokenPresent = true
				want.Token = token
			}
			assertStoredLoginPairEquals(t, dir, want)
			assertCommittedCheckpointMatchesCurrent(t, dir)
		})
	}
}

func TestStaleCommittedCheckpointCannotAuthorizeLaterLogoutOrResurrectCredential(t *testing.T) {
	dir := t.TempDir()
	const firstServer = "http://127.0.0.1:46101"
	const secondServer = "http://127.0.0.1:46102"
	const secondToken = "second-credential"
	if failure := persistExplicitLogin(dir, commandTestToken, firstServer, nil); failure != nil {
		t.Fatalf("first login failure = %#v", failure)
	}
	staleCheckpoint, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
	if err != nil {
		t.Fatalf("ReadFile stale checkpoint: %v", err)
	}
	if failure := persistLogout(dir); failure != nil {
		t.Fatalf("logout failure = %#v", failure)
	}
	if failure := persistExplicitLogin(dir, secondToken, secondServer, nil); failure != nil {
		t.Fatalf("second login failure = %#v", failure)
	}
	want := storedLoginPair{ServerPresent: true, ServerURL: secondServer, TokenPresent: true, Token: secondToken}
	assertStoredLoginPairEquals(t, dir, want)

	// Model a crash restoring a checkpoint from before the intervening logout
	// and re-login. It contains no raw credential with which recovery could
	// restore the first state.
	if err := securefile.AtomicWrite(dir, loginTransactionFileName, staleCheckpoint); err != nil {
		t.Fatalf("restore stale checkpoint: %v", err)
	}
	result := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "logout")
	if result.code != 3 {
		t.Fatalf("logout with stale checkpoint = %#v, want configuration failure", result)
	}
	assertStoredLoginPairEquals(t, dir, want)
	contents, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
	if err != nil || !bytes.Equal(contents, staleCheckpoint) {
		t.Fatal("mismatched stale checkpoint was changed instead of blocking")
	}
}

func TestMalformedOrDuplicateCommittedCheckpointBlocksWithoutMutation(t *testing.T) {
	const serverURL = "http://127.0.0.1:46201"
	const token = "current-credential"
	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "missing fingerprint", mutate: func(raw string) string {
			start := strings.Index(raw, `,"token_fingerprint":`)
			if start < 0 {
				return raw
			}
			return raw[:start] + "}\n"
		}},
		{name: "duplicate presence", mutate: func(raw string) string {
			return strings.Replace(raw, `"server_present":true`, `"server_present":true,"server_present":true`, 1)
		}},
		{name: "raw credential field", mutate: func(raw string) string {
			return strings.Replace(raw, "}\n", `,"token":"current-credential"}`+"\n", 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if failure := persistExplicitLogin(dir, token, serverURL, nil); failure != nil {
				t.Fatalf("persistExplicitLogin() failure = %#v", failure)
			}
			valid, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			malformed := []byte(tc.mutate(string(valid)))
			if bytes.Equal(malformed, valid) {
				t.Fatal("test did not mutate checkpoint")
			}
			if err := securefile.AtomicWrite(dir, loginTransactionFileName, malformed); err != nil {
				t.Fatalf("AtomicWrite malformed checkpoint: %v", err)
			}
			want := storedLoginPair{ServerPresent: true, ServerURL: serverURL, TokenPresent: true, Token: token}
			result := runAuthCommand(t, Runtime{ConfigDir: dir}, "auth", "logout")
			if result.code != 3 {
				t.Fatalf("logout result = %#v, want configuration failure", result)
			}
			assertStoredLoginPairEquals(t, dir, want)
			remaining, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
			if err != nil || !bytes.Equal(remaining, malformed) {
				t.Fatal("malformed checkpoint was changed during blocked recovery")
			}
		})
	}
}

func TestLogoutRemovalAmbiguityRetainsAuthoritativeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	const serverURL = "http://127.0.0.1:46301"
	const token = "prior-credential"
	if failure := persistExplicitLogin(dir, token, serverURL, nil); failure != nil {
		t.Fatalf("persistExplicitLogin() failure = %#v", failure)
	}

	ops := defaultLoginTransactionOperations()
	injected := errors.New("injected post-remove sync failure")
	ops.removeToken = func(configDir string) error {
		resolved, err := config.ResolveDir(configDir)
		if err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(resolved, "credentials.json")); err != nil {
			return err
		}
		return injected
	}
	if failure := persistLogoutWithOperations(dir, nil, ops); failure == nil {
		t.Fatal("persistLogoutWithOperations() succeeded despite ambiguous removal")
	}
	// The rollback either durably restores the prior pair and its fingerprinted
	// checkpoint, or leaves the pending journal. It never deletes the marker.
	if _, err := os.Stat(filepath.Join(dir, loginTransactionFileName)); err != nil {
		t.Fatal("ambiguous removal lost the authoritative marker")
	}
	assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: serverURL, TokenPresent: true, Token: token})
	assertCommittedCheckpointMatchesCurrent(t, dir)

	if failure := persistLogout(dir); failure != nil {
		t.Fatalf("retry logout failure = %#v", failure)
	}
	assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: serverURL})
	assertCommittedCheckpointMatchesCurrent(t, dir)
}

func TestLegacyJournalMigrationIsNonDestructiveOrRejectsAmbiguity(t *testing.T) {
	const priorServer = "http://127.0.0.1:46401"
	const priorToken = "prior-credential"
	const intendedServer = "http://127.0.0.1:46402"
	const intendedToken = "intended-credential"
	legacy := loginTransactionJournal{
		State:                 loginTransactionPending,
		Version:               legacyLoginJournalVersion,
		ServerPresent:         true,
		ServerURL:             priorServer,
		TokenPresent:          true,
		Token:                 priorToken,
		IntendedServerPresent: true,
		IntendedServerURL:     intendedServer,
		IntendedTokenPresent:  true,
		IntendedToken:         intendedToken,
	}

	t.Run("matching pending prior migrates", func(t *testing.T) {
		dir := t.TempDir()
		if err := config.SaveServer(dir, priorServer); err != nil {
			t.Fatal(err)
		}
		if err := auth.NewFileStore(dir).Save(priorToken); err != nil {
			t.Fatal(err)
		}
		if err := writeLoginJournal(dir, legacy); err != nil {
			t.Fatal(err)
		}
		if err := recoverLoginTransaction(dir); err != nil {
			t.Fatalf("recoverLoginTransaction() error = %v", err)
		}
		assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: priorServer, TokenPresent: true, Token: priorToken})
		assertCommittedCheckpointMatchesCurrent(t, dir)
	})

	t.Run("matching committed intended migrates", func(t *testing.T) {
		dir := t.TempDir()
		if err := config.SaveServer(dir, intendedServer); err != nil {
			t.Fatal(err)
		}
		if err := auth.NewFileStore(dir).Save(intendedToken); err != nil {
			t.Fatal(err)
		}
		legacy.State = loginTransactionCommitted
		if err := writeLoginJournal(dir, legacy); err != nil {
			t.Fatal(err)
		}
		if err := recoverLoginTransaction(dir); err != nil {
			t.Fatalf("recoverLoginTransaction() error = %v", err)
		}
		assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: intendedServer, TokenPresent: true, Token: intendedToken})
		assertCommittedCheckpointMatchesCurrent(t, dir)
	})

	t.Run("mismatched committed legacy is rejected", func(t *testing.T) {
		dir := t.TempDir()
		const currentServer = "http://127.0.0.1:46403"
		const currentToken = "later-credential"
		if err := config.SaveServer(dir, currentServer); err != nil {
			t.Fatal(err)
		}
		if err := auth.NewFileStore(dir).Save(currentToken); err != nil {
			t.Fatal(err)
		}
		legacy.State = loginTransactionCommitted
		if err := writeLoginJournal(dir, legacy); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
		if err != nil {
			t.Fatal(err)
		}
		if err := recoverLoginTransaction(dir); err == nil {
			t.Fatal("recoverLoginTransaction() guessed how to recover mismatched legacy committed journal")
		}
		assertStoredLoginPairEquals(t, dir, storedLoginPair{ServerPresent: true, ServerURL: currentServer, TokenPresent: true, Token: currentToken})
		after, err := os.ReadFile(filepath.Join(dir, loginTransactionFileName))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("rejected legacy journal or current state was mutated")
		}
	})
}
