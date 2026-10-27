package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"api/internal/control"
	"api/internal/retry"
	"api/internal/service"
	"api/internal/transfer"

	"github.com/skip2/go-qrcode"
)

// daemonClient is replaced in tests to point at a test server.
var daemonClient = func() *http.Client { return control.Client(control.SocketPath()) }

// call performs one request against the daemon's control socket, sending in
// as JSON (when non-nil) and decoding the reply into out (when non-nil). A
// refused connection becomes "is the daemon running?"; non-2xx replies become
// errors carrying the daemon's message; 429/503 are retried briefly.
func call(method, path string, in, out any) error {
	return callWith(daemonClient(), method, path, in, out)
}

func callWith(client *http.Client, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		payload, _ = json.Marshal(in)
	}
	p := retry.Default()
	p.MaxAttempts = 3
	var (
		status int
		body   []byte
		reqErr error
	)
	err := retry.Do(context.Background(), p, func(int) retry.Outcome {
		req, err := http.NewRequest(method, "http://sharingd"+path, bytes.NewReader(payload))
		if err != nil {
			reqErr = err
			return retry.Outcome{Status: http.StatusBadRequest}
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			reqErr = fmt.Errorf("is the daemon running? %w", err)
			return retry.Outcome{Status: http.StatusBadRequest} // do not retry a missing daemon
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		status, reqErr = resp.StatusCode, nil
		return retry.Outcome{Status: resp.StatusCode, RetryAfter: retry.ParseRetryAfter(resp.Header.Get("Retry-After"))}
	}, nil)
	if reqErr != nil {
		return reqErr
	}
	if err != nil || status < 200 || status > 299 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("daemon answered %d", status)
		}
		return errors.New(msg)
	}
	if out != nil && status != http.StatusNoContent {
		return json.Unmarshal(body, out)
	}
	return nil
}

// printable replaces control characters so a peer-chosen name cannot inject
// terminal escape sequences into CLI output.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// parseArgs lets flags appear before, between or after positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) (positional []string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// pair shows an invite and, unless --yes, asks on the terminal before the phone
// that scans it is accepted.
func pair(args []string) error {
	fs := newFlags("pair")
	yes := fs.Bool("yes", false, "accept the phone without asking")
	if _, err := parseArgs(fs, args); err != nil {
		return fmt.Errorf("usage: sharingd pair [--yes]")
	}
	var inv control.InviteResponse
	if err := call(http.MethodPost, "/invite", control.InviteRequest{Yes: *yes}, &inv); err != nil {
		return err
	}
	fmt.Println("Scan this in the app, or paste the link (valid 5 minutes):")
	if qr, err := qrcode.New(inv.URL, qrcode.Low); err == nil {
		fmt.Println(qr.ToSmallString(false))
	}
	fmt.Println(inv.URL)
	if !inv.Confirm {
		return nil
	}
	return confirmPairing(bufio.NewReader(os.Stdin), os.Stdout, 5*time.Minute)
}

// confirmPairing waits for a phone to ask to pair, then asks the user.
func confirmPairing(in *bufio.Reader, out io.Writer, ttl time.Duration) error {
	fmt.Fprintln(out, "Waiting for the phone to scan (Ctrl-C to cancel)...")
	client := daemonClient()
	client.Timeout = 40 * time.Second
	deadline := time.Now().Add(ttl)
	for time.Now().Before(deadline) {
		var raw json.RawMessage
		if err := callWith(client, http.MethodGet, "/pairing/pending?wait=25", nil, &raw); err != nil {
			return err
		}
		if len(raw) == 0 {
			continue
		}
		var pr struct {
			ID       string `json:"id"`
			DeviceID string `json:"deviceId"`
			Name     string `json:"name"`
		}
		if err := json.Unmarshal(raw, &pr); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s (%s) wants to pair, accept? [y/N] ", printable(pr.Name), printable(pr.DeviceID))
		line, _ := in.ReadString('\n')
		accept := strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
		if err := callWith(client, http.MethodPost, "/pairing/"+url.PathEscape(pr.ID), control.Decision{Accept: accept}, nil); err != nil {
			return err
		}
		if accept {
			fmt.Fprintln(out, "paired", printable(pr.Name))
		} else {
			fmt.Fprintln(out, "rejected", printable(pr.Name))
		}
		return nil
	}
	return errors.New("no phone scanned the invite in time")
}

func peers() error {
	var list []control.PeerInfo
	if err := call(http.MethodGet, "/peers", nil, &list); err != nil {
		return err
	}
	for _, p := range list {
		state := "offline"
		if p.Online {
			state = "online"
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", p.ID, printable(p.Name), state, ago(p.LastSeen))
	}
	return nil
}

// ago renders a last-seen time relative to now.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never seen"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "seen just now"
	case d < time.Hour:
		return fmt.Sprintf("seen %dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("seen %dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("seen %dd ago", int(d.Hours()/24))
}

// send asks the running daemon to offer files or folders to a paired phone.
// The offer is queued when the phone is offline; --wait follows it to the end.
func send(args []string) (err error) {
	fs := newFlags("send")
	to := fs.String("to", "", "peer id or name")
	wait := fs.Bool("wait", false, "wait until the phone has the files and show progress")
	paths, perr := parseArgs(fs, args)
	// Launched from a file manager there is no terminal to show the result in.
	if !isTerminal(os.Stdout) {
		defer func() { notify(len(paths), err) }()
	}
	if perr != nil || len(paths) == 0 {
		return fmt.Errorf("usage: sharingd send [--to <peer>] [--wait] <path>...")
	}
	var ids []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		var resp control.SendResponse
		if err := call(http.MethodPost, "/send", control.SendRequest{Path: abs, Peer: *to}, &resp); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		fmt.Println(resp.State, abs)
		ids = append(ids, resp.ID)
	}
	if *wait {
		for _, id := range ids {
			if err := follow(os.Stdout, id, isTerminal(os.Stdout)); err != nil {
				return err
			}
		}
	}
	return nil
}

// follow polls a transfer until it finishes, printing progress.
func follow(out io.Writer, id string, live bool) error {
	for {
		var t transfer.Info
		if err := call(http.MethodGet, "/transfers/"+url.PathEscape(id), nil, &t); err != nil {
			return err
		}
		switch t.State {
		case transfer.StateDone:
			fmt.Fprintf(out, "\r%s: done%s\n", printable(t.Name), strings.Repeat(" ", 20))
			return nil
		case transfer.StateFailed, transfer.StateCancelled:
			return fmt.Errorf("%s: %s %s", printable(t.Name), t.State, printable(t.Error))
		}
		if live {
			fmt.Fprintf(out, "\r%s: %s %s   ", printable(t.Name), t.State, progress(t))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func progress(t transfer.Info) string {
	if t.Size <= 0 {
		return humanBytes(t.Offset)
	}
	s := fmt.Sprintf("%d%% (%s of %s)", t.Offset*100/t.Size, humanBytes(t.Offset), humanBytes(t.Size))
	if t.BytesPerS > 0 {
		s += " " + humanBytes(int64(t.BytesPerS)) + "/s"
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// transfers lists active and queued transfers in both directions.
func transfers() error {
	var list []transfer.Info
	if err := call(http.MethodGet, "/transfers", nil, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no transfers")
		return nil
	}
	for _, t := range list {
		arrow := "->"
		if t.Direction == "receive" {
			arrow = "<-"
		}
		line := fmt.Sprintf("%.8s\t%s %s\t%s\t%s\t%s", t.ID, arrow, printable(t.Peer), printable(t.Name), t.State, progress(t))
		if t.Attempts > 0 {
			line += fmt.Sprintf("\ttries=%d", t.Attempts)
		}
		if t.Error != "" {
			line += "\t" + printable(t.Error)
		}
		fmt.Println(line)
	}
	return nil
}

// transferAction runs cancel or retry on a transfer id (or unique prefix).
func transferAction(verb string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: sharingd %s <transfer id>", verb)
	}
	if err := call(http.MethodPost, "/transfers/"+url.PathEscape(args[0])+"/"+verb, nil, nil); err != nil {
		return err
	}
	fmt.Println(verb, args[0])
	return nil
}

// setup stores the receive folder (and optionally the device name), then shows
// a pairing QR. It edits the running daemon through the control socket.
func setup(args []string) error {
	fs := newFlags("setup")
	dir := fs.String("dir", "", "folder where received files are saved")
	name := fs.String("name", "", "device name shown on the phone")
	yes := fs.Bool("yes", false, "accept the phone without asking")
	if _, err := parseArgs(fs, args); err != nil {
		return fmt.Errorf("usage: sharingd setup [--dir <folder>] [--name <name>] [--yes]")
	}
	if *dir != "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		*dir = abs
		if err := os.MkdirAll(abs, 0o750); err != nil {
			return err
		}
	}
	if *dir != "" || *name != "" {
		if err := call(http.MethodPost, "/settings", control.SettingsRequest{ReceiveDir: *dir, Name: *name}, nil); err != nil {
			return fmt.Errorf("daemon not running? run `sharingd install` first: %w", err)
		}
		fmt.Println("saved settings (a new name applies to discovery after the daemon restarts)")
	}
	if *yes {
		return pair([]string{"--yes"})
	}
	return pair(nil)
}

// install registers sharingd as a systemd user service that starts at login.
func install() error {
	path, err := service.Install()
	if err != nil {
		return err
	}
	fmt.Println("installed and started", path)
	if err := service.EnableLinger(); err != nil {
		fmt.Println("could not keep it running while logged out; run: loginctl enable-linger", err)
	} else {
		fmt.Println("it will start at boot and keep running after you log out")
	}
	fmt.Println("Next: sharingd setup --dir ~/Downloads/Sharing")
	return nil
}

// unpair revokes a phone: its token stops working and its connection is closed.
func unpair(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: sharingd unpair <id or name>")
	}
	if err := call(http.MethodDelete, "/peers/"+url.PathEscape(args[0]), nil, nil); err != nil {
		return err
	}
	fmt.Println("unpaired", printable(args[0]))
	return nil
}

// rotate replaces a phone's device token; the phone must be online to receive it.
func rotate(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: sharingd rotate <id or name>")
	}
	if err := call(http.MethodPost, "/peers/"+url.PathEscape(args[0])+"/rotate", nil, nil); err != nil {
		return err
	}
	fmt.Println("rotated the token of", printable(args[0]), "(the old one works for 60 seconds)")
	return nil
}
