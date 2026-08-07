package command

import (
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
	maxLoginJournalBytes     = 512 * 1024

	loginStageJournalWritten = "journal-written"
	loginStageTokenSaved     = "token-saved"
	loginStageServerSaved    = "server-saved"
	loginStageBeforeCommit   = "before-commit"
)

var errSimulatedLoginCrash = errors.New("simulated login crash")

type loginTransactionJournal struct {
	Version       int    `json:"version"`
	ServerPresent bool   `json:"server_present"`
	ServerURL     string `json:"server_url"`
	TokenPresent  bool   `json:"token_present"`
	Token         string `json:"token"`
}

type loginStageHook func(stage string) error

func persistExplicitLogin(configDir, token, serverURL string, hook loginStageHook) *output.Error {
	journal, err := snapshotLoginState(configDir)
	if err != nil {
		return transactionConfigurationFailure()
	}
	if err := writeLoginJournal(configDir, journal); err != nil {
		return transactionConfigurationFailure()
	}
	if failure := interruptLoginTransaction(configDir, journal, hook, loginStageJournalWritten); failure != nil {
		return failure
	}

	store := auth.NewFileStore(configDir)
	if err := store.Save(token); err != nil {
		return failAndRestoreLoginTransaction(configDir, journal)
	}
	if failure := interruptLoginTransaction(configDir, journal, hook, loginStageTokenSaved); failure != nil {
		return failure
	}

	if err := config.SaveServer(configDir, serverURL); err != nil {
		return failAndRestoreLoginTransaction(configDir, journal)
	}
	if failure := interruptLoginTransaction(configDir, journal, hook, loginStageServerSaved); failure != nil {
		return failure
	}
	if failure := interruptLoginTransaction(configDir, journal, hook, loginStageBeforeCommit); failure != nil {
		return failure
	}

	if err := removeLoginJournal(configDir); err != nil {
		return failAndRestoreLoginTransaction(configDir, journal)
	}
	return nil
}

func interruptLoginTransaction(configDir string, journal loginTransactionJournal, hook loginStageHook, stage string) *output.Error {
	if hook == nil {
		return nil
	}
	if err := hook(stage); err != nil {
		if errors.Is(err, errSimulatedLoginCrash) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal)
	}
	return nil
}

func snapshotLoginState(configDir string) (loginTransactionJournal, error) {
	journal := loginTransactionJournal{Version: 1}
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
	var journal loginTransactionJournal
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return loginTransactionJournal{}, errors.New("invalid login transaction journal")
	}
	if err := validateLoginJournal(journal); err != nil {
		return loginTransactionJournal{}, err
	}
	return journal, nil
}

func validateLoginJournal(journal loginTransactionJournal) error {
	if journal.Version != 1 {
		return errors.New("invalid login transaction journal")
	}
	if journal.ServerPresent {
		if len(journal.ServerURL) > 4096 {
			return errors.New("invalid login transaction journal")
		}
		normalized, err := config.NormalizeServerURL(journal.ServerURL)
		if err != nil || normalized != journal.ServerURL {
			return errors.New("invalid login transaction journal")
		}
	} else if journal.ServerURL != "" {
		return errors.New("invalid login transaction journal")
	}
	if journal.TokenPresent {
		if strings.TrimSpace(journal.Token) == "" || strings.ContainsAny(journal.Token, "\r\n") || len(journal.Token) > maxTokenInputBytes {
			return errors.New("invalid login transaction journal")
		}
	} else if journal.Token != "" {
		return errors.New("invalid login transaction journal")
	}
	return nil
}

func recoverLoginTransaction(configDir string) error {
	journal, err := readLoginJournal(configDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := restoreLoginState(configDir, journal); err != nil {
		return fmt.Errorf("restore login transaction: %w", err)
	}
	if err := removeLoginJournal(configDir); err != nil {
		return fmt.Errorf("complete login transaction recovery: %w", err)
	}
	return nil
}

func failAndRestoreLoginTransaction(configDir string, journal loginTransactionJournal) *output.Error {
	if err := restoreLoginState(configDir, journal); err != nil {
		return transactionConfigurationFailure()
	}
	if err := removeLoginJournal(configDir); err != nil {
		return transactionConfigurationFailure()
	}
	return transactionConfigurationFailure()
}

func restoreLoginState(configDir string, journal loginTransactionJournal) error {
	var restoreErrors []error
	if journal.ServerPresent {
		if err := config.SaveServer(configDir, journal.ServerURL); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	} else if err := config.RemoveServer(configDir); err != nil {
		restoreErrors = append(restoreErrors, err)
	}

	store := auth.NewFileStore(configDir)
	if journal.TokenPresent {
		if err := store.Save(journal.Token); err != nil {
			restoreErrors = append(restoreErrors, err)
			// If the prior token cannot be restored, remove the current file so
			// the newly submitted token does not remain usable after failure.
			if removeErr := store.Remove(); removeErr != nil {
				restoreErrors = append(restoreErrors, removeErr)
			}
		}
	} else if err := store.Remove(); err != nil {
		restoreErrors = append(restoreErrors, err)
	}
	return errors.Join(restoreErrors...)
}

func removeLoginJournal(configDir string) error {
	dir, err := config.ResolveDir(configDir)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, loginTransactionFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func transactionConfigurationFailure() *output.Error {
	return &output.Error{
		ExitCode: 3,
		Code:     "configuration_error",
		Message:  "Unable to update stored configuration",
	}
}
