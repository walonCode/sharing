package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
