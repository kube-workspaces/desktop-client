// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const desktopName = "kube-workspaces.desktop"

func autostartPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
	}
	return filepath.Join(dir, "autostart", desktopName), nil
}

func (Native) Status() (Status, error) {
	if os.Getenv("FLATPAK_ID") != "" {
		return flatpakStatus()
	}
	path, err := autostartPath()
	if err != nil {
		return Status{}, err
	}
	return desktopStatus(path)
}

func desktopStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	// Hidden=true is the standard XDG disable switch; GNOME also writes its
	// own enable key. Interpret only the Desktop Entry group.
	entry, enabled := false, true
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			entry = line == "[Desktop Entry]"
		}
		if entry && (line == "Hidden=true" || line == "X-GNOME-Autostart-enabled=false") {
			enabled = false
		}
	}
	return Status{Enabled: enabled}, nil
}

func (n Native) SetEnabled(enabled bool) (Status, error) {
	if os.Getenv("FLATPAK_ID") != "" {
		return setFlatpakEnabled(enabled)
	}
	path, err := autostartPath()
	if err != nil {
		return Status{}, err
	}
	if !enabled {
		// A Hidden entry also masks a system-wide entry of the same name.
		err = writeDesktop(path, "[Desktop Entry]\nType=Application\nName=Kube Workspaces\nHidden=true\n")
	} else {
		var exe string
		exe, err = startupExecutable()
		if err == nil {
			err = writeDesktop(path, desktopEntry(exe))
		}
	}
	if err != nil {
		return Status{}, err
	}
	return n.Status()
}

func startupExecutable() (string, error) {
	// The executable inside an AppImage is mounted under a temporary path.
	// Register the persistent outer image instead of that disappearing mount.
	if image := os.Getenv("APPIMAGE"); image != "" {
		if !filepath.IsAbs(image) {
			return "", errors.New("APPIMAGE must be an absolute path")
		}
		return image, nil
	}
	return os.Executable()
}

// Exec has two escaping layers: the desktop-entry string and the argument
// grammar. Percent is a field-code introducer even inside a quoted argument.
func desktopEntry(exe string) string {
	exe = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(exe)
	exec := "\"" + exe + "\" shell"
	exec = strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(exec)
	return fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Kube Workspaces\nComment=Access your Kube Workspaces\nExec=%s\nIcon=kube-workspaces\nTerminal=false\n", exec)
}

func writeDesktop(path, data string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kube-workspaces-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.WriteString(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
