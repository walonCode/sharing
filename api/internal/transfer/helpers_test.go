package transfer

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/pairing"
	"api/internal/protocol"
	"api/internal/server"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// doH is e.do with extra request headers; the response body is read and closed.
func (e *env) doH(t *testing.T, method, path string, body io.Reader, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	return e.doAs(t, e.token, method, path, body, hdr)
}

func (e *env) doAs(t *testing.T, token, method, path string, body io.Reader, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// otherPeer pairs a second device and returns its token.
func (e *env) otherPeer(t *testing.T) (id, token string) {
	t.Helper()
	token = pairing.NewToken()
	id = "other-device-1"
	if err := e.srv.Store.AddPeer(config.Peer{ID: id, Name: "Tablet", TokenHash: pairing.HashToken(token), PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return id, token
}

func (e *env) settings(t *testing.T, fn func(*config.Settings)) {
	t.Helper()
	if err := e.srv.Store.UpdateSettings(fn); err != nil {
		t.Fatal(err)
	}
}

// connect opens the phone's websocket and consumes hello.
func (e *env) connect(t *testing.T) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + e.token}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	var hello protocol.Message
	if err := wsjson.Read(ctx, ws, &hello); err != nil || hello.Type != protocol.TypeHello {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	return ws, ctx
}

func next(t *testing.T, ctx context.Context, ws *websocket.Conn) protocol.Message {
	t.Helper()
	var m protocol.Message
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// restart builds a fresh server and service over the same config directory,
// as after a daemon restart.
func (e *env) restart(t *testing.T) *env {
	t.Helper()
	store, err := config.Open(e.srv.Store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(store)
	svc := Mount(srv)
	srv.OnMessage = svc.HandleMessage
	ts := newTestServer(t, srv)
	return &env{svc: svc, srv: srv, ts: ts, token: e.token, recv: e.recv}
}

func s(v string) *strings.Reader { return strings.NewReader(v) }

func removeFile(p string) error { return os.Remove(p) }
func removeAll(p string) error  { return os.RemoveAll(p) }
