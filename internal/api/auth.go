package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/output"
)

// User identifies the authenticated Artisan user.
type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
}

// Organization identifies the tenant bound to the bearer credential.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Identity is the user, tenant, and role bound to a bearer credential.
type Identity struct {
	User         User         `json:"user"`
	Organization Organization `json:"organization"`
	Role         string       `json:"role"`
}

// Identity verifies the configured bearer credential and returns its identity.
func (c *Client) Identity(ctx context.Context) (Identity, *output.Error) {
	var identity Identity
	if failure := c.Do(ctx, Request{
		Method: http.MethodGet,
		Path:   "/api/v1/auth/me",
	}, &identity); failure != nil {
		return Identity{}, failure
	}
	forbiddenValues := []string{c.token, c.serverURL.String()}
	if !validIdentity(identity) || identityContainsAny(identity, forbiddenValues) {
		return Identity{}, invalidServerResponseAvoiding(http.StatusOK, forbiddenValues)
	}
	return identity, nil
}

func validIdentity(identity Identity) bool {
	required := []string{
		identity.User.ID,
		identity.User.Email,
		identity.User.Nickname,
		identity.Organization.ID,
		identity.Organization.Name,
		identity.Organization.Slug,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return identity.Role == "admin" || identity.Role == "member"
}

func identityContainsAny(identity Identity, forbiddenValues []string) bool {
	values := []string{
		identity.User.ID,
		identity.User.Email,
		identity.User.Nickname,
		identity.Organization.ID,
		identity.Organization.Name,
		identity.Organization.Slug,
		identity.Role,
	}
	for _, value := range values {
		if containsAny(value, forbiddenValues) {
			return true
		}
	}
	return false
}
