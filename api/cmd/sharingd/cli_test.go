package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/control"
	"api/internal/server"
	"api/internal/transfer"
)

// useDaemon points the CLI helpers at an in-process control API.
func useDaemon(t *testing.T) (*server.Server, *transfer.Service) {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(store)
	svc := transfer.Mount(srv)
	ts := httptest.NewServer(control.Handler(srv, svc, "fp"))
	t.Cleanup(ts.Close)
	old := daemonClient
	daemonClient = func() *http.Client {
		return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
			},
		}}
	}
	t.Cleanup(func() { daemonClient = old })
	return srv, svc
}

func TestCallDecodesAndReportsDaemonErrors(t *testing.T) {
	useDaemon(t)
	var peers []control.PeerInfo
	if err := call(http.MethodGet, "/peers", nil, &peers); err != nil || len(peers) != 0 {
		t.Fatalf("peers: %v %v", peers, err)
	}
	err := call(http.MethodDelete, "/peers/nobody", nil, nil)
	if err == nil || !strings.Contains(err.Error(), `no paired peer "nobody"`) {
		t.Fatalf("daemon message not surfaced: %v", err)
	}
	if err := call(http.MethodPost, "/send", control.SendRequest{}, nil); err == nil || !strings.Contains(err.Error(), "path required") {
		t.Fatalf("bad request: %v", err)
	}
}

func TestCallExplainsMissingDaemon(t *testing.T) {
	old := daemonClient
	daemonClient = func() *http.Client {
		return &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: errRefused{}}
		}}}
	}
	defer func() { daemonClient = old }()
	if err := call(http.MethodGet, "/peers", nil, nil); err == nil || !strings.Contains(err.Error(), "is the daemon running?") {
		t.Fatalf("err = %v", err)
	}
}

type errRefused struct{}

func (errRefused) Error() string { return "connection refused" }

func TestParseArgsAllowsFlagsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"--to", "pixel", "a.txt", "b.txt"},
		{"a.txt", "--to", "pixel", "b.txt"},
		{"a.txt", "b.txt", "--to=pixel"},
	} {
		fs := newFlags("send")
		to := fs.String("to", "", "")
		paths, err := parseArgs(fs, args)
		if err != nil || *to != "pixel" || strings.Join(paths, ",") != "a.txt,b.txt" {
			t.Errorf("%v -> to=%q paths=%v err=%v", args, *to, paths, err)
		}
	}
	fs := newFlags("send")
	fs.Bool("wait", false, "")
	if _, err := parseArgs(fs, []string{"--bogus"}); err == nil {
		t.Fatal("unknown flag must error")
	}
}

func TestPrintableStripsTerminalEscapes(t *testing.T) {
	if got := printable("Pixel\x1b[2J\r\n8"); strings.ContainsAny(got, "\x1b\r\n") {
		t.Fatalf("control characters survived: %q", got)
	}
	if printable("Pixel 8 é") != "Pixel 8 é" {
		t.Fatal("ordinary text must be unchanged")
	}
}

func TestHumanBytesAndAgo(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
	if ago(time.Time{}) != "never seen" || ago(time.Now()) != "seen just now" || ago(time.Now().Add(-5*time.Minute)) != "seen 5m ago" ||
		ago(time.Now().Add(-3*time.Hour)) != "seen 3h ago" || ago(time.Now().Add(-72*time.Hour)) != "seen 3d ago" {
		t.Fatal("ago renders wrongly")
	}
}

func TestConfirmPairingAcceptsAndRejects(t *testing.T) {
	for answer, wantAccept := range map[string]bool{"y\n": true, "Yes\n": true, "n\n": false, "\n": false, "": false} {
		srv, _ := useDaemon(t)
		res := make(chan bool, 1)
		go func() { res <- srv.Pairing.Ask(context.Background(), "device-0001", "Pixel\x1b[0m 8") }()
		var out bytes.Buffer
		if err := confirmPairing(bufio.NewReader(strings.NewReader(answer)), &out, 10*time.Second); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if got := <-res; got != wantAccept {
			t.Errorf("answer %q accepted=%v, want %v", answer, got, wantAccept)
		}
		if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "wants to pair") {
			t.Errorf("prompt = %q", out.String())
		}
	}
}

func TestTransfersAndActionsThroughTheDaemon(t *testing.T) {
	_, svc := useDaemon(t)
	var list []transfer.Info
	if err := call(http.MethodGet, "/transfers", nil, &list); err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	if err := transferAction("cancel", []string{"deadbeef"}); err == nil {
		t.Fatal("unknown transfer must error")
	}
	if err := transferAction("cancel", nil); err == nil {
		t.Fatal("usage error expected")
	}
	_ = svc
}

func TestCheckPortComparesCertificates(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	want := config.FingerprintDER(ts.Certificate().Raw)
	if c := checkPort(port, want); c.status != "ok" {
		t.Fatalf("matching cert: %v", c)
	}
	if c := checkPort(port, "deadbeef"); c.status != "FAIL" || !strings.Contains(c.detail, "different certificate") {
		t.Fatalf("other cert: %v", c)
	}
	ts.Close()
	if c := checkPort(port, want); c.status != "FAIL" {
		t.Fatalf("closed port: %v", c)
	}
}

func TestCheckDiskWarnsBelowReserve(t *testing.T) {
	set := config.Settings{ReceiveDir: t.TempDir(), MinFreeBytes: 1 << 62}
	if c := checkDisk(set); c.status != "warn" {
		t.Fatalf("%v", c)
	}
	set.MinFreeBytes = 0
	if c := checkDisk(set); c.status != "ok" {
		t.Fatalf("%v", c)
	}
}
