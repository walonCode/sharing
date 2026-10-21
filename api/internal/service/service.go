// Package service installs sharingd as a systemd user unit so it starts at
// login and is restarted if it ever dies.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const UnitName = "sharingd.service"

// UnitFile renders the unit for the binary at exe.
func UnitFile(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=Sharing daemon (file and clipboard sync with your phone)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%q run
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, exe)
}

// UnitPath is ~/.config/systemd/user/sharingd.service.
func UnitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user", UnitName), nil
}

// Install writes the unit for the running binary, then enables and starts it.
func Install() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	path, err := UnitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(UnitFile(exe)), 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", UnitName}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return path, fmt.Errorf("systemctl %v: %w: %s", args, err, out)
		}
	}
	return path, nil
}

// Uninstall stops and removes the unit.
func Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", UnitName).Run()
	path, err := UnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return exec.Command("systemctl", "--user", "daemon-reload").Run()
}
