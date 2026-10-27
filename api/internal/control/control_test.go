package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/server"
	"api/internal/transfer"
)

func TestInviteRedeemable(t *testing.T) {
	store, _ := config.Open(t.TempDir())
	srv := server.New(store)
	ts := httptest.NewServer(Handler(srv, transfer.Mount(srv), "fp123"))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/invite", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out InviteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil || u.Query().Get("fp") != "fp123" {
		t.Fatalf("url = %q err=%v", out.URL, err)
	}
	if !srv.Pairing.Redeem(u.Query().Get("token")) {
		t.Fatal("invite token should be redeemable")
	}
}

func TestSettingsEndpoint(t *testing.T) {
	store, _ := config.Open(t.TempDir())
	srv := server.New(store)
	ts := httptest.NewServer(Handler(srv, transfer.Mount(srv), "fp"))
	defer ts.Close()

	post := func(body string) int {
		resp, err := http.Post(ts.URL+"/settings", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{"receiveDir":"relative/path"}`); code != http.StatusBadRequest {
		t.Fatalf("relative dir = %d", code)
	}
	if code := post(`{"receiveDir":"/srv/inbox"}`); code != http.StatusOK {
		t.Fatalf("valid = %d", code)
	}
	if got := store.Settings().ReceiveDir; got != "/srv/inbox" {
		t.Fatalf("receiveDir = %q", got)
	}
}

func TestUnpairRevokesAccess(t *testing.T) {
	store, _ := config.Open(t.TempDir())
	srv := server.New(store)
	_ = store.AddPeer(config.Peer{ID: "p1", Name: "pixel", TokenHash: "h"})
	ts := httptest.NewServer(Handler(srv, transfer.Mount(srv), "fp"))
	defer ts.Close()

	del := func(id string) int {
		req, _ := http.NewRequest("DELETE", ts.URL+"/peers/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := del("nope"); code != http.StatusNotFound {
		t.Fatalf("unknown peer = %d", code)
	}
	if code := del("pixel"); code != http.StatusNoContent { // by name
		t.Fatalf("unpair = %d", code)
	}
	if len(store.Peers()) != 0 {
		t.Fatal("peer still stored")
	}
}

func TestListenRefusesLiveSocketAndReplacesStaleOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	first, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() { // accept so dials succeed
		for {
			c, err := first.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen = %v, want ErrAlreadyRunning", err)
	}
	// The first daemon's socket must still be intact.
	if c, err := net.Dial("unix", path); err != nil {
		t.Fatalf("live socket was disturbed: %v", err)
	} else {
		c.Close()
	}

	// A dead daemon leaves a stale file; the next one replaces it.
	first.Close()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Listen(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	second.Close()
}

func TestSocketPathNeverUsesTmp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "/home/someone")
	p := SocketPath()
	if strings.HasPrefix(p, os.TempDir()) || p != "/home/someone/.local/state/sharing/sharingd.sock" {
		t.Fatalf("fallback path = %q", p)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if p := SocketPath(); p != "/run/user/1000/sharing/sharingd.sock" {
		t.Fatalf("runtime path = %q", p)
	}
}

func TestListenCreatesPrivateDirAndSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "sharing", "s.sock")
	old := syscall.Umask(0) // a permissive umask must not leak into the socket
	defer syscall.Umask(old)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", st.Mode().Perm())
	}
	if now := syscall.Umask(0); now != 0 {
		t.Fatalf("umask not restored: %o", now)
	}
}

func TestListenTightensLooseDirAndRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(filepath.Join(loose, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if st, _ := os.Stat(loose); st.Mode().Perm() != 0o700 {
		t.Fatalf("loose dir not tightened: %v", st.Mode().Perm())
	}

	target := filepath.Join(base, "target")
	_ = os.Mkdir(target, 0o700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(link, "s.sock")); err == nil {
		t.Fatal("a symlinked socket directory must be refused")
	}
}

func apiServer(t *testing.T) (*server.Server, *transfer.Service, *httptest.Server) {
	t.Helper()
	store, _ := config.Open(t.TempDir())
	srv := server.New(store)
	svc := transfer.Mount(srv)
	ts := httptest.NewServer(Handler(srv, svc, "fp"))
	t.Cleanup(ts.Close)
	return srv, svc, ts
}

func TestInviteRequiresConfirmationByDefault(t *testing.T) {
	_, _, ts := apiServer(t)
	post := func(body string) InviteResponse {
		resp, err := http.Post(ts.URL+"/invite", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out InviteResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if !post("").Confirm || !post(`{"yes":false}`).Confirm {
		t.Fatal("confirmation must be on by default")
	}
	if post(`{"yes":true}`).Confirm {
		t.Fatal("--yes must skip confirmation")
	}
}

func TestPairingDecisionEndpoints(t *testing.T) {
	srv, _, ts := apiServer(t)
	resp, _ := http.Get(ts.URL + "/pairing/pending?wait=0")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("nothing pending = %d", resp.StatusCode)
	}
	resp.Body.Close()
	res := make(chan bool, 1)
	go func() { res <- srv.Pairing.Ask(context.Background(), "device-0001", "Pixel") }()
	var pr struct{ ID, Name string }
	for range 200 {
		r, err := http.Get(ts.URL + "/pairing/pending?wait=1")
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode == http.StatusOK {
			_ = json.NewDecoder(r.Body).Decode(&pr)
			r.Body.Close()
			break
		}
		r.Body.Close()
	}
	if pr.ID == "" || pr.Name != "Pixel" {
		t.Fatalf("pending = %+v", pr)
	}
	r, _ := http.Post(ts.URL+"/pairing/"+pr.ID, "application/json", strings.NewReader(`{"accept":true}`))
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent || !<-res {
		t.Fatalf("decide = %d", r.StatusCode)
	}
	r, _ = http.Post(ts.URL+"/pairing/nope", "application/json", strings.NewReader(`{"accept":true}`))
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown request = %d", r.StatusCode)
	}
}

func TestRotateNeedsOnlinePeerAndKeepsOldTokenBriefly(t *testing.T) {
	srv, _, ts := apiServer(t)
	_ = srv.Store.AddPeer(config.Peer{ID: "device-0001", Name: "pixel", TokenHash: "h", PairedAt: time.Now()})
	r, _ := http.Post(ts.URL+"/peers/pixel/rotate", "application/json", nil)
	r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("offline rotate = %d, want 409", r.StatusCode)
	}
	r, _ = http.Post(ts.URL+"/peers/nobody/rotate", "application/json", nil)
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown peer = %d", r.StatusCode)
	}
}

func TestPeersListShowsLastSeen(t *testing.T) {
	srv, _, ts := apiServer(t)
	seen := time.Now().Add(-time.Hour).Truncate(time.Second)
	_ = srv.Store.AddPeer(config.Peer{ID: "device-0001", Name: "pixel", TokenHash: "h", LastSeen: seen})
	r, err := http.Get(ts.URL + "/peers")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var list []PeerInfo
	_ = json.NewDecoder(r.Body).Decode(&list)
	if len(list) != 1 || !list[0].LastSeen.Equal(seen) || list[0].Online {
		t.Fatalf("peers = %+v", list)
	}
}

func TestSettingsRejectsHostileName(t *testing.T) {
	_, _, ts := apiServer(t)
	r, _ := http.Post(ts.URL+"/settings", "application/json", strings.NewReader(`{"name":"bad\u001b[2Jname"}`))
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("control-character name = %d", r.StatusCode)
	}
}
