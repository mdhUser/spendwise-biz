package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdhuser/spendwise-biz/internal/authclient"
	"github.com/mdhuser/spendwise-biz/internal/identity"
)

type fakeVerifier struct {
	principal identity.Principal
	err       error
	ready     bool
	calls     int
}

func (f *fakeVerifier) Verify(context.Context, string) (identity.Principal, error) {
	f.calls++
	return f.principal, f.err
}

func (f *fakeVerifier) Ready() bool { return f.ready }

func (f *fakeVerifier) Ensure(context.Context) error {
	return f.err
}

type fakeAuthenticator struct {
	user  authclient.User
	err   error
	calls int
}

func (f *fakeAuthenticator) Authenticate(context.Context, string) (authclient.User, error) {
	f.calls++
	return f.user, f.err
}

func newAuthTestServer(verifier AccessVerifier, authenticator Authenticator) *Server {
	return New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, authenticator, verifier, Config{
		AuthCookieName:  "__Host-sw_at",
		PlainCookieName: "sw_at",
	})
}

func protectedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := authclient.UserFromContext(r.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": user.ID})
	})
}

func TestRequireAuthPrefersJWTLocally(t *testing.T) {
	verifier := &fakeVerifier{principal: identity.Principal{ID: "jwt-user", SessionID: "sid", Name: "JWT User"}, ready: true}
	authenticator := &fakeAuthenticator{user: authclient.User{ID: "opaque-user", Status: "active"}}
	server := newAuthTestServer(verifier, authenticator)

	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/accounts", nil)
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	recorder := httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Auth-Method"); got != "jwt" {
		t.Fatalf("X-Auth-Method = %q, want jwt", got)
	}
	if authenticator.calls != 0 {
		t.Fatalf("opaque /me calls = %d, want 0 on the JWT path", authenticator.calls)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifier.calls)
	}
}

func TestRequireAuthAcceptsAccessCookie(t *testing.T) {
	verifier := &fakeVerifier{principal: identity.Principal{ID: "cookie-user"}, ready: true}
	server := newAuthTestServer(verifier, &fakeAuthenticator{})
	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/accounts", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-sw_at", Value: "cookie-jwt"})
	recorder := httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Auth-Method"); got != "jwt" {
		t.Fatalf("X-Auth-Method = %q", got)
	}
}

func TestRequireAuthFallsBackToOpaqueMe(t *testing.T) {
	verifier := &fakeVerifier{err: identity.ErrInvalidToken, ready: true}
	authenticator := &fakeAuthenticator{user: authclient.User{ID: "opaque-user", Status: "active"}}
	server := newAuthTestServer(verifier, authenticator)

	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/accounts", nil)
	req.Header.Set("Authorization", "Bearer opaque-token")
	recorder := httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Auth-Method"); got != "opaque" {
		t.Fatalf("X-Auth-Method = %q, want opaque", got)
	}
	if authenticator.calls != 1 {
		t.Fatalf("opaque /me calls = %d, want 1", authenticator.calls)
	}
}

func TestRequireAuthMapsFailures(t *testing.T) {
	server := newAuthTestServer(
		&fakeVerifier{err: identity.ErrInvalidToken, ready: true},
		&fakeAuthenticator{err: authclient.ErrUnauthenticated},
	)
	req := httptest.NewRequest(http.MethodGet, "/v1/ledger/accounts", nil)
	req.Header.Set("Authorization", "Bearer bad")
	recorder := httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}

	server = newAuthTestServer(
		&fakeVerifier{err: identity.ErrInvalidToken, ready: true},
		&fakeAuthenticator{err: authclient.ErrUnavailable},
	)
	recorder = httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "AUTH_UNAVAILABLE" {
		t.Fatalf("error code = %q", body.Error.Code)
	}
}

func TestRequireAuthRejectsMissingTokenWithoutCalls(t *testing.T) {
	verifier := &fakeVerifier{ready: true}
	authenticator := &fakeAuthenticator{}
	server := newAuthTestServer(verifier, authenticator)
	recorder := httptest.NewRecorder()
	server.requireAuth(protectedHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ledger/accounts", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if verifier.calls != 0 || authenticator.calls != 0 {
		t.Fatalf("verifier=%d auth=%d calls, want 0/0", verifier.calls, authenticator.calls)
	}
}

func TestReadyzFailsWhenJWKSNotLoaded(t *testing.T) {
	verifier := &fakeVerifier{ready: false}
	server := newAuthTestServer(verifier, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "jwks" {
		t.Fatalf("reason = %v", body["reason"])
	}
}
