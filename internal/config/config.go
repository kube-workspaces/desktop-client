// Package config stores the client's instance profiles and session tokens.
//
// Profiles (which instance, which user) are ordinary config data and live in a
// JSON file under the user's config directory. Session tokens are credentials
// and live in the OS keychain instead, so they are never written to disk in
// plaintext by default.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AppName is the identifier used for the config directory and as the keychain
// service name.
const AppName = "kube-workspaces"

// Profile describes one kube-workspaces instance the user connects to.
type Profile struct {
	// Name uniquely identifies the profile and is the keychain account key.
	Name string `json:"name"`
	// Server is the base URL of the instance, e.g. https://workspaces.example.com.
	Server string `json:"server"`
	// Email is the last account used to log in, remembered for convenience.
	Email string `json:"email,omitempty"`
	// Namespace optionally pins workspace listings to one namespace. Empty
	// means all namespaces the user can see.
	Namespace string `json:"namespace,omitempty"`
	// InsecureSkipVerify disables TLS verification. Intended for dev clusters
	// with self-signed certificates and nothing else.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Settings are the client's own preferences: how the interface looks, in
// contrast to [Profile], which is about an instance. They are stored in the
// same file but are independent of any profile, so a client with no instance
// configured yet still remembers what its user picked.
type Settings struct {
	// Style names the theme's typography: "bubbly" (the default), "retro" or
	// "clean". Empty means the default.
	Style string `json:"style,omitempty"`
	// Mode names the colour scheme: "dark" (the default) or "light". Empty
	// means the default.
	Mode string `json:"mode,omitempty"`
}

// Config is the on-disk configuration document.
type Config struct {
	// Current is the name of the active profile.
	Current string `json:"current,omitempty"`
	// Profiles holds every configured instance, keyed by name.
	Profiles map[string]*Profile `json:"profiles"`
	// Settings is the client's own appearance preferences.
	Settings Settings `json:"settings,omitempty"`

	// path records where this config was loaded from so Save can round-trip.
	path string
}

// Dir returns the directory holding the client's configuration.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, AppName), nil
}

// Path returns the full path of the configuration file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the configuration, returning an empty one if it does not exist yet.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Profiles: map[string]*Profile{}, path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	cfg.path = path
	return cfg, nil
}

// Save writes the configuration back to disk, creating the directory as needed.
// The file is written via a temporary file and renamed so a crash cannot leave
// a truncated config behind.
func (c *Config) Save() error {
	if c.path == "" {
		path, err := Path()
		if err != nil {
			return err
		}
		c.path = path
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Get returns the named profile, or nil.
func (c *Config) Get(name string) *Profile { return c.Profiles[name] }

// Active returns the current profile. It returns an error when no profile is
// configured or the recorded current profile is missing.
func (c *Config) Active() (*Profile, error) {
	if len(c.Profiles) == 0 {
		return nil, errors.New("no profiles configured: run `kube-workspaces login --server <url>` first")
	}
	if c.Current == "" {
		// A single profile is unambiguous, so do not make the user select it.
		if len(c.Profiles) == 1 {
			for _, p := range c.Profiles {
				return p, nil
			}
		}
		return nil, errors.New("no active profile: select one with `kube-workspaces profile use <name>`")
	}
	p, ok := c.Profiles[c.Current]
	if !ok {
		return nil, fmt.Errorf("active profile %q no longer exists", c.Current)
	}
	return p, nil
}

// Put inserts or replaces a profile and makes it current.
func (c *Config) Put(p *Profile) {
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	c.Profiles[p.Name] = p
	c.Current = p.Name
}

// Remove deletes a profile and its stored token.
func (c *Config) Remove(name string) error {
	if _, ok := c.Profiles[name]; !ok {
		return fmt.Errorf("no such profile %q", name)
	}
	delete(c.Profiles, name)
	if c.Current == name {
		c.Current = ""
		if len(c.Profiles) == 1 {
			for n := range c.Profiles {
				c.Current = n
			}
		}
	}
	// A leftover token would be both a security wart and a confusing state.
	_ = DeleteToken(name)
	return nil
}

// Names returns profile names in stable sorted order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ProfileNameForServer derives a default profile name from a server URL: the
// hostname, which is both stable and recognisable.
func ProfileNameForServer(server string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "default"
	}
	return s
}
