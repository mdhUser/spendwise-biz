package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory      = 64 * 1024
	iterations  = 3
	parallelism = 2
	saltLength  = 16
	keyLength   = 32
)

// maxConcurrent bounds simultaneous Argon2 computations. One hash costs about
// 64 MiB, so more than two concurrent login/registration attempts can exhaust
// a small instance. The semaphore only wraps the Argon2 call itself; hashing
// inputs, parsing PHC strings and random salt generation stay outside it.
const maxConcurrent = 2

// dummyHash is a valid PHC string for an unguessable password. Login verifies
// against it when the account does not exist so the "unknown email" and
// "wrong password" paths perform equivalent Argon2 work.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$bJdPYr7hscISLTKXH6sgiA$ZDDKavn40UT+ReLIpXialk8K9MDC/gQGHNcuq61cehU"

var defaultLimiter = NewLimiter(maxConcurrent)

// Limiter caps concurrent Argon2 computations. Do blocks until a slot is free;
// callers must not hold it for any work other than the key derivation.
type Limiter struct {
	slots chan struct{}
}

func NewLimiter(concurrency int) *Limiter {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Limiter{slots: make(chan struct{}, concurrency)}
}

func (l *Limiter) Do(fn func()) {
	l.slots <- struct{}{}
	defer func() { <-l.slots }()
	fn()
}

func (l *Limiter) Capacity() int { return cap(l.slots) }

func Hash(value string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	var key []byte
	defaultLimiter.Do(func() {
		key = argon2.IDKey([]byte(value), salt, iterations, memory, parallelism, keyLength)
	})
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func Verify(value, encoded string) bool {
	params, salt, want, err := parse(encoded)
	if err != nil {
		return false
	}
	var ok bool
	defaultLimiter.Do(func() {
		got := argon2.IDKey([]byte(value), salt, params.iterations, params.memory, params.parallelism, uint32(len(want)))
		ok = subtle.ConstantTimeCompare(got, want) == 1
	})
	return ok
}

// VerifyDummy spends the same Argon2 work as Verify against an account that
// does not exist. Callers must ignore the result.
func VerifyDummy(value string) {
	_ = Verify(value, dummyHash)
}

type params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func parse(encoded string) (params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return params{}, nil, nil, errors.New("invalid argon2id hash")
	}
	var p params
	for _, item := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			return p, nil, nil, errors.New("invalid argon2 params")
		}
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return p, nil, nil, err
		}
		switch kv[0] {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.iterations = uint32(n)
		case "p":
			p.parallelism = uint8(n)
		}
	}
	if p.memory < 8*1024 || p.memory > 1024*1024 || p.iterations < 1 || p.iterations > 10 || p.parallelism < 1 || p.parallelism > 16 {
		return p, nil, nil, errors.New("argon2 params out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, errors.New("invalid argon2 salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, errors.New("invalid argon2 key")
	}
	return p, salt, key, nil
}
