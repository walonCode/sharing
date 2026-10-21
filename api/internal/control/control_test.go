package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
