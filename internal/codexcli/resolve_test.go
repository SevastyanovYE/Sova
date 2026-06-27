package codexcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfiguredExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestResolveRejectsConfiguredNonExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(path); err == nil {
		t.Fatal("expected non-executable path to fail")
	}
}
