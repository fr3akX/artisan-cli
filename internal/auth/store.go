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

	"github.com/fr3akX/artisan-cli/internal/securefile"
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
	contents, err := json.Marshal(credentialsFile{Token: token})
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	contents = append(contents, '\n')
	if err := securefile.AtomicWrite(dir, credentialsFileName, contents); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

func (s *fileStore) Load() (string, error) {
	return s.load(securefile.OpenPrivate)
}

type privateOpener func(string) (*os.File, error)

func (s *fileStore) load(opener privateOpener) (string, error) {
	dir, err := resolveConfigDir(s.configDir)
	if err != nil {
		return "", err
	}
	file, err := opener(filepath.Join(dir, credentialsFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("unsafe_credentials: %w", err)
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
	if err := securefile.DurableRemove(dir, credentialsFileName); err != nil {
		return fmt.Errorf("remove credentials: %w", err)
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
