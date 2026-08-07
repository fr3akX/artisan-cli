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
			"user":{"id":"user-id","email":"owner@example.com","nickname":"Owner","future":"accepted"},
			"organization":{"id":"organization-id","name":"My Roastery","slug":"my-roastery","future":true},
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
	if identity.User.ID != "user-id" || identity.User.Email != "owner@example.com" || identity.User.Nickname != "Owner" {
		t.Fatalf("Identity() returned unexpected user")
	}
	if identity.Organization.ID != "organization-id" || identity.Organization.Name != "My Roastery" || identity.Organization.Slug != "my-roastery" {
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
		{name: "missing user", body: `{"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank user id", body: `{"user":{"id":" ","email":"e","nickname":"n"},"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank email", body: `{"user":{"id":"u","email":"","nickname":"n"},"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank nickname", body: `{"user":{"id":"u","email":"e","nickname":""},"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "missing organization", body: `{"user":{"id":"u","email":"e","nickname":"n"},"role":"admin"}`},
		{name: "blank organization id", body: `{"user":{"id":"u","email":"e","nickname":"n"},"organization":{"id":"","name":"Org","slug":"org"},"role":"admin"}`},
		{name: "blank organization name", body: `{"user":{"id":"u","email":"e","nickname":"n"},"organization":{"id":"o","name":"","slug":"org"},"role":"admin"}`},
		{name: "blank organization slug", body: `{"user":{"id":"u","email":"e","nickname":"n"},"organization":{"id":"o","name":"Org","slug":""},"role":"admin"}`},
		{name: "missing role", body: `{"user":{"id":"u","email":"e","nickname":"n"},"organization":{"id":"o","name":"Org","slug":"org"}}`},
		{name: "unknown role", body: `{"user":{"id":"u","email":"e","nickname":"n"},"organization":{"id":"o","name":"Org","slug":"org"},"role":"owner"}`},
		{name: "credential reflected in identity", body: `{"user":{"id":"u","email":"e","nickname":"test-secret-token"},"organization":{"id":"o","name":"Org","slug":"org"},"role":"admin"}`},
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
