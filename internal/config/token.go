package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// ErrNoToken is returned when no token is stored for a profile.
var ErrNoToken = errors.New("no stored session token")

// Token storage policy:
//
// Session tokens are bearer credentials — anyone holding one can act as the
// user until it expires. They therefore go in the OS keychain (Keychain on
// macOS, Credential Manager on Windows, Secret Service on Linux).
//
// Headless Linux boxes and CI runners frequently have no Secret Service. Rather
// than fail, we fall back to a 0600 file inside the config directory, but only
// when the fallback has been explicitly enabled, and we make the degraded
// storage visible to the caller so the UI can warn. Silently writing a
// credential to disk when the user expects a keychain would be the wrong
// default.

// FallbackEnvVar, when set to a true-ish value, permits the plaintext file
// fallback used on systems without a working keychain.
const FallbackEnvVar = "KUBE_WORKSPACES_INSECURE_TOKEN_FILE"

// TokenStorage describes where a token ended up.
type TokenStorage int

const (
	// StorageKeychain means the OS keychain was used.
	StorageKeychain TokenStorage = iota
	// StorageFile means the insecure file fallback was used.
	StorageFile
)

// String implements fmt.Stringer.
func (s TokenStorage) String() string {
	if s == StorageFile {
		return "file (insecure fallback)"
	}
	return "OS keychain"
}

// SaveToken stores the session token for a profile, returning where it was
// stored so callers can warn about the insecure fallback.
func SaveToken(profile, token string) (TokenStorage, error) {
	err := keyring.Set(AppName, profile, token)
	if err == nil {
		return StorageKeychain, nil
	}
	if !fallbackEnabled() {
		return StorageKeychain, fmt.Errorf(
			"store token in keychain: %w (no keychain available? set %s=1 to fall back to a 0600 file)",
			err, FallbackEnvVar)
	}
	path, perr := tokenFilePath(profile)
	if perr != nil {
		return StorageFile, perr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return StorageFile, fmt.Errorf("create token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return StorageFile, fmt.Errorf("write token file: %w", err)
	}
	return StorageFile, nil
}

// LoadToken retrieves the session token for a profile.
func LoadToken(profile string) (string, error) {
	token, err := keyring.Get(AppName, profile)
	if err == nil && token != "" {
		return token, nil
	}
	keychainErr := err

	path, perr := tokenFilePath(profile)
	if perr == nil {
		if data, ferr := os.ReadFile(path); ferr == nil && len(data) > 0 {
			return string(data), nil
		}
	}
	if errors.Is(keychainErr, keyring.ErrNotFound) || keychainErr == nil {
		return "", ErrNoToken
	}
	return "", fmt.Errorf("%w (keychain: %v)", ErrNoToken, keychainErr)
}

// DeleteToken removes a stored token from both the keychain and the fallback
// file. Missing entries are not an error.
func DeleteToken(profile string) error {
	var firstErr error
	if err := keyring.Delete(AppName, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		firstErr = err
	}
	if path, err := tokenFilePath(profile); err == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func fallbackEnabled() bool {
	v := os.Getenv(FallbackEnvVar)
	return v != "" && v != "0" && v != "false"
}

func tokenFilePath(profile string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	// Profile names come from hostnames, but be defensive: a name containing a
	// path separator must not escape the config directory.
	safe := filepath.Base(profile)
	if safe == "." || safe == string(filepath.Separator) {
		return "", fmt.Errorf("invalid profile name %q", profile)
	}
	return filepath.Join(dir, "tokens", safe+".token"), nil
}
