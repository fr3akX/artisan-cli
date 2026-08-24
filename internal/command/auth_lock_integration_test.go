package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/auth"
	"github.com/fr3akX/artisan-cli/internal/config"
	"github.com/fr3akX/artisan-cli/internal/securefile"
)

func TestAuthStateLockPreventsMixedPairAcrossProcessesAndRecoversCrash(t *testing.T) {
	for _, crash := range []bool{false, true} {
		name := "publish"
		if crash {
			name = "crash"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			oldRequests := make(chan string, 1)
			oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				oldRequests <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{}`)
			}))
			defer oldServer.Close()
			newRequests := make(chan string, 1)
			newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				newRequests <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{}`)
			}))
			defer newServer.Close()

			if failure := persistExplicitLogin(dir, "token-A", oldServer.URL, nil); failure != nil {
				t.Fatal(failure)
			}
			ready := filepath.Join(t.TempDir(), "ready")
			proceed := filepath.Join(t.TempDir(), "proceed")
			cmd := exec.Command(os.Args[0], "-test.run=^TestAuthStateLockHelperProcess$")
			cmd.Env = append(os.Environ(),
				"ARTISAN_AUTH_LOCK_HELPER=1", "ARTISAN_AUTH_DIR="+dir,
				"ARTISAN_AUTH_READY="+ready, "ARTISAN_AUTH_PROCEED="+proceed,
				"ARTISAN_AUTH_SERVER="+newServer.URL,
				fmt.Sprintf("ARTISAN_AUTH_CRASH=%t", crash),
			)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitForTestFile(t, ready)

			type result struct{ clientErr int }
			loaded := make(chan result, 1)
			go func() {
				client, code := authenticatedClient(context.Background(), Runtime{ConfigDir: dir, Getenv: func(string) string { return "" }}, false, "", time.Second)
				if client == nil {
					loaded <- result{clientErr: code}
					return
				}
				failure := client.Do(context.Background(), apiTestRequest(), nil)
				if failure != nil {
					loaded <- result{clientErr: failure.ExitCode}
					return
				}
				loaded <- result{}
			}()
			select {
			case got := <-loaded:
				t.Fatalf("snapshot escaped lock during split publication: %+v", got)
			case <-time.After(100 * time.Millisecond):
			}
			if err := os.WriteFile(proceed, []byte("go"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("helper: %v", err)
			}
			select {
			case got := <-loaded:
				if got.clientErr != 0 {
					t.Fatalf("request exit = %d", got.clientErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("locked snapshot deadlocked")
			}
			if crash {
				select {
				case authorization := <-oldRequests:
					if authorization != "Bearer token-A" {
						t.Fatalf("old server authorization = %q", authorization)
					}
				case <-time.After(time.Second):
					t.Fatal("recovered request did not use old server")
				}
				select {
				case authorization := <-newRequests:
					t.Fatalf("crash request reached new server with %q", authorization)
				default:
				}
			} else {
				select {
				case authorization := <-newRequests:
					if authorization != "Bearer token-B" {
						t.Fatalf("new server authorization = %q", authorization)
					}
				case <-time.After(time.Second):
					t.Fatal("published request did not use new server")
				}
				select {
				case authorization := <-oldRequests:
					t.Fatalf("published request reached old server with %q", authorization)
				default:
				}
			}
		})
	}
}

func TestAuthLoginHoldsStateLockThroughValidationAndPublication(t *testing.T) {
	dir := t.TempDir()
	requestStarted := make(chan struct{})
	allowResponse := make(chan struct{})
	var allowOnce sync.Once
	unblockResponse := func() { allowOnce.Do(func() { close(allowResponse) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" || r.Header.Get("Authorization") != "Bearer token-B" {
			t.Errorf("request = %s authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		close(requestStarted)
		<-allowResponse
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`)
	}))
	defer server.Close()
	defer unblockResponse()

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runAuthLogin(context.Background(), []string{"--token-stdin"}, Runtime{
			In: strings.NewReader("token-B\n"), Out: &stdout, Err: &stderr,
			ConfigDir: dir, Getenv: func(string) string { return "" },
		}, false, server.URL, time.Second)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("login did not begin remote validation")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := securefile.AcquirePrivateLock(ctx, dir, authStateLockFileName, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("state lock during validation error = %v, want deadline exceeded", err)
	}
	unblockResponse()
	select {
	case code := <-done:
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("login result = code %d stdout %q stderr %q", code, stdout.String(), stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("login deadlocked while holding state lock")
	}
	pair, err := readStoredLoginPair(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !pair.ServerPresent || pair.ServerURL != server.URL || !pair.TokenPresent || pair.Token != "token-B" {
		t.Fatalf("stored pair = %+v", pair)
	}
}

func TestCanceledAuthStateSnapshotIsStableInterruptedOutput(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		args := []string{"inventory", "lot", "list"}
		if jsonMode {
			args = append([]string{"--json"}, args...)
		}
		code := Run(ctx, args, Runtime{Out: &stdout, Err: &stderr, ConfigDir: t.TempDir(), Getenv: func(string) string { return "" }})
		if code != 130 {
			t.Fatalf("json=%t code = %d, want 130", jsonMode, code)
		}
		if jsonMode {
			if got := stdout.String(); got != "{\"ok\":false,\"error\":{\"code\":\"interrupted\",\"message\":\"Operation interrupted\"}}\n" || stderr.Len() != 0 {
				t.Fatalf("JSON output = stdout %q stderr %q", got, stderr.String())
			}
		} else if stdout.Len() != 0 || stderr.String() != "Operation interrupted\n" {
			t.Fatalf("human output = stdout %q stderr %q", stdout.String(), stderr.String())
		}
	}
}

func apiTestRequest() api.Request {
	return api.Request{Method: http.MethodGet, Path: "/probe", ExpectedStatus: http.StatusOK}
}

func TestAuthStateLockHelperProcess(t *testing.T) {
	if os.Getenv("ARTISAN_AUTH_LOCK_HELPER") != "1" {
		return
	}
	dir := os.Getenv("ARTISAN_AUTH_DIR")
	release, failure := acquireAuthStateLock(context.Background(), dir)
	if failure != nil {
		t.Fatal(failure)
	}
	defer release()
	prior, err := readStoredLoginPair(dir)
	if err != nil {
		t.Fatal(err)
	}
	intended := storedLoginPair{ServerPresent: true, ServerURL: os.Getenv("ARTISAN_AUTH_SERVER"), TokenPresent: true, Token: "token-B"}
	if err := writeLoginJournal(dir, pendingJournalForPairs(prior, intended)); err != nil {
		t.Fatal(err)
	}
	if err := auth.NewFileStore(dir).Save("token-B"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("ARTISAN_AUTH_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, os.Getenv("ARTISAN_AUTH_PROCEED"))
	if os.Getenv("ARTISAN_AUTH_CRASH") == "true" {
		return
	}
	if err := config.SaveServer(dir, intended.ServerURL); err != nil {
		t.Fatal(err)
	}
	if err := writeLoginJournal(dir, committedCheckpointForPair(intended)); err != nil {
		t.Fatal(err)
	}
}

func waitForTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}
