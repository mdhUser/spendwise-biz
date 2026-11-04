package password

import "testing"

func TestHashAndVerify(t *testing.T) {
	encoded, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("correct horse battery staple", encoded) {
		t.Fatal("expected password to verify")
	}
	if Verify("wrong password", encoded) {
		t.Fatal("wrong password verified")
	}
	if Verify("anything", "not-a-hash") {
		t.Fatal("invalid hash verified")
	}
}
