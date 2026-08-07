// Package config loads and persists Artisan CLI configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const configFileName = "config.json"

// Origin identifies where an effective configuration value came from.
type Origin string

const (
	OriginEnvironment Origin = "environment"
	OriginStored      Origin = "stored"
)

// Source records the origin of independently resolved configuration values.
type Source struct {
	ServerURL Origin
	Token     Origin
}

// Values contains the effective server and credential configuration.
type Values struct {
	ServerURL string
	Token     string
	Source    Source
}

type configFile struct {
	ServerURL string `json:"server_url"`
}

// NormalizeServerURL validates a server origin and removes its single root
// trailing slash. HTTPS is required except for loopback HTTP origins.
func NormalizeServerURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid_server_url: malformed URL")
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", errors.New("invalid_server_url: an absolute URL with a host is required")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("invalid_server_url: port must not be empty")
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("invalid_server_url: port must be between 1 and 65535")
		}
	}
	if parsed.User != nil {
		return "", errors.New("invalid_server_url: credentials are not allowed")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("invalid_server_url: query strings are not allowed")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("invalid_server_url: fragments are not allowed")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("invalid_server_url: an origin must not contain a path")
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", errors.New("invalid_server_url: HTTP is allowed only for loopback servers")
		}
	default:
		return "", fmt.Errorf("invalid_server_url: unsupported scheme %q", parsed.Scheme)
	}

	if parsed.Path == "/" {
		return strings.TrimSuffix(raw, "/"), nil
	}
	return raw, nil
}

// Load resolves server and token independently using environment variables
// before stored values. It never persists environment overrides.
func Load(configDir string, getenv func(string) string) (Values, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	dir, err := resolveConfigDir(configDir)
	if err != nil {
		return Values{}, err
	}

	var values Values
	if environmentServer := getenv("ARTISAN_SERVER_URL"); environmentServer != "" {
		values.ServerURL, err = NormalizeServerURL(environmentServer)
		if err != nil {
			return Values{}, err
		}
		values.Source.ServerURL = OriginEnvironment
	} else {
		values.ServerURL, err = loadStoredServer(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Values{}, err
		}
		if err == nil {
			values.Source.ServerURL = OriginStored
		}
	}

	if environmentToken := getenv("ARTISAN_SERVER_TOKEN"); environmentToken != "" {
		if err := validateToken(environmentToken); err != nil {
			return Values{}, err
		}
		values.Token = environmentToken
		values.Source.Token = OriginEnvironment
	} else {
		values.Token, err = auth.NewFileStore(dir).Load()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Values{}, err
		}
		if err == nil {
			values.Source.Token = OriginStored
		}
	}

	missing := make([]string, 0, 2)
	if values.ServerURL == "" {
		missing = append(missing, "server URL")
	}
	if values.Token == "" {
		missing = append(missing, "server token")
	}
	if len(missing) != 0 {
		return Values{}, fmt.Errorf("missing_configuration: %s is not configured", strings.Join(missing, " and "))
	}
	return values, nil
}

// SaveServer validates and atomically persists the canonical server origin.
func SaveServer(configDir, serverURL string) error {
	normalized, err := NormalizeServerURL(serverURL)
	if err != nil {
		return err
	}
	dir, err := resolveConfigDir(configDir)
	if err != nil {
		return err
	}
	contents, err := json.Marshal(configFile{ServerURL: normalized})
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	contents = append(contents, '\n')
	if err := securefile.AtomicWrite(dir, configFileName, contents); err != nil {
		return fmt.Errorf("save configuration: %w", err)
	}
	return nil
}

type privateOpener func(string) (*os.File, error)

func loadStoredServer(configDir string) (string, error) {
	return loadStoredServerWithOpener(configDir, securefile.OpenPrivate)
}

func loadStoredServerWithOpener(configDir string, opener privateOpener) (string, error) {
	file, err := opener(filepath.Join(configDir, configFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("unsafe_configuration: %w", err)
	}
	defer file.Close()

	var stored configFile
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return "", fmt.Errorf("decode configuration: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("decode configuration: %w", err)
	}
	serverURL, err := NormalizeServerURL(stored.ServerURL)
	if err != nil {
		return "", err
	}
	return serverURL, nil
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
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
