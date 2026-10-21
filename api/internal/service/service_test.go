package service

import (
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	u := UnitFile("/opt/my dir/sharingd")
	for _, want := range []string{
		`ExecStart="/opt/my dir/sharingd" run`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}
