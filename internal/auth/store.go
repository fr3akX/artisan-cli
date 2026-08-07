// Package auth persists Artisan bearer credentials.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const credentialsFileName = "credentials.json"

// Store persists a bearer token without exposing it as a command-line value.
type Store interface {
	Save(token string) error
	Load() (string, error)
	Remove() error
}

type fileStore struct {
	configDir string
}

type credentialsFile struct {
	Token string `json:"token"`
}

// NewFileStore returns a credential store rooted in configDir. If configDir is
// empty, the platform user configuration directory's artisan child is used.
func NewFileStore(configDir string) Store {
	return &fileStore{configDir: configDir}
}

func (s *fileStore) Save(token string) error {
	if err := validateToken(token); err != nil {
		return err
	}
	dir, err := resolveConfigDir(s.configDir)
	if err != nil {
		return err
	}
	if err := preparePrivateDirectory(dir); err != nil {
		return fmt.Errorf("prepare credential directory: %w", err)
	}

	contents, err := json.Marshal(credentialsFile{Token: token})
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	contents = append(contents, '\n')
	if err := writeCredentialsAtomically(dir, contents); err != nil {
		return err
	}
	return nil
}

func (s *fileStore) Load() (string, error) {
	dir, err := resolveConfigDir(s.configDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, credentialsFileName)
	if err := verifyPrivatePermissions(path); err != nil {
		return "", err
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open credentials: %w", err)
	}
	defer file.Close()

	var stored credentialsFile
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return "", fmt.Errorf("decode credentials: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("decode credentials: %w", err)
	}
	if err := validateToken(stored.Token); err != nil {
		return "", err
	}
	return stored.Token, nil
}

func (s *fileStore) Remove() error {
	dir, err := resolveConfigDir(s.configDir)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, credentialsFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials: %w", err)
	}
	return nil
}

func writeCredentialsAtomically(dir string, contents []byte) (err error) {
	temporary, err := os.CreateTemp(dir, ".credentials.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary credentials: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary credentials: %w", err)
	}
	if err := applyPrivatePermissions(temporaryPath); err != nil {
		return fmt.Errorf("secure temporary credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary credentials: %w", err)
	}
	if err := verifyPrivatePermissions(temporaryPath); err != nil {
		return err
	}
	path := filepath.Join(dir, credentialsFileName)
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}
	if err := verifyPrivatePermissions(path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("verify credentials: %v; remove unsafe credentials: %w", err, removeErr)
		}
		return err
	}
	return nil
}

func validateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("invalid_credentials: token must not be blank")
	}
	if strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid_credentials: token must be a single line")
	}
	return nil
}

func resolveConfigDir(configDir string) (string, error) {
	if configDir != "" {
		return configDir, nil
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(userConfigDir, "artisan"), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
