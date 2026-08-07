package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
)

func TestNormalizeServerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "https", raw: "https://artisan.example", want: "https://artisan.example"},
		{name: "https port", raw: "https://artisan.example:8443/", want: "https://artisan.example:8443"},
		{name: "localhost http", raw: "http://localhost:8000/", want: "http://localhost:8000"},
		{name: "ipv4 loopback http", raw: "http://127.0.0.1/", want: "http://127.0.0.1"},
		{name: "ipv4 loopback range http", raw: "http://127.42.0.1/", want: "http://127.42.0.1"},
		{name: "ipv6 loopback http", raw: "http://[::1]:8000/", want: "http://[::1]:8000"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.NormalizeServerURL(tt.raw)
			if err != nil {
				t.Fatalf("NormalizeServerURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeServerURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeServerURLRejectsUnsafeOrNonOriginURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "non-loopback http", raw: "http://artisan.example"},
		{name: "non-loopback private http", raw: "http://192.168.1.10"},
		{name: "credentials", raw: "https://user:secret@artisan.example"},
		{name: "query", raw: "https://artisan.example?debug=true"},
		{name: "empty query", raw: "https://artisan.example?"},
		{name: "fragment", raw: "https://artisan.example#fragment"},
		{name: "empty fragment", raw: "https://artisan.example#"},
		{name: "api path", raw: "https://artisan.example/api/v1"},
		{name: "double trailing slash", raw: "https://artisan.example//"},
		{name: "missing host", raw: "https:///api"},
		{name: "missing hostname with port", raw: "https://:443"},
		{name: "empty port", raw: "https://artisan.example:"},
		{name: "out of range port", raw: "https://artisan.example:99999"},
		{name: "unsupported scheme", raw: "ftp://artisan.example"},
		{name: "relative", raw: "artisan.example"},
		{name: "blank", raw: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := config.NormalizeServerURL(tt.raw); err == nil {
				t.Fatalf("NormalizeServerURL(%q) = %q, want error", tt.raw, got)
			}
		})
	}
}

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := config.SaveServer(dir, "https://stored.example/"); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := auth.NewFileStore(dir).Save("stored-token"); err != nil {
		t.Fatalf("Save token: %v", err)
	}

	tests := []struct {
		name string
		env  map[string]string
		want config.Values
	}{
		{
			name: "stored values",
			want: config.Values{
				ServerURL: "https://stored.example",
				Token:     "stored-token",
				Source: config.Source{
					ServerURL: config.OriginStored,
					Token:     config.OriginStored,
				},
			},
		},
		{
			name: "environment values override independently",
			env: map[string]string{
				"ARTISAN_SERVER_URL":   "https://environment.example/",
				"ARTISAN_SERVER_TOKEN": "environment-token",
			},
			want: config.Values{
				ServerURL: "https://environment.example",
				Token:     "environment-token",
				Source: config.Source{
					ServerURL: config.OriginEnvironment,
					Token:     config.OriginEnvironment,
				},
			},
		},
		{
			name: "environment server with stored token",
			env: map[string]string{
				"ARTISAN_SERVER_URL": "https://environment.example",
			},
			want: config.Values{
				ServerURL: "https://environment.example",
				Token:     "stored-token",
				Source: config.Source{
					ServerURL: config.OriginEnvironment,
					Token:     config.OriginStored,
				},
			},
		},
		{
			name: "stored server with environment token",
			env: map[string]string{
				"ARTISAN_SERVER_TOKEN": "environment-token",
			},
			want: config.Values{
				ServerURL: "https://stored.example",
				Token:     "environment-token",
				Source: config.Source{
					ServerURL: config.OriginStored,
					Token:     config.OriginEnvironment,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			got, err := config.Load(dir, getenv)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Load() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestEnvironmentOverridesDoNotRewriteStoredFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := config.SaveServer(dir, "https://stored.example"); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	if err := auth.NewFileStore(dir).Save("stored-token"); err != nil {
		t.Fatalf("Save token: %v", err)
	}

	beforeConfig := mustReadFile(t, filepath.Join(dir, "config.json"))
	beforeCredentials := mustReadFile(t, filepath.Join(dir, "credentials.json"))
	getenv := func(key string) string {
		return map[string]string{
			"ARTISAN_SERVER_URL":   "https://environment.example",
			"ARTISAN_SERVER_TOKEN": "environment-token",
		}[key]
	}
	if _, err := config.Load(dir, getenv); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := mustReadFile(t, filepath.Join(dir, "config.json")); !reflect.DeepEqual(got, beforeConfig) {
		t.Fatalf("config.json changed: got %q, want %q", got, beforeConfig)
	}
	if got := mustReadFile(t, filepath.Join(dir, "credentials.json")); !reflect.DeepEqual(got, beforeCredentials) {
		t.Fatalf("credentials.json changed: got %q, want %q", got, beforeCredentials)
	}
}

func TestLoadRequiresBothValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		getenv func(string) string
	}{
		{name: "both missing"},
		{
			name: "server missing",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := auth.NewFileStore(dir).Save("token"); err != nil {
					t.Fatalf("Save token: %v", err)
				}
			},
		},
		{
			name: "token missing",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := config.SaveServer(dir, "https://artisan.example"); err != nil {
					t.Fatalf("SaveServer: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			getenv := tt.getenv
			if getenv == nil {
				getenv = func(string) string { return "" }
			}
			if _, err := config.Load(dir, getenv); err == nil || !strings.Contains(err.Error(), "missing_configuration") {
				t.Fatalf("Load error = %v, want missing_configuration", err)
			}
		})
	}
}

func TestLoadRejectsUnknownConfigFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"server_url":"https://artisan.example","extra":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := config.Load(dir, func(key string) string {
		if key == "ARTISAN_SERVER_TOKEN" {
			return "environment-token"
		}
		return ""
	}); err == nil {
		t.Fatal("Load succeeded with unknown config field")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return contents
}
