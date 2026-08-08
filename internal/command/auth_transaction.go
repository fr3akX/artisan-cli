package command

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

	legacyLoginJournalVersion = 1
	authCheckpointVersion     = 2

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

// loginTransactionJournal represents either a version 1 legacy journal, a
// version 2 pending transaction, or a version 2 committed checkpoint. Pending
// and legacy records use the raw prior/intended fields. A committed version 2
// checkpoint uses only presence bits and the fingerprint fields.
type loginTransactionJournal struct {
	State                 loginTransactionState
	Version               int
	ServerPresent         bool
	ServerURL             string
	TokenPresent          bool
	Token                 string
	IntendedServerPresent bool
	IntendedServerURL     string
	IntendedTokenPresent  bool
	IntendedToken         string
	ServerFingerprint     string
	TokenFingerprint      string
}

type pendingLoginJournalFile struct {
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

type committedLoginCheckpointFile struct {
	State             loginTransactionState `json:"state"`
	Version           int                   `json:"version"`
	ServerPresent     bool                  `json:"server_present"`
	ServerFingerprint string                `json:"server_fingerprint"`
	TokenPresent      bool                  `json:"token_present"`
	TokenFingerprint  string                `json:"token_fingerprint"`
}

type loginStageHook func(stage string) error

type loginTransactionOperations struct {
	writeJournal func(string, loginTransactionJournal) error
	saveToken    func(string, string) error
	removeToken  func(string) error
	saveServer   func(string, string) error
	removeServer func(string) error
}

func defaultLoginTransactionOperations() loginTransactionOperations {
	return loginTransactionOperations{
		writeJournal: writeLoginJournal,
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

// Transaction helpers in this file deliberately do not acquire the auth-state
// lock. Command entry points hold one lock across recovery, remote validation,
// snapshot reads, and publication so nested helper calls cannot deadlock.
func persistExplicitLogin(configDir, token, serverURL string, hook loginStageHook) *output.Error {
	return persistExplicitLoginWithOperations(configDir, token, serverURL, hook, defaultLoginTransactionOperations())
}

func persistExplicitLoginWithOperations(configDir, token, serverURL string, hook loginStageHook, operations loginTransactionOperations) *output.Error {
	prior, err := readStoredLoginPair(configDir)
	if err != nil {
		return transactionConfigurationFailure()
	}
	intended := prior
	intended.ServerPresent = true
	intended.ServerURL = serverURL
	intended.TokenPresent = true
	intended.Token = token
	return persistStoredLoginPairWithOperations(configDir, prior, intended, hook, operations)
}

func persistStoredToken(configDir, token string) *output.Error {
	prior, err := readStoredLoginPair(configDir)
	if err != nil {
		return transactionConfigurationFailure()
	}
	intended := prior
	intended.TokenPresent = true
	intended.Token = token
	return persistStoredLoginPairWithOperations(configDir, prior, intended, nil, defaultLoginTransactionOperations())
}

func persistLogout(configDir string) *output.Error {
	return persistLogoutWithOperations(configDir, nil, defaultLoginTransactionOperations())
}

func persistLogoutWithOperations(configDir string, hook loginStageHook, operations loginTransactionOperations) *output.Error {
	prior, err := readStoredLoginPair(configDir)
	if err != nil {
		return transactionConfigurationFailure()
	}
	intended := prior
	intended.TokenPresent = false
	intended.Token = ""
	return persistStoredLoginPairWithOperations(configDir, prior, intended, hook, operations)
}

func persistStoredLoginPairWithOperations(configDir string, prior, intended storedLoginPair, hook loginStageHook, operations loginTransactionOperations) *output.Error {
	if err := validateStoredLoginPair(prior); err != nil {
		return transactionConfigurationFailure()
	}
	if err := validateStoredLoginPair(intended); err != nil {
		return transactionConfigurationFailure()
	}
	journal := pendingJournalForPairs(prior, intended)
	if err := operations.writeJournal(configDir, journal); err != nil {
		return transactionConfigurationFailure()
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageJournalWritten, operations); failure != nil {
		return failure
	}

	if err := applyStoredTokenWithOperations(configDir, intended, operations); err != nil {
		if securefile.ReplacementVisible(err) {
			return transactionConfigurationFailure()
		}
		return failAndRestoreLoginTransaction(configDir, journal, operations)
	}
	if failure := interruptPendingLoginTransaction(configDir, journal, hook, loginStageTokenSaved, operations); failure != nil {
		return failure
	}

	if err := applyStoredServerWithOperations(configDir, intended, operations); err != nil {
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

	checkpoint := committedCheckpointForPair(intended)
	if err := operations.writeJournal(configDir, checkpoint); err != nil {
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
	// The fingerprinted checkpoint remains the authoritative marker. It is
	// deliberately never deleted as part of successful mutation.
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

func pendingJournalForPairs(prior, intended storedLoginPair) loginTransactionJournal {
	return loginTransactionJournal{
		State:                 loginTransactionPending,
		Version:               authCheckpointVersion,
		ServerPresent:         prior.ServerPresent,
		ServerURL:             prior.ServerURL,
		TokenPresent:          prior.TokenPresent,
		Token:                 prior.Token,
		IntendedServerPresent: intended.ServerPresent,
		IntendedServerURL:     intended.ServerURL,
		IntendedTokenPresent:  intended.TokenPresent,
		IntendedToken:         intended.Token,
	}
}

func snapshotLoginState(configDir, intendedToken, intendedServerURL string) (loginTransactionJournal, error) {
	prior, err := readStoredLoginPair(configDir)
	if err != nil {
		return loginTransactionJournal{}, err
	}
	intended := prior
	intended.ServerPresent = true
	intended.ServerURL = intendedServerURL
	intended.TokenPresent = true
	intended.Token = intendedToken
	return pendingJournalForPairs(prior, intended), nil
}

func committedCheckpointForPair(pair storedLoginPair) loginTransactionJournal {
	return loginTransactionJournal{
		State:             loginTransactionCommitted,
		Version:           authCheckpointVersion,
		ServerPresent:     pair.ServerPresent,
		TokenPresent:      pair.TokenPresent,
		ServerFingerprint: loginStateFingerprint("server", pair.ServerPresent, pair.ServerURL),
		TokenFingerprint:  loginStateFingerprint("token", pair.TokenPresent, pair.Token),
	}
}

func loginStateFingerprint(kind string, present bool, value string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("artisan-cli-auth-checkpoint-v2\x00"))
	_, _ = hash.Write([]byte(kind))
	if present {
		_, _ = hash.Write([]byte("\x001\x00"))
	} else {
		_, _ = hash.Write([]byte("\x000\x00"))
	}
	_, _ = hash.Write([]byte(value))
	return hex.EncodeToString(hash.Sum(nil))
}

func checkpointMatchesPair(checkpoint loginTransactionJournal, pair storedLoginPair) bool {
	if checkpoint.State != loginTransactionCommitted || checkpoint.Version != authCheckpointVersion ||
		checkpoint.ServerPresent != pair.ServerPresent || checkpoint.TokenPresent != pair.TokenPresent {
		return false
	}
	serverFingerprint := loginStateFingerprint("server", pair.ServerPresent, pair.ServerURL)
	tokenFingerprint := loginStateFingerprint("token", pair.TokenPresent, pair.Token)
	return subtle.ConstantTimeCompare([]byte(checkpoint.ServerFingerprint), []byte(serverFingerprint)) == 1 &&
		subtle.ConstantTimeCompare([]byte(checkpoint.TokenFingerprint), []byte(tokenFingerprint)) == 1
}

func writeLoginJournal(configDir string, journal loginTransactionJournal) error {
	if err := validateLoginJournal(journal); err != nil {
		return err
	}
	var encoded any
	if journal.Version == authCheckpointVersion && journal.State == loginTransactionCommitted {
		encoded = committedLoginCheckpointFile{
			State:             journal.State,
			Version:           journal.Version,
			ServerPresent:     journal.ServerPresent,
			ServerFingerprint: journal.ServerFingerprint,
			TokenPresent:      journal.TokenPresent,
			TokenFingerprint:  journal.TokenFingerprint,
		}
	} else {
		encoded = pendingLoginJournalFile{
			State:                 journal.State,
			Version:               journal.Version,
			ServerPresent:         journal.ServerPresent,
			ServerURL:             journal.ServerURL,
			TokenPresent:          journal.TokenPresent,
			Token:                 journal.Token,
			IntendedServerPresent: journal.IntendedServerPresent,
			IntendedServerURL:     journal.IntendedServerURL,
			IntendedTokenPresent:  journal.IntendedTokenPresent,
			IntendedToken:         journal.IntendedToken,
		}
	}
	contents, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	dir, err := config.ResolveDir(configDir)
	if err != nil {
		return err
	}
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
		return loginTransactionJournal{}, invalidLoginJournal()
	}

	known := map[string]bool{
		"state": true, "version": true, "server_present": true,
		"server_url": true, "server_fingerprint": true,
		"token_present": true, "token": true, "token_fingerprint": true,
		"intended_server_present": true, "intended_server_url": true,
		"intended_token_present": true, "intended_token": true,
	}
	values := make(map[string]json.RawMessage, len(known))
	for decoder.More() {
		fieldToken, err := decoder.Token()
		field, ok := fieldToken.(string)
		if err != nil || !ok || !known[field] {
			return loginTransactionJournal{}, invalidLoginJournal()
		}
		if _, duplicate := values[field]; duplicate {
			return loginTransactionJournal{}, invalidLoginJournal()
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return loginTransactionJournal{}, invalidLoginJournal()
		}
		values[field] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return loginTransactionJournal{}, invalidLoginJournal()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return loginTransactionJournal{}, invalidLoginJournal()
	}

	var journal loginTransactionJournal
	stateRaw, stateOK := values["state"]
	versionRaw, versionOK := values["version"]
	if !stateOK || !versionOK || !decodeJournalString(stateRaw, (*string)(&journal.State)) ||
		!decodeJournalInteger(versionRaw, &journal.Version) {
		return loginTransactionJournal{}, invalidLoginJournal()
	}

	var required []string
	switch {
	case journal.Version == legacyLoginJournalVersion &&
		(journal.State == loginTransactionPending || journal.State == loginTransactionCommitted):
		required = pendingJournalFields()
	case journal.Version == authCheckpointVersion && journal.State == loginTransactionPending:
		required = pendingJournalFields()
	case journal.Version == authCheckpointVersion && journal.State == loginTransactionCommitted:
		required = []string{"state", "version", "server_present", "server_fingerprint", "token_present", "token_fingerprint"}
	default:
		return loginTransactionJournal{}, invalidLoginJournal()
	}
	if !hasExactJournalFields(values, required) {
		return loginTransactionJournal{}, invalidLoginJournal()
	}

	if journal.Version == authCheckpointVersion && journal.State == loginTransactionCommitted {
		if !decodeJournalBoolean(values["server_present"], &journal.ServerPresent) ||
			!decodeJournalString(values["server_fingerprint"], &journal.ServerFingerprint) ||
			!decodeJournalBoolean(values["token_present"], &journal.TokenPresent) ||
			!decodeJournalString(values["token_fingerprint"], &journal.TokenFingerprint) {
			return loginTransactionJournal{}, invalidLoginJournal()
		}
		return journal, nil
	}

	if !decodeJournalBoolean(values["server_present"], &journal.ServerPresent) ||
		!decodeJournalString(values["server_url"], &journal.ServerURL) ||
		!decodeJournalBoolean(values["token_present"], &journal.TokenPresent) ||
		!decodeJournalString(values["token"], &journal.Token) ||
		!decodeJournalBoolean(values["intended_server_present"], &journal.IntendedServerPresent) ||
		!decodeJournalString(values["intended_server_url"], &journal.IntendedServerURL) ||
		!decodeJournalBoolean(values["intended_token_present"], &journal.IntendedTokenPresent) ||
		!decodeJournalString(values["intended_token"], &journal.IntendedToken) {
		return loginTransactionJournal{}, invalidLoginJournal()
	}
	return journal, nil
}

func pendingJournalFields() []string {
	return []string{
		"state", "version", "server_present", "server_url", "token_present", "token",
		"intended_server_present", "intended_server_url", "intended_token_present", "intended_token",
	}
}

func hasExactJournalFields(values map[string]json.RawMessage, required []string) bool {
	if len(values) != len(required) {
		return false
	}
	for _, field := range required {
		if _, ok := values[field]; !ok {
			return false
		}
	}
	return true
}

func invalidLoginJournal() error {
	return errors.New("invalid login transaction journal")
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
	switch {
	case journal.Version == legacyLoginJournalVersion &&
		(journal.State == loginTransactionPending || journal.State == loginTransactionCommitted):
		if journal.ServerFingerprint != "" || journal.TokenFingerprint != "" {
			return invalidLoginJournal()
		}
	case journal.Version == authCheckpointVersion && journal.State == loginTransactionPending:
		if journal.ServerFingerprint != "" || journal.TokenFingerprint != "" {
			return invalidLoginJournal()
		}
	case journal.Version == authCheckpointVersion && journal.State == loginTransactionCommitted:
		if journal.ServerURL != "" || journal.Token != "" || journal.IntendedServerPresent ||
			journal.IntendedServerURL != "" || journal.IntendedTokenPresent || journal.IntendedToken != "" {
			return invalidLoginJournal()
		}
		if !validLoginFingerprint(journal.ServerFingerprint) || !validLoginFingerprint(journal.TokenFingerprint) {
			return invalidLoginJournal()
		}
		if !journal.ServerPresent && journal.ServerFingerprint != loginStateFingerprint("server", false, "") {
			return invalidLoginJournal()
		}
		if !journal.TokenPresent && journal.TokenFingerprint != loginStateFingerprint("token", false, "") {
			return invalidLoginJournal()
		}
		return nil
	default:
		return invalidLoginJournal()
	}

	if err := validateJournalPair(journal.ServerPresent, journal.ServerURL, journal.TokenPresent, journal.Token); err != nil {
		return err
	}
	if err := validateJournalPair(journal.IntendedServerPresent, journal.IntendedServerURL, journal.IntendedTokenPresent, journal.IntendedToken); err != nil {
		return err
	}
	return nil
}

func validLoginFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value) && len(decoded) == sha256.Size
}

func validateJournalPair(serverPresent bool, serverURL string, tokenPresent bool, token string) error {
	return validateStoredLoginPair(storedLoginPair{
		ServerPresent: serverPresent,
		ServerURL:     serverURL,
		TokenPresent:  tokenPresent,
		Token:         token,
	})
}

type storedLoginPair struct {
	ServerPresent bool
	ServerURL     string
	TokenPresent  bool
	Token         string
}

func validateStoredLoginPair(pair storedLoginPair) error {
	if pair.ServerPresent {
		if len(pair.ServerURL) > 4096 {
			return invalidLoginJournal()
		}
		normalized, err := config.NormalizeServerURL(pair.ServerURL)
		if err != nil || normalized != pair.ServerURL {
			return invalidLoginJournal()
		}
	} else if pair.ServerURL != "" {
		return invalidLoginJournal()
	}
	if pair.TokenPresent {
		if strings.TrimSpace(pair.Token) == "" || strings.ContainsAny(pair.Token, "\r\n") || len(pair.Token) > maxTokenInputBytes {
			return invalidLoginJournal()
		}
	} else if pair.Token != "" {
		return invalidLoginJournal()
	}
	return nil
}

func recoverLoginTransaction(configDir string) error {
	return recoverLoginTransactionWithOperations(configDir, defaultLoginTransactionOperations())
}

func recoverLoginTransactionWithOperations(configDir string, operations loginTransactionOperations) error {
	journal, err := readLoginJournal(configDir)
	if errors.Is(err, os.ErrNotExist) {
		current, currentErr := readStoredLoginPair(configDir)
		if currentErr != nil {
			return currentErr
		}
		// Bootstrap configurations created before checkpoints (including normal
		// states from the unreleased version 1 protocol) by durably fingerprinting
		// the exact pair before the operation is allowed to continue.
		if err := operations.writeJournal(configDir, committedCheckpointForPair(current)); err != nil {
			return fmt.Errorf("establish authentication checkpoint: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}

	if journal.Version == legacyLoginJournalVersion {
		return migrateLegacyLoginJournal(configDir, journal, operations)
	}
	if journal.State == loginTransactionCommitted {
		current, currentErr := readStoredLoginPair(configDir)
		if currentErr != nil {
			return fmt.Errorf("validate authentication checkpoint: %w", currentErr)
		}
		if !checkpointMatchesPair(journal, current) {
			return errors.New("stored authentication state does not match committed checkpoint")
		}
		// Replacing a matching checkpoint with itself makes any prior ambiguous
		// replacement durable before a later mutation can install a pending record.
		if err := operations.writeJournal(configDir, journal); err != nil {
			return fmt.Errorf("refresh authentication checkpoint: %w", err)
		}
		return nil
	}

	prior, intended := pairsFromPendingJournal(journal)
	current, currentErr := readStoredLoginPair(configDir)
	resolved := prior
	if currentErr == nil && current == intended {
		resolved = intended
	}
	// A version 2 pending marker was installed before data mutation and is never
	// deleted. Mixed or unreadable state therefore resolves deterministically to
	// the protected prior pair; an exact intended pair rolls forward.
	if err := applyStoredLoginPairWithOperations(configDir, resolved, operations); err != nil {
		return fmt.Errorf("resolve authentication transaction: %w", err)
	}
	if err := operations.writeJournal(configDir, committedCheckpointForPair(resolved)); err != nil {
		return fmt.Errorf("commit recovered authentication transaction: %w", err)
	}
	return nil
}

func migrateLegacyLoginJournal(configDir string, journal loginTransactionJournal, operations loginTransactionOperations) error {
	current, err := readStoredLoginPair(configDir)
	if err != nil {
		return fmt.Errorf("cannot safely migrate legacy authentication journal: %w", err)
	}
	prior, intended := pairsFromPendingJournal(journal)
	var resolved storedLoginPair
	switch journal.State {
	case loginTransactionCommitted:
		if current != intended {
			return errors.New("legacy committed authentication journal does not match stored state")
		}
		resolved = intended
	case loginTransactionPending:
		switch current {
		case prior:
			resolved = prior
		case intended:
			resolved = intended
		default:
			return errors.New("legacy pending authentication journal is ambiguous")
		}
	default:
		return invalidLoginJournal()
	}
	if err := applyStoredLoginPairWithOperations(configDir, resolved, operations); err != nil {
		return fmt.Errorf("make migrated authentication state durable: %w", err)
	}
	if err := operations.writeJournal(configDir, committedCheckpointForPair(resolved)); err != nil {
		return fmt.Errorf("commit migrated authentication checkpoint: %w", err)
	}
	return nil
}

func pairsFromPendingJournal(journal loginTransactionJournal) (storedLoginPair, storedLoginPair) {
	return storedLoginPair{
			ServerPresent: journal.ServerPresent,
			ServerURL:     journal.ServerURL,
			TokenPresent:  journal.TokenPresent,
			Token:         journal.Token,
		}, storedLoginPair{
			ServerPresent: journal.IntendedServerPresent,
			ServerURL:     journal.IntendedServerURL,
			TokenPresent:  journal.IntendedTokenPresent,
			Token:         journal.IntendedToken,
		}
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
	var applyErrors []error
	if err := applyStoredServerWithOperations(configDir, pair, operations); err != nil {
		applyErrors = append(applyErrors, err)
	}
	if err := applyStoredTokenWithOperations(configDir, pair, operations); err != nil {
		applyErrors = append(applyErrors, err)
	}
	return errors.Join(applyErrors...)
}

func applyStoredServerWithOperations(configDir string, pair storedLoginPair, operations loginTransactionOperations) error {
	if pair.ServerPresent {
		return operations.saveServer(configDir, pair.ServerURL)
	}
	return operations.removeServer(configDir)
}

func applyStoredTokenWithOperations(configDir string, pair storedLoginPair, operations loginTransactionOperations) error {
	if pair.TokenPresent {
		if err := operations.saveToken(configDir, pair.Token); err != nil {
			// Do not leave a different credential usable when the protected value
			// could not be made durable. The pending marker remains authoritative
			// until a later recovery completes.
			return errors.Join(err, operations.removeToken(configDir))
		}
		return nil
	}
	return operations.removeToken(configDir)
}

func failAndRestoreLoginTransaction(configDir string, journal loginTransactionJournal, operations loginTransactionOperations) *output.Error {
	prior, _ := pairsFromPendingJournal(journal)
	if err := applyStoredLoginPairWithOperations(configDir, prior, operations); err != nil {
		return transactionConfigurationFailure()
	}
	if err := operations.writeJournal(configDir, committedCheckpointForPair(prior)); err != nil {
		return transactionConfigurationFailure()
	}
	return transactionConfigurationFailure()
}

func restoreLoginStateWithOperations(configDir string, journal loginTransactionJournal, operations loginTransactionOperations) error {
	prior, _ := pairsFromPendingJournal(journal)
	return applyStoredLoginPairWithOperations(configDir, prior, operations)
}

func transactionConfigurationFailure() *output.Error {
	return &output.Error{
		ExitCode: 3,
		Code:     "configuration_error",
		Message:  "Unable to update stored configuration",
	}
}
