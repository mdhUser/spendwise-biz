package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticateSuccess(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":"user-1","user_type":"registered","status":"active","display_name":"Demo"}}`))
	}))
	defer server.Close()

	client := New(server.URL+"/", 2*time.Second)
	user, err := client.Authenticate(context.Background(), "opaque-token")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != "user-1" || user.Status != "active" {
		t.Fatalf("user = %+v, want user-1/active", user)
	}
	if gotPath != "/v1/auth/me" {
		t.Fatalf("path = %q, want /v1/auth/me", gotPath)
	}
	if gotAuth != "Bearer opaque-token" {
		t.Fatalf("Authorization = %q, want Bearer opaque-token", gotAuth)
	}
}

func TestAuthenticateRejectsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).Authenticate(context.Background(), "expired")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestAuthenticateRejectsInactiveUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":"user-1","status":"disabled"}}`))
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).Authenticate(context.Background(), "token")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestAuthenticateMapsServerErrorToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).Authenticate(context.Background(), "token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAuthenticateMapsMalformedBodyToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).Authenticate(context.Background(), "token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAuthenticateMapsTimeoutToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"user":{"id":"user-1","status":"active"}}`))
	}))
	defer server.Close()

	_, err := New(server.URL, 50*time.Millisecond).Authenticate(context.Background(), "token")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAuthenticateRejectsEmptyTokenWithoutRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be called for an empty token")
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).Authenticate(context.Background(), strings.Repeat(" ", 3))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}
