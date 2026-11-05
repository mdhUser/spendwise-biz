// Package authclient validates opaque bearer tokens against spendwise-auth.
//
// spendwise-biz never reads the auth service's users or sessions tables. The
// only contract is GET /v1/auth/me, which returns the authenticated user for a
// valid Authorization: Bearer token.
package authclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrUnauthenticated means the token is missing, invalid, expired, revoked,
	// or belongs to a user that is not active.
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	// ErrUnavailable means the auth service could not be reached or returned an
	// unexpected response. Callers should map it to HTTP 503.
	ErrUnavailable = errors.New("auth: service unavailable")
)

// User is the subset of the auth service's user representation that the
// business service needs. ID is the auth users.id string and is used as the
// ledger owner ID.
type User struct {
	ID          string  `json:"id"`
	UserType    string  `json:"user_type"`
	Status      string  `json:"status"`
	DisplayName *string `json:"display_name"`
}

type meResponse struct {
	User User `json:"user"`
}

// Client is a small HTTP client for the auth service's introspection endpoint.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a client for baseURL. A non-positive timeout falls back to 5s.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Authenticate calls GET {baseURL}/v1/auth/me with the supplied bearer token.
func (c *Client) Authenticate(ctx context.Context, token string) (User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, ErrUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/auth/me", nil)
	if err != nil {
		return User{}, fmt.Errorf("%w: build request: %v", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return User{}, ErrUnauthenticated
	case resp.StatusCode != http.StatusOK:
		return User{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}

	var body meResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&body); err != nil {
		return User{}, fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	if strings.TrimSpace(body.User.ID) == "" {
		return User{}, fmt.Errorf("%w: empty user id", ErrUnavailable)
	}
	if body.User.Status != "" && body.User.Status != "active" {
		return User{}, ErrUnauthenticated
	}
	return body.User, nil
}

type contextKey struct{}

// WithUser stores the authenticated user in the request context.
func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, contextKey{}, user)
}

// UserFromContext returns the authenticated user stored by the auth middleware.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(contextKey{}).(User)
	return user, ok
}
