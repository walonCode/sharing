package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	u := UnitFile("/opt/my dir/sharingd")
	for _, want := range []string{
		`ExecStart="/opt/my dir/sharingd" run`,
		"Restart=always",
		"StartLimitIntervalSec=0",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestIntegrationRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := InstallIntegration("/opt/my dir/sharingd"); err != nil {
		t.Fatal(err)
	}
	data, _ := dataDir()
	script, err := os.ReadFile(filepath.Join(data, "nautilus", "scripts", "Send to phone"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `exec "/opt/my dir/sharingd" send "$@"`) || !strings.Contains(string(script), "NAUTILUS_SCRIPT_SELECTED_FILE_PATHS") {
		t.Fatalf("script:\n%s", script)
	}
	if st, _ := os.Stat(filepath.Join(data, "nautilus", "scripts", "Send to phone")); st.Mode()&0o111 == 0 {
		t.Fatal("script must be executable")
	}
	desktop, _ := os.ReadFile(filepath.Join(data, "applications", "sharing-send.desktop"))
	if !strings.Contains(string(desktop), `Exec="/opt/my dir/sharingd" send %F`) || !strings.Contains(string(desktop), "inode/directory") {
		t.Fatalf("desktop:\n%s", desktop)
	}
	if err := RemoveIntegration(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "applications", "sharing-send.desktop")); !os.IsNotExist(err) {
		t.Fatal("desktop entry not removed")
	}
	if err := RemoveIntegration(); err != nil { // idempotent
		t.Fatal(err)
	}
}
