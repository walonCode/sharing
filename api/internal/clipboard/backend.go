// Package clipboard syncs the system text clipboard with paired peers.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Backend reads and writes the system clipboard as text.
type Backend interface {
	Read(ctx context.Context) (string, error)
	Write(ctx context.Context, text string) error
}

var ErrNoBackend = errors.New("no clipboard tool found (install wl-clipboard, xclip, or xsel)")

type execBackend struct {
	read, write []string
}

func (b execBackend) Read(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, b.read[0], b.read[1:]...).Output()
	if err != nil {
		// Tools exit non-zero when the clipboard is empty or holds non-text data.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

func (b execBackend) Write(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, b.write[0], b.write[1:]...)
	cmd.Stdin = strings.NewReader(text)
	// wl-copy and xclip fork a background owner that keeps the selection alive.
	// Leaving stdout/stderr nil (/dev/null) stops Run from waiting on pipes the
	// forked owner would hold open forever.
	return cmd.Run()
}

// Detect picks Wayland (wl-clipboard) when a Wayland session is active, then
// xclip, then xsel.
func Detect() (Backend, error) {
	adoptWaylandSocket()
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return execBackend{read: []string{"wl-paste", "--no-newline", "--type", "text/plain"}, write: []string{"wl-copy", "--type", "text/plain"}}, nil
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return execBackend{read: []string{"xclip", "-selection", "clipboard", "-o"}, write: []string{"xclip", "-selection", "clipboard", "-i"}}, nil
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return execBackend{read: []string{"xsel", "--clipboard", "--output"}, write: []string{"xsel", "--clipboard", "--input"}}, nil
	}
	return nil, ErrNoBackend
}

// adoptWaylandSocket sets WAYLAND_DISPLAY (and XDG_RUNTIME_DIR) from the
// runtime dir when started by systemd before the session exported them.
func adoptWaylandSocket() {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socks, _ := filepath.Glob(filepath.Join(dir, "wayland-[0-9]*"))
	for _, sock := range socks {
		if !strings.HasSuffix(sock, ".lock") {
			os.Setenv("XDG_RUNTIME_DIR", dir)
			os.Setenv("WAYLAND_DISPLAY", filepath.Base(sock))
			return
		}
	}
}
