package service

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	scriptName  = "Send to phone"
	desktopName = "sharing-send.desktop"
)

// NautilusScript is the right-click "Scripts > Send to phone" entry. Nautilus
// passes the selection newline-separated in NAUTILUS_SCRIPT_SELECTED_FILE_PATHS.
func NautilusScript(exe string) string {
	return fmt.Sprintf(`#!/bin/sh
# Installed by sharingd. Right-click files or folders > Scripts > Send to phone.
IFS='
'
set -- $NAUTILUS_SCRIPT_SELECTED_FILE_PATHS
[ $# -gt 0 ] || exit 0
exec %q send "$@"
`, exe)
}

// DesktopEntry adds "Send to phone" to the Open With menu of any file manager.
func DesktopEntry(exe string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Send to phone
Comment=Send to your paired phone with sharing
Exec=%q send %%F
Icon=send-to
Terminal=false
NoDisplay=false
Categories=Utility;
MimeType=inode/directory;application/octet-stream;application/pdf;image/*;video/*;audio/*;text/*;application/zip;
`, exe)
}

func dataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// InstallIntegration writes the file-manager entries for the binary at exe.
func InstallIntegration(exe string) error {
	data, err := dataDir()
	if err != nil {
		return err
	}
	script := filepath.Join(data, "nautilus", "scripts", scriptName)
	desktop := filepath.Join(data, "applications", desktopName)
	for _, f := range []struct {
		path, body string
		mode       os.FileMode
	}{
		{script, NautilusScript(exe), 0o755},
		{desktop, DesktopEntry(exe), 0o644},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, []byte(f.body), f.mode); err != nil {
			return err
		}
	}
	return nil
}

// RemoveIntegration deletes the entries written by InstallIntegration.
func RemoveIntegration() error {
	data, err := dataDir()
	if err != nil {
		return err
	}
	for _, p := range []string{
		filepath.Join(data, "nautilus", "scripts", scriptName),
		filepath.Join(data, "applications", desktopName),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
