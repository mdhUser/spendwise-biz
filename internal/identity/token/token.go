package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func New() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	value := "swb_" + base64.RawURLEncoding.EncodeToString(raw)
	return value, Hash(value), nil
}

func Hash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
