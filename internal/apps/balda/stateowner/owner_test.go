package stateowner

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireExcludesAnotherOwnerAndReleases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrActive) {
		t.Fatalf("second owner error = %v, want ErrActive", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsEmptyDirectory(t *testing.T) {
	if _, err := Acquire("  "); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}
