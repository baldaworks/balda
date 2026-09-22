package userpassword

import "testing"

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	password := []byte("correct horse battery staple")
	hash, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if string(password) == hash {
		t.Fatal("Hash() returned plaintext")
	}
	if !Verify(hash, password) {
		t.Fatal("Verify(correct) = false")
	}
	if Verify(hash, []byte("incorrect password")) {
		t.Fatal("Verify(incorrect) = true")
	}
}

func TestHashRejectsUnboundedPassword(t *testing.T) {
	t.Parallel()
	if _, err := Hash([]byte("short")); err == nil {
		t.Fatal("Hash(short) error = nil")
	}
	if _, err := Hash(make([]byte, MaxLength+1)); err == nil {
		t.Fatal("Hash(long) error = nil")
	}
}
