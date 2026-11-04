package token

import "testing"

func TestTokenHashIsStable(t *testing.T) {
	value, hash, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(value) < 40 {
		t.Fatalf("token too short: %d", len(value))
	}
	if len(hash) != 32 {
		t.Fatalf("hash length = %d", len(hash))
	}
	again := Hash(value)
	if string(again) != string(hash) {
		t.Fatal("hash mismatch")
	}
	if string(Hash(value+"x")) == string(hash) {
		t.Fatal("different token produced same hash")
	}
}
