// Package auth stores and resolves printer LAN access codes. Codes are never printed or logged.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/voska/bambu/internal/errfmt"
)

// Service is the keychain service name; the account is the printer serial.
const Service = "bambu"

// Sources of an access code, in resolution order.
const (
	SourceKeychain    = "keychain"
	SourceEnv         = "env"
	SourceBambuStudio = "bambu_studio"
)

// ErrNotFound is returned by a Keyring when no secret is stored.
var ErrNotFound = errors.New("secret not found")

// Keyring is the OS secret store (swappable in tests).
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// System is the OS keychain (macOS Keychain, Secret Service, Windows Credential Manager).
type System struct{}

// Get reads a secret.
func (System) Get(service, user string) (string, error) {
	s, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return s, err //nolint:wrapcheck // mapped by caller
}

// Set stores a secret. On macOS go-keyring passes it to `security -i` on stdin, never argv.
func (System) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret) //nolint:wrapcheck // mapped by caller
}

// Delete removes a secret.
func (System) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err //nolint:wrapcheck // mapped by caller
}

var codeRe = regexp.MustCompile(`^[A-Za-z0-9]{4,32}$`)

// ValidateCode checks the shape of an access code (8 alphanumerics on current printers).
func ValidateCode(code string) error {
	if !codeRe.MatchString(code) {
		return errfmt.New(errfmt.ExitUsage, "access code must be 4-32 letters/digits").
			WithHint("read it on the printer: Settings > LAN Only (it is 8 characters)")
	}
	return nil
}

// Store saves the access code for a printer serial.
func Store(kr Keyring, serial, code string) error {
	if err := ValidateCode(code); err != nil {
		return err
	}
	if err := kr.Set(Service, serial, code); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "store access code in OS keychain").
			WithHint("on headless Linux use BAMBU_ACCESS_CODE instead")
	}
	return nil
}

// Remove deletes the stored access code.
func Remove(kr Keyring, serial string) error {
	if err := kr.Delete(Service, serial); err != nil && !errors.Is(err, ErrNotFound) {
		return errfmt.Wrap(errfmt.ExitConfig, err, "remove access code from OS keychain")
	}
	return nil
}

// Resolve finds the access code for serial: keychain → BAMBU_ACCESS_CODE → Bambu Studio's config (fallback).
func Resolve(kr Keyring, serial string) (code, source string, err error) {
	if kr != nil {
		if c, kerr := kr.Get(Service, serial); kerr == nil && c != "" {
			return c, SourceKeychain, nil
		}
	}
	if c := strings.TrimSpace(os.Getenv("BAMBU_ACCESS_CODE")); c != "" {
		return c, SourceEnv, nil
	}
	if c := studioCode(StudioConfPath(), serial); c != "" {
		return c, SourceBambuStudio, nil
	}
	return "", "", errfmt.New(errfmt.ExitAuth, "no access code for printer %s", serial).
		WithHint("run: bambu auth set <printer>  (code from the printer screen: Settings > LAN Only), or set BAMBU_ACCESS_CODE")
}

// StudioConfPath is where Bambu Studio keeps its app config (which caches LAN access codes).
func StudioConfPath() string {
	if p := os.Getenv("BAMBU_STUDIO_CONF"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "BambuStudio", "BambuStudio.conf")
	case "windows":
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "BambuStudio", "BambuStudio.conf")
		}
	}
	return filepath.Join(home, ".config", "BambuStudio", "BambuStudio.conf")
}

// studioCode reads access_code / user_access_code for serial from BambuStudio.conf. The file is JSON,
// optionally followed by a checksum line.
func studioCode(path, serial string) string {
	b, err := os.ReadFile(path) //nolint:gosec // well-known app config path
	if err != nil {
		return ""
	}
	if i := strings.LastIndexByte(string(b), '}'); i >= 0 {
		b = b[:i+1]
	}
	var conf map[string]json.RawMessage
	if json.Unmarshal(b, &conf) != nil {
		return ""
	}
	for _, key := range []string{"access_code", "user_access_code"} {
		var m map[string]string
		if raw, ok := conf[key]; ok && json.Unmarshal(raw, &m) == nil && m[serial] != "" {
			return m[serial]
		}
	}
	return ""
}
