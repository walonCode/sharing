// Package clipboard syncs the system text clipboard with paired peers.
package clipboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

// Watcher is implemented by backends that can announce clipboard changes. The
// channel closes when the watch ends (tool exited or ctx ended).
type Watcher interface {
	Watch(ctx context.Context) (<-chan struct{}, error)
}

var ErrNoWatch = errors.New("clipboard backend cannot watch for changes")

var ErrNoBackend = errors.New("no clipboard tool found (install wl-clipboard, xclip, or xsel)")

type execBackend struct {
	read, write []string
	// watch, when set, is a long-lived command that prints a line every time
	// the clipboard changes (wl-paste --watch echo). It replaces polling.
	watch []string
}

// Read returns the clipboard text, but never more than MaxBytes+1 bytes: a
// larger value is ignored by the caller, so the rest is not worth reading
// (a 200 MB copy would otherwise be pulled into memory on every poll).
func (b execBackend) Read(ctx context.Context) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.read[0], b.read[1:]...) //nolint:gosec // fixed tool names from Detect
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	data, rerr := io.ReadAll(io.LimitReader(out, MaxBytes+1))
	if len(data) > MaxBytes {
		cancel() // stop the tool instead of draining the rest
	}
	werr := cmd.Wait()
	if rerr != nil {
		return "", rerr
	}
	if len(data) > MaxBytes {
		return string(data), nil
	}
	if werr != nil {
		// Tools exit non-zero when the clipboard is empty or holds non-text data.
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return "", nil
		}
		return "", werr
	}
	return string(data), nil
}

// Watch implements Watcher when the tool can report changes itself.
func (b execBackend) Watch(ctx context.Context) (<-chan struct{}, error) {
	if len(b.watch) == 0 {
		return nil, ErrNoWatch
	}
	cmd := exec.CommandContext(ctx, b.watch[0], b.watch[1:]...) //nolint:gosec // fixed tool names from Detect
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	events := make(chan struct{}, 1)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			select {
			case events <- struct{}{}: // coalesce bursts into one event
			default:
			}
		}
		_ = cmd.Wait()
	}()
	return events, nil
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
			return execBackend{
				read:  []string{"wl-paste", "--no-newline", "--type", "text/plain"},
				write: []string{"wl-copy", "--type", "text/plain"},
				// One long-lived process that prints a line per change; the
				// command it runs (echo) ignores the clipboard on its stdin.
				watch: []string{"wl-paste", "--type", "text/plain", "--watch", "echo"},
			}, nil
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
			_ = os.Setenv("XDG_RUNTIME_DIR", dir)
			_ = os.Setenv("WAYLAND_DISPLAY", filepath.Base(sock))
			return
		}
	}
}

// Describe names the backend and how it learns about changes, for `sharingd doctor`.
func Describe(b Backend) string {
	eb, ok := b.(execBackend)
	if !ok || len(eb.read) == 0 {
		return "custom backend"
	}
	if len(eb.watch) > 0 {
		return eb.read[0] + " (event driven)"
	}
	return eb.read[0] + " (polling, 400 ms while active, 2 s when quiet)"
}
