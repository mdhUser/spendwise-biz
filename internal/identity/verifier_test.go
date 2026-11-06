package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type jwksFixture struct {
	mu       sync.Mutex
	keys     []JWKSKey
	etag     string
	status   int
	requests int
	lastINM  string
}

type JWKSKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	KID string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

func newFixture(keys ...ed25519.PublicKey) *jwksFixture {
	f := &jwksFixture{status: http.StatusOK, etag: `"v1"`}
	for _, key := range keys {
		f.keys = append(f.keys, JWKSKey{
			Kty: "OKP", Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(key),
			KID: testKID(key), Use: "sig", Alg: "EdDSA",
		})
	}
	return f
}

func (f *jwksFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.lastINM = r.Header.Get("If-None-Match")
	if f.status != http.StatusOK {
		w.WriteHeader(f.status)
		return
	}
	if f.lastINM != "" && f.lastINM == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", f.etag)
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": f.keys})
}

func (f *jwksFixture) set(keys []ed25519.PublicKey, etag string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = f.keys[:0]
	for _, key := range keys {
		f.keys = append(f.keys, JWKSKey{
			Kty: "OKP", Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(key),
			KID: testKID(key), Use: "sig", Alg: "EdDSA",
		})
	}
	f.etag = etag
	f.status = status
}

func (f *jwksFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func testKID(public ed25519.PublicKey) string {
	return KeyIDForTest(public)
}

// KeyIDForTest exposes the same derivation as jwt.KeyID without importing the
// auth module (the two repositories only share the wire contract).
func KeyIDForTest(public ed25519.PublicKey) string {
	return keyID(public)
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func signTestToken(t *testing.T, private ed25519.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(private, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func claimsFor(t *testing.T, issuer, audience string, now time.Time, kid string) map[string]any {
	t.Helper()
	return map[string]any{
		"iss": issuer, "aud": audience, "sub": "user-1", "sid": "session-1",
		"jti": "token-1", "iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(),
		"name": "Demo", "ver": 1,
	}
}

func TestVerifierPrimeAndVerify(t *testing.T) {
	public, private := newKey(t)
	fixture := newFixture(public)
	server := httptest.NewServer(fixture)
	defer server.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	verifier := NewVerifier(Config{JWKSURL: server.URL, Issuer: "spendwise-auth", Audience: "spendwise-api", CacheTTL: 5 * time.Minute})
	verifier.SetClock(func() time.Time { return now })
	if err := verifier.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if !verifier.Ready() {
		t.Fatal("verifier should be ready after Prime")
	}
	token := signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "spendwise-api", now, testKID(public)))
	principal, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if principal.ID != "user-1" || principal.SessionID != "session-1" || principal.Name != "Demo" {
		t.Fatalf("principal = %+v", principal)
	}
	if fixture.count() != 1 {
		t.Fatalf("fetches = %d, want 1", fixture.count())
	}
}

func TestVerifierUsesETagAfterCacheTTL(t *testing.T) {
	public, private := newKey(t)
	fixture := newFixture(public)
	server := httptest.NewServer(fixture)
	defer server.Close()
	current := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	verifier := NewVerifier(Config{JWKSURL: server.URL, Issuer: "spendwise-auth", Audience: "spendwise-api", CacheTTL: 5 * time.Minute})
	verifier.SetClock(func() time.Time { return current })
	if err := verifier.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "spendwise-api", current, testKID(public)))
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if fixture.count() != 1 {
		t.Fatalf("fresh cache refetched: %d", fixture.count())
	}

	current = current.Add(6 * time.Minute)
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify after TTL: %v", err)
	}
	if fixture.count() != 2 {
		t.Fatalf("fetches = %d, want 2", fixture.count())
	}
	if fixture.lastINM != `"v1"` {
		t.Fatalf("If-None-Match = %q, want ETag", fixture.lastINM)
	}
}

func TestVerifierRefreshesOnUnknownKID(t *testing.T) {
	public1, private1 := newKey(t)
	public2, private2 := newKey(t)
	fixture := newFixture(public1)
	server := httptest.NewServer(fixture)
	defer server.Close()
	current := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	verifier := NewVerifier(Config{JWKSURL: server.URL, Issuer: "spendwise-auth", Audience: "spendwise-api"})
	verifier.SetClock(func() time.Time { return current })
	if err := verifier.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldToken := signTestToken(t, private1, testKID(public1), claimsFor(t, "spendwise-auth", "spendwise-api", current, testKID(public1)))
	if _, err := verifier.Verify(context.Background(), oldToken); err != nil {
		t.Fatal(err)
	}

	// Rotate the JWKS and sign with the new key. Unknown-kid refresh is
	// rate-limited, so move past the 10s guard.
	fixture.set([]ed25519.PublicKey{public2}, `"v2"`, http.StatusOK)
	current = current.Add(11 * time.Second)
	newToken := signTestToken(t, private2, testKID(public2), claimsFor(t, "spendwise-auth", "spendwise-api", current, testKID(public2)))
	if _, err := verifier.Verify(context.Background(), newToken); err != nil {
		t.Fatalf("Verify after rotation: %v", err)
	}
	if fixture.count() != 2 {
		t.Fatalf("fetches = %d, want 2", fixture.count())
	}
}

func TestVerifierStartupFailureThenRecovery(t *testing.T) {
	public, private := newKey(t)
	fixture := newFixture(public)
	fixture.set(nil, `"v1"`, http.StatusInternalServerError)
	server := httptest.NewServer(fixture)
	defer server.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	verifier := NewVerifier(Config{JWKSURL: server.URL, Issuer: "spendwise-auth", Audience: "spendwise-api"})
	verifier.SetClock(func() time.Time { return now })
	if err := verifier.Prime(context.Background()); err == nil {
		t.Fatal("Prime should fail while JWKS is 500")
	}
	if verifier.Ready() {
		t.Fatal("verifier must not be ready without keys")
	}

	fixture.set([]ed25519.PublicKey{public}, `"v1"`, http.StatusOK)
	token := signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "spendwise-api", now, testKID(public)))
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify after recovery: %v", err)
	}
	if !verifier.Ready() {
		t.Fatal("verifier should become ready after a successful fetch")
	}
}

func TestVerifierStaticKeyFallback(t *testing.T) {
	public, private := newKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	verifier := NewVerifier(Config{
		JWKSURL:         server.URL,
		Issuer:          "spendwise-auth",
		Audience:        "spendwise-api",
		StaticPublicKey: base64.RawURLEncoding.EncodeToString(public),
		StaticKeyID:     testKID(public),
	})
	verifier.SetClock(func() time.Time { return now })
	if err := verifier.Prime(context.Background()); err == nil {
		t.Fatal("Prime should report the JWKS failure even with a fallback")
	}
	if !verifier.Ready() {
		t.Fatal("static fallback should make the verifier ready")
	}
	token := signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "spendwise-api", now, testKID(public)))
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("static Verify: %v", err)
	}
}

func TestVerifierRejectsExpiredAndWrongAudience(t *testing.T) {
	public, private := newKey(t)
	fixture := newFixture(public)
	server := httptest.NewServer(fixture)
	defer server.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	verifier := NewVerifier(Config{JWKSURL: server.URL, Issuer: "spendwise-auth", Audience: "spendwise-api"})
	verifier.SetClock(func() time.Time { return now })
	if err := verifier.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}

	expiredClaims := claimsFor(t, "spendwise-auth", "spendwise-api", now, testKID(public))
	expiredClaims["exp"] = now.Add(-2 * time.Minute).Unix()
	expired := signTestToken(t, private, testKID(public), expiredClaims)
	if _, err := verifier.Verify(context.Background(), expired); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expired err = %v, want ErrExpiredToken", err)
	}

	wrongAudience := signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "other-api", now, testKID(public)))
	if _, err := verifier.Verify(context.Background(), wrongAudience); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong audience err = %v, want ErrInvalidToken", err)
	}

	tamperedParts := splitToken(t, signTestToken(t, private, testKID(public), claimsFor(t, "spendwise-auth", "spendwise-api", now, testKID(public))))
	tamperedParts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"spendwise-auth","aud":"spendwise-api","sub":"attacker","sid":"s","iat":1,"exp":9999999999,"ver":1}`))
	if _, err := verifier.Verify(context.Background(), tamperedParts[0]+"."+tamperedParts[1]+"."+tamperedParts[2]); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered err = %v, want ErrInvalidToken", err)
	}
}

func splitToken(t *testing.T, raw string) []string {
	t.Helper()
	parts := []string{}
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			parts = append(parts, raw[start:i])
			start = i + 1
		}
	}
	parts = append(parts, raw[start:])
	if len(parts) != 3 {
		t.Fatalf("token parts = %d", len(parts))
	}
	return parts
}
