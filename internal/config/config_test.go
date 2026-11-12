package config

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenCreatedOnce(t *testing.T) {
	dir := t.TempDir()
	first, err := Token(dir)
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	raw, err := hex.DecodeString(first)
	if err != nil || len(raw) != tokenBytes {
		t.Fatalf("token %q is not %d bytes of hex: %v", first, tokenBytes, err)
	}

	info, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token mode = %o, want 600", mode)
	}

	second, err := Token(dir)
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if first != second {
		t.Fatalf("token changed: %q then %q", first, second)
	}
}

func TestLoadUsesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SNXW_HOME", dir)
	t.Setenv("SNXW_ADDR", "127.0.0.1:9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Dir != dir {
		t.Errorf("dir = %q, want %q", cfg.Dir, dir)
	}
	if cfg.Addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q", cfg.Addr)
	}
	if cfg.Token == "" {
		t.Error("token is empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "token")); err != nil {
		t.Errorf("token file: %v", err)
	}
}

func TestAddrDefault(t *testing.T) {
	t.Setenv("SNXW_ADDR", "")
	if got := Addr(); got != DefaultAddr {
		t.Fatalf("Addr() = %q, want %q", got, DefaultAddr)
	}
}
