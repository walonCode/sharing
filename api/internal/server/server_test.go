package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/protocol"
	"api/internal/server"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func newServer(t *testing.T) (*server.Server, *httptest.Server) {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(store)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func pair(t *testing.T, ts *httptest.Server, token, id string) (*http.Response, protocol.PairResponse) {
	t.Helper()
	body, _ := json.Marshal(protocol.PairRequest{Token: token, DeviceID: id, Name: "phone"})
	resp, err := http.Post(ts.URL+"/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var out protocol.PairResponse
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp, out
}

func TestPairWithValidInvite(t *testing.T) {
	s, ts := newServer(t)
	resp, out := pair(t, ts, s.Pairing.NewInvite(time.Minute), "phone-1")
	if resp.StatusCode != http.StatusOK || out.PeerToken == "" {
		t.Fatalf("status=%d token=%q", resp.StatusCode, out.PeerToken)
	}
	if got := s.Store.Peers(); len(got) != 1 || got[0].ID != "phone-1" {
		t.Fatalf("peers = %+v", got)
	}
	if s.Store.Peers()[0].TokenHash == out.PeerToken {
		t.Fatal("raw token must not be stored")
	}
}

func TestInviteIsSingleUse(t *testing.T) {
	s, ts := newServer(t)
	token := s.Pairing.NewInvite(time.Minute)
	if resp, _ := pair(t, ts, token, "a"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first pair: %d", resp.StatusCode)
	}
	if resp, _ := pair(t, ts, token, "b"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reuse should be forbidden, got %d", resp.StatusCode)
	}
}

func TestExpiredAndBogusInviteRejected(t *testing.T) {
	s, ts := newServer(t)
	expired := s.Pairing.NewInvite(-time.Second)
	for _, tok := range []string{expired, "bogus", ""} {
		if resp, _ := pair(t, ts, tok, "a"); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("token %q: got %d", tok, resp.StatusCode)
		}
	}
	if len(s.Store.Peers()) != 0 {
		t.Fatal("no peer should be stored")
	}
}

func wsURL(ts *httptest.Server) string { return "ws" + ts.URL[len("http"):] + "/ws" }

func TestWebSocketRequiresAuth(t *testing.T) {
	_, ts := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(ts), nil)
	if err == nil {
		t.Fatal("expected dial to fail without a token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resp = %+v", resp)
	}
}

func dialPeer(t *testing.T, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL(ts), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func TestHubDeliversToCorrectPeer(t *testing.T) {
	s, ts := newServer(t)
	_, a := pair(t, ts, s.Pairing.NewInvite(time.Minute), "a")
	_, b := pair(t, ts, s.Pairing.NewInvite(time.Minute), "b")
	ca, cb := dialPeer(t, ts, a.PeerToken), dialPeer(t, ts, b.PeerToken)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, c := range []*websocket.Conn{ca, cb} {
		var hello protocol.Message
		if err := wsjson.Read(ctx, c, &hello); err != nil || hello.Type != protocol.TypeHello {
			t.Fatalf("hello: %v %+v", err, hello)
		}
	}

	if !s.Hub.Send("b", protocol.Message{Type: protocol.TypeOffer, ID: "x1", Kind: "file"}) {
		t.Fatal("send to online peer failed")
	}
	var got protocol.Message
	if err := wsjson.Read(ctx, cb, &got); err != nil || got.ID != "x1" {
		t.Fatalf("b got %v %+v", err, got)
	}
	if s.Hub.Send("nobody", protocol.Message{Type: protocol.TypeOffer}) {
		t.Fatal("send to unknown peer should fail")
	}
}

func TestIncomingMessagesReachHandler(t *testing.T) {
	s, ts := newServer(t)
	_, a := pair(t, ts, s.Pairing.NewInvite(time.Minute), "a")
	got := make(chan protocol.Message, 1)
	s.OnMessage = func(peerID string, m protocol.Message) {
		if peerID == "a" {
			got <- m
		}
	}
	c := dialPeer(t, ts, a.PeerToken)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, protocol.Message{Type: protocol.TypeClip, Data: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Data != "hi" {
			t.Fatalf("m = %+v", m)
		}
	case <-ctx.Done():
		t.Fatal("handler not called")
	}
}
