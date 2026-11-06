// Package identity verifies Ed25519 access tokens locally against a cached
// JWKS document. It intentionally does not import the auth service or read its
// database: the only runtime contract is GET /v1/auth/jwks.
package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalidToken means malformed input, an unknown key, a bad signature or
	// failing registered claims. Handlers map it to 401.
	ErrInvalidToken = errors.New("identity: invalid token")
	// ErrExpiredToken means the token signature is valid but exp is outside the
	// allowed clock skew.
	ErrExpiredToken = errors.New("identity: token expired")
	// ErrJWKSUnavailable means no keys could be loaded. Handlers should treat
	// this as 503 only when no local key and no opaque fallback can help.
	ErrJWKSUnavailable = errors.New("identity: jwks unavailable")
)

const (
	clockSkew       = 60 * time.Second
	unknownKIDDelay = 10 * time.Second
)

// Principal is the authenticated identity derived from JWT claims.
type Principal struct {
	ID        string
	SessionID string
	Name      string
}

// Config configures a Verifier. JWKSURL is required for remote keys; a static
// public key can be supplied as a startup fallback (recommended for auth
// outages that coincide with an api restart).
type Config struct {
	JWKSURL         string
	Issuer          string
	Audience        string
	CacheTTL        time.Duration
	HTTPTimeout     time.Duration
	StaticPublicKey string
	StaticKeyID     string
}

// Verifier is safe for concurrent use. Keys are replaced atomically on a
// successful refresh; stale keys keep working if a refresh fails.
type Verifier struct {
	cfg       Config
	client    *http.Client
	now       func() time.Time // tests override
	mu        sync.RWMutex
	keys      map[string]ed25519.PublicKey
	etag      string
	fetchedAt time.Time
	lastTry   time.Time
	staticKey ed25519.PublicKey
	staticKID string
	ready     bool
	lastErr   error

	// fetches counts network attempts; tests assert cache behaviour.
	fetches int
}

// NewVerifier builds a verifier. Call Prime during startup to decide readiness.
func NewVerifier(cfg Config) *Verifier {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 5 * time.Second
	}
	v := &Verifier{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:    time.Now,
		keys:   map[string]ed25519.PublicKey{},
	}
	if strings.TrimSpace(cfg.StaticPublicKey) != "" {
		if key, err := parseStaticKey(cfg.StaticKeyID, cfg.StaticPublicKey); err == nil {
			v.staticKey = key
			v.staticKID = cfg.StaticKeyID
			if v.staticKID == "" {
				v.staticKID = keyID(key)
			}
			v.ready = true
		} else {
			v.lastErr = fmt.Errorf("static public key: %w", err)
		}
	}
	if cfg.JWKSURL == "" && v.staticKey == nil {
		v.lastErr = ErrJWKSUnavailable
	}
	return v
}

// SetClock overrides the verifier clock in tests.
func (v *Verifier) SetClock(now func() time.Time) {
	if now != nil {
		v.now = now
	}
}

// Prime loads the JWKS once at startup. It returns an error when remote keys
// are unavailable and no static key exists; callers should log and keep the
// process running so /readyz can report the failure.
func (v *Verifier) Prime(ctx context.Context) error {
	if v.cfg.JWKSURL == "" {
		if v.staticKey != nil {
			return nil
		}
		return ErrJWKSUnavailable
	}
	err := v.fetch(ctx, false)
	if err != nil {
		v.mu.Lock()
		v.lastErr = err
		v.mu.Unlock()
		if v.staticKey != nil {
			// The embedded fallback keeps verification available; readiness is
			// still true but the next Verify may retry JWKS.
			return err
		}
		return err
	}
	return nil
}

// Ensure refreshes JWKS when the cache is empty or stale. Readiness probes call
// it so a process that started before auth was available can become ready
// without waiting for business traffic.
func (v *Verifier) Ensure(ctx context.Context) error {
	if v == nil {
		return ErrJWKSUnavailable
	}
	if v.Ready() && v.cacheFresh() {
		return nil
	}
	return v.fetch(ctx, true)
}

// Ready reports whether at least one verification key is loaded. /readyz uses
// this: a process that cannot verify tokens must not receive traffic.
func (v *Verifier) Ready() bool {
	if v == nil {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.ready
}

// LastError exposes the most recent JWKS error for readiness diagnostics.
func (v *Verifier) LastError() error {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.lastErr
}

// Fetches reports the number of JWKS HTTP attempts; tests use it.
func (v *Verifier) Fetches() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.fetches
}

// Verify checks a compact JWT. It refreshes an expired cache (5 minutes by
// default) or a key-id miss, but keeps using stale keys if that refresh fails.
func (v *Verifier) Verify(ctx context.Context, raw string) (Principal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || v == nil {
		return Principal{}, ErrInvalidToken
	}
	header, err := decodeHeader(raw)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if header.Alg != "EdDSA" || (header.Typ != "" && header.Typ != "JWT") {
		return Principal{}, ErrInvalidToken
	}

	key, ok := v.lookup(header.KID)
	if !ok {
		if !v.cacheFresh() {
			_ = v.fetch(ctx, true)
			key, ok = v.lookup(header.KID)
		}
		if !ok {
			// A kid miss can be a legitimate rotation. Rate-limit refreshes so
			// random tokens cannot hammer auth.
			if v.shouldRefresh() {
				_ = v.fetch(ctx, true)
				key, ok = v.lookup(header.KID)
			}
		}
	}
	if !ok && v.staticKey != nil && (header.KID == "" || header.KID == v.staticKID) {
		key, ok = v.staticKey, true
	}
	if !ok {
		return Principal{}, ErrInvalidToken
	}
	if !v.cacheFresh() && v.cfg.JWKSURL != "" && v.shouldRefresh() {
		// Best effort: if this fails we continue with the key we have.
		_ = v.fetch(ctx, true)
	}
	return verifySignature(raw, key, v.cfg.Issuer, v.cfg.Audience, v.now())
}

func (v *Verifier) lookup(kid string) (ed25519.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if kid == "" {
		if len(v.keys) == 1 {
			for _, key := range v.keys {
				return key, true
			}
		}
		if v.staticKey != nil {
			return v.staticKey, true
		}
		return nil, false
	}
	key, ok := v.keys[kid]
	return key, ok
}

func (v *Verifier) cacheFresh() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.keys) == 0 {
		return false
	}
	return v.now().Sub(v.fetchedAt) < v.cfg.CacheTTL
}

func (v *Verifier) shouldRefresh() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.now().Sub(v.lastTry) >= unknownKIDDelay
}

func (v *Verifier) fetch(ctx context.Context, conditional bool) error {
	if v.cfg.JWKSURL == "" {
		return ErrJWKSUnavailable
	}
	v.mu.Lock()
	v.lastTry = v.now()
	etag := v.etag
	v.fetches++
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrJWKSUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	if conditional && etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrJWKSUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		v.mu.Lock()
		v.fetchedAt = v.now()
		v.lastErr = nil
		v.ready = len(v.keys) > 0 || v.staticKey != nil
		v.mu.Unlock()
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrJWKSUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: read body: %v", ErrJWKSUnavailable, err)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: no usable Ed25519 keys", ErrJWKSUnavailable)
	}
	v.mu.Lock()
	v.keys = keys
	v.etag = resp.Header.Get("ETag")
	v.fetchedAt = v.now()
	v.lastErr = nil
	v.ready = true
	v.mu.Unlock()
	return nil
}

func parseJWKS(body []byte) (map[string]ed25519.PublicKey, error) {
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			KID string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("%w: decode jwks: %v", ErrJWKSUnavailable, err)
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" || key.KID == "" {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if key.Alg != "" && key.Alg != "EdDSA" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			continue
		}
		public := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(public, decoded)
		keys[key.KID] = public
	}
	return keys, nil
}

func parseStaticKey(kid, raw string) (ed25519.PublicKey, error) {
	value := strings.TrimSpace(raw)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == ed25519.PublicKeySize {
			public := make(ed25519.PublicKey, ed25519.PublicKeySize)
			copy(public, decoded)
			return public, nil
		}
	}
	if len(raw) == ed25519.PublicKeySize {
		public := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(public, []byte(raw))
		return public, nil
	}
	return nil, errors.New("identity: static public key must be a 32-byte Ed25519 key")
}

func keyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	KID string `json:"kid"`
}

type jwtClaims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Subject   string `json:"sub"`
	SessionID string `json:"sid"`
	JTI       string `json:"jti"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Name      string `json:"name,omitempty"`
	Version   int    `json:"ver"`
}

func decodeHeader(raw string) (jwtHeader, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return jwtHeader{}, ErrInvalidToken
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeader{}, ErrInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(decoded, &header); err != nil {
		return jwtHeader{}, ErrInvalidToken
	}
	return header, nil
}

func verifySignature(raw string, public ed25519.PublicKey, issuer, audience string, now time.Time) (Principal, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Principal{}, ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Principal{}, ErrInvalidToken
	}
	if !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
		return Principal{}, ErrInvalidToken
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return Principal{}, ErrInvalidToken
	}
	if claims.Issuer != issuer || claims.Audience != audience {
		return Principal{}, ErrInvalidToken
	}
	if claims.Version != 1 || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.SessionID) == "" {
		return Principal{}, ErrInvalidToken
	}
	if claims.ExpiresAt == 0 {
		return Principal{}, ErrInvalidToken
	}
	if now.After(time.Unix(claims.ExpiresAt, 0).Add(clockSkew)) {
		return Principal{}, ErrExpiredToken
	}
	if claims.IssuedAt > now.Add(clockSkew).Unix() {
		return Principal{}, ErrInvalidToken
	}
	return Principal{ID: claims.Subject, SessionID: claims.SessionID, Name: claims.Name}, nil
}
