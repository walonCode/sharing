package server_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"api/internal/protocol"
	"api/internal/server"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// expectClosed fails unless the server closes conn within d.
func expectClosed(t *testing.T, conn net.Conn, d time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, err := io.Copy(io.Discard, conn)
	var ne net.Error
	if err != nil && errorsAsTimeout(err, &ne) {
		t.Fatal("server kept the connection open")
	}
}

func errorsAsTimeout(err error, ne *net.Error) bool {
	if e, ok := err.(net.Error); ok {
		*ne = e
		return e.Timeout()
	}
	return false
}

func shortTimeouts(t *testing.T) {
	t.Helper()
	oh, oi := server.HeaderTimeout, server.IdleTimeout
	server.HeaderTimeout, server.IdleTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { server.HeaderTimeout, server.IdleTimeout = oh, oi })
}

func realConfigServer(t *testing.T, s *server.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config = server.NewHTTPServer("", s.Handler())
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func TestSlowlorisHeadersAreCut(t *testing.T) {
	shortTimeouts(t)
	s, _ := newServer(t)
	ts := realConfigServer(t, s)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A request that never finishes its headers.
	_, _ = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nX-Slow: "))
	expectClosed(t, conn, 3*time.Second)
}

func TestSilentConnectionIsCut(t *testing.T) {
	shortTimeouts(t)
	s, _ := newServer(t)
	ts := realConfigServer(t, s)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	expectClosed(t, conn, 3*time.Second) // never sends a byte (net/http applies the same limit to a stalled TLS handshake)
}

func TestKeepAliveConnectionOutlivesTheHeaderTimeout(t *testing.T) {
	shortTimeouts(t)
	s, _ := newServer(t)
	ts := realConfigServer(t, s)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /nothing HTTP/1.1\r\nHost: x\r\n\r\n"))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(500 * time.Millisecond) // longer than the header timeout
	_, _ = conn.Write([]byte("GET /nothing HTTP/1.1\r\nHost: x\r\n\r\n"))
	if resp, err = http.ReadResponse(br, nil); err != nil {
		t.Fatalf("second request on a live connection failed: %v", err)
	}
	resp.Body.Close()
}

func TestStalledUploadIsCutButSlowSteadyIsNot(t *testing.T) {
	shortTimeouts(t)
	s, ts := newServer(t)
	_, p := pair(t, ts, s.Pairing.NewInvite(time.Minute), "device-slow")
	var mu sync.Mutex
	var bodyErr error
	var got int
	done := make(chan struct{}, 2)
	s.Handle("POST /sink", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		mu.Lock()
		bodyErr, got = err, int(n)
		mu.Unlock()
		done <- struct{}{}
	}))

	post := func(feed func(pw *io.PipeWriter)) {
		pr, pw := io.Pipe()
		req, _ := http.NewRequest("POST", ts.URL+"/sink", pr)
		req.Header.Set("Authorization", "Bearer "+p.PeerToken)
		go feed(pw)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}

	// Steady: a byte every 100ms for 1s is far longer than the 300ms idle timeout in total.
	post(func(pw *io.PipeWriter) {
		for range 10 {
			_, _ = pw.Write([]byte("x"))
			time.Sleep(100 * time.Millisecond)
		}
		pw.Close()
	})
	<-done
	mu.Lock()
	if bodyErr != nil || got != 10 {
		t.Fatalf("steady upload: n=%d err=%v", got, bodyErr)
	}
	mu.Unlock()

	// Stalled: one byte, then silence.
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	go post(func(pw *io.PipeWriter) { _, _ = pw.Write([]byte("x")); <-stall; pw.Close() })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stalled upload was never cut")
	}
	mu.Lock()
	defer mu.Unlock()
	if bodyErr == nil {
		t.Fatal("expected a read error on a stalled body")
	}
}

func TestAuthFailuresAreThrottled(t *testing.T) {
	s, ts := newServer(t)
	_, p := pair(t, ts, s.Pairing.NewInvite(time.Minute), "device-ok1")
	get := func(token string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/ws", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 10; i++ {
		if code := get("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d", i, code)
		}
	}
	if code := get("wrong"); code != http.StatusTooManyRequests {
		t.Fatalf("after 10 failures = %d, want 429", code)
	}
	// While throttled even a valid token from that address waits (the address is the unit).
	if code := get(p.PeerToken); code != http.StatusTooManyRequests {
		t.Fatalf("valid token while throttled = %d", code)
	}
}

func TestBadPairRequestsAreRejected(t *testing.T) {
	s, ts := newServer(t)
	for name, req := range map[string]protocol.PairRequest{
		"short id":      {DeviceID: "abc", Name: "Phone"},
		"id with space": {DeviceID: "device 0001", Name: "Phone"},
		"empty name":    {DeviceID: "device-0001", Name: "   "},
		"escape name":   {DeviceID: "device-0001", Name: "Pixel\x1b[2J"},
		"long name":     {DeviceID: "device-0001", Name: strings.Repeat("n", 65)},
	} {
		req.Token = s.Pairing.NewInvite(time.Minute)
		resp, _ := pairReq(t, ts, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
	if len(s.Store.Peers()) != 0 {
		t.Fatal("no peer should have been stored")
	}
}

func TestPairNameIsTrimmed(t *testing.T) {
	s, ts := newServer(t)
	resp, _ := pairReq(t, ts, protocol.PairRequest{Token: s.Pairing.NewInvite(time.Minute), DeviceID: "device-0002", Name: "  Pixel 8  "})
	if resp.StatusCode != http.StatusOK || s.Store.Peers()[0].Name != "Pixel 8" {
		t.Fatalf("status=%d peers=%+v", resp.StatusCode, s.Store.Peers())
	}
}

func wsDial(t *testing.T, ts *httptest.Server, token string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, wsURL(ts), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(protocol.MaxMessageBytes)
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx
}

func TestClipboardSizeBoundaryKeepsConnectionOpen(t *testing.T) {
	s, ts := newServer(t)
	_, p := pair(t, ts, s.Pairing.NewInvite(time.Minute), "device-clip")
	lens := make(chan int, 4)
	s.OnMessage = func(_ string, m protocol.Message) { lens <- len(m.Data) }
	c, ctx := wsDial(t, ts, p.PeerToken)
	for _, n := range []int{protocol.MaxClipBytes, protocol.MaxClipBytes + 1, 5} {
		if err := wsjson.Write(ctx, c, protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: strings.Repeat("a", n)}); err != nil {
			t.Fatalf("write %d bytes: %v", n, err)
		}
		select {
		case got := <-lens:
			if got != n {
				t.Fatalf("handler saw %d bytes, want %d", got, n)
			}
		case <-ctx.Done():
			t.Fatalf("clip of %d bytes never reached the handler (connection closed?)", n)
		}
	}
	// Unknown types are ignored, not fatal.
	if err := wsjson.Write(ctx, c, protocol.Message{Type: "from-the-future"}); err != nil {
		t.Fatal(err)
	}
	<-lens
}

func TestLastSeenAndRotation(t *testing.T) {
	s, ts := newServer(t)
	_, p := pair(t, ts, s.Pairing.NewInvite(time.Minute), "device-rot1")
	if !s.Store.Peers()[0].LastSeen.IsZero() {
		t.Fatal("lastSeen should start empty")
	}
	c, ctx := wsDial(t, ts, p.PeerToken)
	var hello protocol.Message
	if err := wsjson.Read(ctx, c, &hello); err != nil || hello.Proto != protocol.Version {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	if s.Store.Peers()[0].LastSeen.IsZero() {
		t.Fatal("lastSeen not recorded")
	}

	newTok := "new-token-value"
	if err := s.Store.RotatePeer("device-rot1", hashOf(newTok), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Both tokens must match during the grace period, only the new one afterwards.
	peer := s.Store.Peers()[0]
	now := time.Now()
	if !peer.Matches(hashOf(newTok), now) || !peer.Matches(hashOf(p.PeerToken), now) {
		t.Fatal("both tokens must work during the grace period")
	}
	if peer.Matches(hashOf(p.PeerToken), now.Add(61*time.Second)) {
		t.Fatal("old token must stop working after 60s")
	}
	if !peer.Matches(hashOf(newTok), now.Add(time.Hour)) {
		t.Fatal("new token must keep working")
	}
}

func waitPending(t *testing.T, s *server.Server) string {
	t.Helper()
	for range 400 {
		if p := s.Pairing.Pending(); len(p) > 0 {
			return p[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending pairing request")
	return ""
}

func TestPairingNeedsConfirmationWhenInviteRequiresIt(t *testing.T) {
	for _, accept := range []bool{true, false} {
		s, ts := newServer(t)
		tok := s.Pairing.NewInviteConfirm(time.Minute, true)
		type result struct {
			resp *http.Response
			out  protocol.PairResponse
		}
		res := make(chan result, 1)
		go func() {
			r, o := pairReq(t, ts, protocol.PairRequest{Token: tok, DeviceID: "device-0003", Name: "Pixel"})
			res <- result{r, o}
		}()
		s.Pairing.Decide(waitPending(t, s), accept)
		got := <-res
		if accept && (got.resp.StatusCode != 200 || got.out.PeerToken == "") {
			t.Fatalf("accepted: %d", got.resp.StatusCode)
		}
		if !accept && (got.resp.StatusCode != http.StatusForbidden || len(s.Store.Peers()) != 0) {
			t.Fatalf("rejected: %d peers=%d", got.resp.StatusCode, len(s.Store.Peers()))
		}
	}
}

func TestLostPairResponseCanBeRepeated(t *testing.T) {
	s, ts := newServer(t)
	tok := s.Pairing.NewInvite(time.Minute)
	req := protocol.PairRequest{Token: tok, DeviceID: "device-0004", Name: "Pixel"}
	r1, first := pairReq(t, ts, req)
	r2, second := pairReq(t, ts, req) // the phone never saw the first answer
	if r1.StatusCode != 200 || r2.StatusCode != 200 || first.PeerToken == second.PeerToken {
		t.Fatalf("statuses %d %d, tokens equal=%v", r1.StatusCode, r2.StatusCode, first.PeerToken == second.PeerToken)
	}
	if peer := s.Store.Peers()[0]; peer.TokenHash != hashOf(second.PeerToken) {
		t.Fatal("the fresh token must replace the first")
	}
	other := req
	other.DeviceID = "device-0005"
	if r3, _ := pairReq(t, ts, other); r3.StatusCode != http.StatusForbidden {
		t.Fatalf("a different device reusing the invite = %d", r3.StatusCode)
	}
}
