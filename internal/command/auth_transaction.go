package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/output"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const (
	loginTransactionFileName = ".login-transaction.json"
	maxLoginJournalBytes     = 1024 * 1024

	loginStageJournalWritten = "journal-written"
	loginStageTokenSaved     = "token-saved"
	loginStageServerSaved    = "server-saved"
	loginStageBeforeCommit   = "before-commit"
	loginStageCommitted      = "committed"
)

type loginTransactionState string

const (
	loginTransactionPending   loginTransactionState = "pending"
	loginTransactionCommitted loginTransactionState = "committed"
)

var errSimulatedLoginCrash = errors.New("simulated login crash")

type loginTransactionJournal struct {
	State                 loginTransactionState `json:"state"`
	Version               int                   `json:"version"`
	ServerPresent         bool                  `json:"server_present"`
	ServerURL             string                `json:"server_url"`
	TokenPresent          bool                  `json:"token_present"`
	Token                 string                `json:"token"`
	IntendedServerPresent bool                  `json:"intended_server_present"`
	IntendedServerURL     string                `json:"intended_server_url"`
	IntendedTokenPresent  bool                  `json:"intended_token_present"`
	IntendedToken         string                `json:"intended_token"`
}

type loginStageHook func(stage string) error

type loginTransactionOperations struct {
	writeJournal  func(string, loginTransactionJournal) error
	removeJournal func(string) error
	saveToken     func(string, string) error
	removeToken   func(string) error
	saveServer    func(string, string) error
	removeServer  func(string) error
}

func defaultLoginTransactionOperations() loginTransactionOperations {
	return loginTransactionOperations{
		writeJournal:  writeLoginJournal,
		removeJournal: removeLoginJournal,
		saveToken: func(configDir, token string) error {
			return auth.NewFileStore(configDir).Save(token)
		},
		removeToken: func(configDir string) error {
			return auth.NewFileStore(configDir).Remove()
		},
		saveServer:   config.SaveServer,
		removeServer: config.RemoveServer,
	}
}

func persistExplicitLogin(configDir, token, serverURL string, hook loginStageHook) *output.Error {
	return persistExplicitLoginWithOperations(configDir, token, serverURL, hook, defaultLoginTransactionOperations())
}

func persistExplicitLoginWithOperations(configDir, token, serverURL string, hook loginStageHook, operations loginTransactionOperations) *output.Error {
	journal, err := snapshotLoginState(configDir, token, serverURL)
	if err != nil {
		return transactionConfigurationFailure()
	}
	if err := operations.writeJournal(configDir, journal); err != nil {
		return transactionConfigurationFailure()
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageJournalWritten, operations); failure != nil {
		return failure
	}

	if err := operations.saveToken(configDir, token); err != nil {
		if securefile.ReplacementVisible(err) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal, operations)
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageTokenSaved, operations); failure != nil {
		return failure
	}

	if err := operations.saveServer(configDir, serverURL); err != nil {
		if securefile.ReplacementVisible(err) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal, operations)
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageServerSaved, operations); failure != nil {
		return failure
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageBeforeCommit, operations); failure != nil {
		return failure
	}

	journal.State = loginTransactionCommitted
	if err := operations.writeJournal(configDir, journal); err != nil {
		if securefile.ReplacementVisible(err) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal, operations)
	}
	if hook != nil {
		if err := hook(loginStageCommitted); err != nil {
			return transactionConfigurationFailure()
		}
	}
	// A durable committed marker makes deletion cleanup-only. Failure to remove
	// it is safe: the next auth command recognizes it and retries cleanup.
	_ = operations.removeJournal(configDir)
	return nil
}

func interruptPendingLoginTransaction(configDir string, journal loginTransactionJournal, hook loginStageHook, stage string, operations loginTransactionOperations) *output.Error {
	if hook == nil {
		return nil
	}
	if err := hook(stage); err != nil {
		if errors.Is(err, errSimulatedLoginCrash) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal, operations)
	}
	return nil
}

func snapshotLoginState(configDir, intendedToken, intendedServerURL string) (loginTransactionJournal, error) {
	journal := loginTransactionJournal{
		State:                 loginTransactionPending,
		Version:               1,
		IntendedServerPresent: true,
		IntendedServerURL:     intendedServerURL,
		IntendedTokenPresent:  true,
		IntendedToken:         intendedToken,
	}
	serverURL, err := config.LoadStoredServer(configDir)
	if err == nil {
		journal.ServerPresent = true
		journal.ServerURL = serverURL
	} else if !errors.Is(err, os.ErrNotExist) {
		return loginTransactionJournal{}, err
	}

	token, err := auth.NewFileStore(configDir).Load()
	if err == nil {
		journal.TokenPresent = true
		journal.Token = token
	} else if !errors.Is(err, os.ErrNotExist) {
		return loginTransactionJournal{}, err
	}
	return journal, nil
}

func writeLoginJournal(configDir string, journal loginTransactionJournal) error {
	if err := validateLoginJournal(journal); err != nil {
		return err
	}
	dir, err := config.ResolveDir(configDir)
	if err != nil {
		return err
	}
	contents, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return securefile.AtomicWrite(dir, loginTransactionFileName, contents)
}

func readLoginJournal(configDir string) (loginTransactionJournal, error) {
	dir, err := config.ResolveDir(configDir)
	if err != nil {
		return loginTransactionJournal{}, err
	}
	file, err := securefile.OpenPrivate(filepath.Join(dir, loginTransactionFileName))
	if err != nil {
		return loginTransactionJournal{}, err
	}
	defer file.Close()

	limited := io.LimitReader(file, maxLoginJournalBytes+1)
	contents, err := io.ReadAll(limited)
	if err != nil || len(contents) > maxLoginJournalBytes {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}
	journal, err := decodeLoginJournal(contents)
	if err != nil {
		return loginTransactionJournal{}, err
	}
	if err := validateLoginJournal(journal); err != nil {
		return loginTransactionJournal{}, err
	}
	return journal, nil
}

func decodeLoginJournal(contents []byte) (loginTransactionJournal, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}

	required := map[string]bool{
		"state": false, "version": false, "server_present": false,
		"server_url": false, "token_present": false, "token": false,
		"intended_server_present": false, "intended_server_url": false,
		"intended_token_present": false, "intended_token": false,
	}
	values := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		fieldToken, err := decoder.Token()
		field, ok := fieldToken.(string)
		if err != nil || !ok {
			return loginTransactionJournal{}, errors.New("invalid login transaction journal")
		}
		seen, known := required[field]
		if !known || seen {
			return loginTransactionJournal{}, errors.New("invalid login transaction journal")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return loginTransactionJournal{}, errors.New("invalid login transaction journal")
		}
		required[field] = true
		values[field] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}
	for _, present := range required {
		if !present {
			return loginTransactionJournal{}, errors.New("invalid login transaction journal")
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}

	var journal loginTransactionJournal
	if !decodeJournalString(values["state"], (*string)(&journal.State)) ||
		!decodeJournalInteger(values["version"], &journal.Version) ||
		!decodeJournalBoolean(values["server_present"], &journal.ServerPresent) ||
		!decodeJournalString(values["server_url"], &journal.ServerURL) ||
		!decodeJournalBoolean(values["token_present"], &journal.TokenPresent) ||
		!decodeJournalString(values["token"], &journal.Token) ||
		!decodeJournalBoolean(values["intended_server_present"], &journal.IntendedServerPresent) ||
		!decodeJournalString(values["intended_server_url"], &journal.IntendedServerURL) ||
		!decodeJournalBoolean(values["intended_token_present"], &journal.IntendedTokenPresent) ||
		!decodeJournalString(values["intended_token"], &journal.IntendedToken) {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}
	return journal, nil
}

func decodeJournalString(raw json.RawMessage, destination *string) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '"' && json.Unmarshal(trimmed, destination) == nil
}

func decodeJournalInteger(raw json.RawMessage, destination *int) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] < '0' || trimmed[0] > '9' {
		return false
	}
	return json.Unmarshal(trimmed, destination) == nil
}

func decodeJournalBoolean(raw json.RawMessage, destination *bool) bool {
	trimmed := bytes.TrimSpace(raw)
	if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
		return false
	}
	return json.Unmarshal(trimmed, destination) == nil
}

func validateLoginJournal(journal loginTransactionJournal) error {
	if journal.State != loginTransactionPending && journal.State != loginTransactionCommitted {
		return errors.New("invalid login transaction journal")
	}
	if journal.Version != 1 {
		return errors.New("invalid login transaction journal")
	}
	if err := validateJournalPair(journal.ServerPresent, journal.ServerURL, journal.TokenPresent, journal.Token); err != nil {
		return err
	}
	if err := validateJournalPair(journal.IntendedServerPresent, journal.IntendedServerURL, journal.IntendedTokenPresent, journal.IntendedToken); err != nil {
		return err
	}
	return nil
}

func validateJournalPair(serverPresent bool, serverURL string, tokenPresent bool, token string) error {
	if serverPresent {
		if len(serverURL) > 4096 {
			return errors.New("invalid login transaction journal")
		}
		normalized, err := config.NormalizeServerURL(serverURL)
		if err != nil || normalized != serverURL {
			return errors.New("invalid login transaction journal")
		}
	} else if serverURL != "" {
		return errors.New("invalid login transaction journal")
	}
	if tokenPresent {
		if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || len(token) > maxTokenInputBytes {
			return errors.New("invalid login transaction journal")
		}
	} else if token != "" {
		return errors.New("invalid login transaction journal")
	}
	return nil
}

type storedLoginPair struct {
	ServerPresent bool
	ServerURL     string
	TokenPresent  bool
	Token         string
}

func recoverLoginTransaction(configDir string) error {
	return recoverLoginTransactionWithOperations(configDir, defaultLoginTransactionOperations())
}

func recoverLoginTransactionWithOperations(configDir string, operations loginTransactionOperations) error {
	journal, err := readLoginJournal(configDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	current, currentErr := readStoredLoginPair(configDir)
	intended := storedLoginPair{
		ServerPresent: journal.IntendedServerPresent,
		ServerURL:     journal.IntendedServerURL,
		TokenPresent:  journal.IntendedTokenPresent,
		Token:         journal.IntendedToken,
	}
	if currentErr == nil && current == intended {
		// Both intended values are visible. Rewrite them durably before marking
		// committed so any earlier post-rename error is resolved by roll-forward.
		if err := applyStoredLoginPairWithOperations(configDir, intended, operations); err != nil {
			return fmt.Errorf("roll forward login transaction: %w", err)
		}
		journal.State = loginTransactionCommitted
		if err := operations.writeJournal(configDir, journal); err != nil {
			return fmt.Errorf("commit recovered login transaction: %w", err)
		}
		// A failure can only resurrect this committed marker after the exact
		// intended pair and marker have both been made durable. Retrying it is
		// cleanup-only and cannot roll back a mixed pair.
		_ = operations.removeJournal(configDir)
		return nil
	}

	// Prior, mixed, and unreadable states all resolve to the exact prior pair.
	// A committed marker cannot bypass this actual-state validation.
	prior := storedLoginPair{
		ServerPresent: journal.ServerPresent,
		ServerURL:     journal.ServerURL,
		TokenPresent:  journal.TokenPresent,
		Token:         journal.Token,
	}
	if err := applyStoredLoginPairWithOperations(configDir, prior, operations); err != nil {
		return fmt.Errorf("restore login transaction: %w", err)
	}
	if err := operations.removeJournal(configDir); err != nil {
		return fmt.Errorf("complete login transaction recovery: %w", err)
	}
	return nil
}

func readStoredLoginPair(configDir string) (storedLoginPair, error) {
	var pair storedLoginPair
	serverURL, err := config.LoadStoredServer(configDir)
	if err == nil {
		pair.ServerPresent = true
		pair.ServerURL = serverURL
	} else if !errors.Is(err, os.ErrNotExist) {
		return storedLoginPair{}, err
	}
	token, err := auth.NewFileStore(configDir).Load()
	if err == nil {
		pair.TokenPresent = true
		pair.Token = token
	} else if !errors.Is(err, os.ErrNotExist) {
		return storedLoginPair{}, err
	}
	return pair, nil
}

func applyStoredLoginPairWithOperations(configDir string, pair storedLoginPair, operations loginTransactionOperations) error {
	journal := loginTransactionJournal{
		ServerPresent: pair.ServerPresent,
		ServerURL:     pair.ServerURL,
		TokenPresent:  pair.TokenPresent,
		Token:         pair.Token,
	}
	return restoreLoginStateWithOperations(configDir, journal, operations)
}

func failAndRestoreLoginTransaction(configDir string, journal loginTransactionJournal, operations loginTransactionOperations) *output.Error {
	if err := restoreLoginStateWithOperations(configDir, journal, operations); err != nil {
		return transactionConfigurationFailure()
	}
	if err := operations.removeJournal(configDir); err != nil {
		return transactionConfigurationFailure()
	}
	return transactionConfigurationFailure()
}

func restoreLoginStateWithOperations(configDir string, journal loginTransactionJournal, operations loginTransactionOperations) error {
	var restoreErrors []error
	if journal.ServerPresent {
		if err := operations.saveServer(configDir, journal.ServerURL); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	} else if err := operations.removeServer(configDir); err != nil {
		restoreErrors = append(restoreErrors, err)
	}

	if journal.TokenPresent {
		if err := operations.saveToken(configDir, journal.Token); err != nil {
			restoreErrors = append(restoreErrors, err)
			// If the prior token cannot be restored, durably remove the current
			// file so the submitted token does not remain usable after failure.
			if removeErr := operations.removeToken(configDir); removeErr != nil {
				restoreErrors = append(restoreErrors, removeErr)
			}
		}
	} else if err := operations.removeToken(configDir); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	return errors.Join(restoreErrors...)
}

func removeLoginJournal(configDir string) error {
	dir, err := config.ResolveDir(configDir)
	if err != nil {
		return err
	}
	return securefile.DurableRemove(dir, loginTransactionFileName)
}

func transactionConfigurationFailure() *output.Error {
	return &output.Error{
		ExitCode: 3,
		Code:     "configuration_error",
		Message:  "Unable to update stored configuration",
	}
}
