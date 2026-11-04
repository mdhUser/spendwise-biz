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

func Hash(value string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(value), salt, iterations, memory, parallelism, keyLength)
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
	got := argon2.IDKey([]byte(value), salt, params.iterations, params.memory, params.parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
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
