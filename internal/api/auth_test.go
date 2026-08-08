package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const identityTestToken = "test-secret-token"

func TestIdentityAcceptsCompleteResponseAndAdditiveFields(t *testing.T) {
	var receivedAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("request = %s %s, want GET /api/v1/auth/me", r.Method, r.URL.Path)
		}
		receivedAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner","future":"accepted"},
			"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"My Roastery","slug":"my-roastery","future":true},
			"role":"admin",
			"future":{"nested":true}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, identityTestToken, time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	identity, failure := client.Identity(context.Background())
	if failure != nil {
		t.Fatalf("Identity() failure code = %q", failure.Code)
	}
	if identity.User.ID != "11111111-1111-4111-8111-111111111111" || identity.User.Email != "owner@example.com" || identity.User.Nickname != "Owner" {
		t.Fatalf("Identity() returned unexpected user")
	}
	if identity.Organization.ID != "22222222-2222-4222-8222-222222222222" || identity.Organization.Name != "My Roastery" || identity.Organization.Slug != "my-roastery" {
		t.Fatalf("Identity() returned unexpected organization")
	}
	if identity.Role != "admin" {
		t.Fatalf("Identity() role = %q, want admin", identity.Role)
	}
	if receivedAuthorization != "Bearer "+identityTestToken {
		t.Fatal("Identity() did not send the expected bearer credential")
	}
}

func TestIdentityRejectsMissingOrInvalidRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing user", body: `{"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank user id", body: `{"user":{"id":" ","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank email", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank nickname", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":""},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "missing organization", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"role":"admin"}`},
		{name: "blank organization id", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank organization name", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"","slug":"org"},"role":"admin"}`},
		{name: "blank organization slug", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":""},"role":"admin"}`},
		{name: "missing role", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"}}`},
		{name: "unknown role", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"owner"}`},
		{name: "malformed user UUID", body: `{"user":{"id":"not-a-uuid","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "noncanonical organization UUID", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-22222222222A","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "control in nickname", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"bad\nname"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "control in organization name", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"bad\tname","slug":"org"},"role":"admin"}`},
		{name: "nickname too long", body: fmt.Sprintf(`{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":%q},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`, strings.Repeat("n", 101))},
		{name: "email too long", body: fmt.Sprintf(`{"user":{"id":"11111111-1111-4111-8111-111111111111","email":%q,"nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`, strings.Repeat("e", 321))},
		{name: "organization name too long", body: fmt.Sprintf(`{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":%q,"slug":"org"},"role":"admin"}`, strings.Repeat("o", 201))},
		{name: "organization slug too long", body: fmt.Sprintf(`{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"n"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":%q},"role":"admin"}`, strings.Repeat("s", 101))},
		{name: "credential reflected in identity", body: `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"e","nickname":"test-secret-token"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":"Org","slug":"org"},"role":"admin"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, identityTestToken, time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}

			_, failure := client.Identity(context.Background())
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("Identity() failure = %#v, want invalid_server_response", failure)
			}
			if strings.Contains(failure.Code, identityTestToken) || strings.Contains(failure.Message, identityTestToken) {
				t.Fatal("Identity() failure exposed the bearer credential")
			}
		})
	}
}

func TestIdentityRejectsServerOriginReflection(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"user":{"id":"11111111-1111-4111-8111-111111111111","email":"owner@example.com","nickname":"Owner"},"organization":{"id":"22222222-2222-4222-8222-222222222222","name":%q,"slug":"org"},"role":"admin"}`, server.URL)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, identityTestToken, time.Second)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, failure := client.Identity(context.Background())
	if failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("Identity() failure = %#v, want invalid_server_response", failure)
	}
	if strings.Contains(failure.Message, server.URL) || strings.Contains(failure.Code, server.URL) {
		t.Fatal("identity failure reflected server origin")
	}
}
