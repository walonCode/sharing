package control

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
