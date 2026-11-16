// Package config resolves the daemon's data directory, bind address and
// per-install auth token.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultAddr keeps the daemon on loopback.
	DefaultAddr = "127.0.0.1:7766"
	tokenBytes  = 32
)

// Config is everything the daemon and CLI need to find each other.
type Config struct {
	Dir   string
	Addr  string
	Token string
	// Features are the opt-in capabilities the operator named. Empty means
	// every autonomous behaviour stays off, which is the default.
	Features []string
}

// Load resolves the data directory, address and token, creating the directory
// and the token file on first run.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	tok, err := Token(dir)
	if err != nil {
		return nil, err
	}
	feats, err := Features(dir)
	if err != nil {
		return nil, err
	}
	return &Config{Dir: dir, Addr: Addr(), Token: tok, Features: feats}, nil
}

// FeaturesFile is where the enabled names live: one per line, 0600, beside the
// token. $SNXW_FEATURES wins when set, so a run can enable something for one
// process without editing the file.
func FeaturesFile(dir string) string { return filepath.Join(dir, "features") }

// Features reads the enabled feature names. It never fails open: a file it
// cannot read is an error, and no file means none are enabled.
func Features(dir string) ([]string, error) {
	if env := os.Getenv("SNXW_FEATURES"); env != "" {
		return strings.FieldsFunc(env, func(r rune) bool { return r == ',' || r == ' ' }), nil
	}
	raw, err := os.ReadFile(FeaturesFile(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read features: %w", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if name := strings.TrimSpace(line); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// WriteFeatures replaces the enabled set. The CLI owns this; the daemon only
// reads it.
func WriteFeatures(dir string, names []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	body := strings.Join(names, "\n")
	if body != "" {
		body += "\n"
	}
	path := FeaturesFile(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write features: %w", err)
	}
	return os.Rename(tmp, path)
}

// Dir returns $SNXW_HOME, or <user config dir>/snxw.
func Dir() (string, error) {
	if d := os.Getenv("SNXW_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "snxw"), nil
}

// Addr returns $SNXW_ADDR, or DefaultAddr.
func Addr() string {
	if a := os.Getenv("SNXW_ADDR"); a != "" {
		return a
	}
	return DefaultAddr
}

// Token reads dir/token, generating a new 32-byte token (0600) if it is absent.
func Token(dir string) (string, error) {
	path := filepath.Join(dir, "token")
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read token: %w", err)
	}

	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(buf)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) { // another process won the race
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return "", fmt.Errorf("read token: %w", rerr)
			}
			return strings.TrimSpace(string(b)), nil
		}
		return "", fmt.Errorf("create token: %w", err)
	}
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		return "", fmt.Errorf("write token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}
