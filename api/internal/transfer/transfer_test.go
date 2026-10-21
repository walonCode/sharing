package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/protocol"
	"api/internal/server"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFolderRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "photos")
	write(t, filepath.Join(src, "a.txt"), "A")
	write(t, filepath.Join(src, "sub", "deep", "b.txt"), "B")
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Pack(&buf, src); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	got, err := Unpack(&buf, dest, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "photos" {
		t.Fatalf("got %s", got)
	}
	if read(t, filepath.Join(got, "a.txt")) != "A" || read(t, filepath.Join(got, "sub/deep/b.txt")) != "B" {
		t.Fatal("contents differ")
	}
	if st, err := os.Stat(filepath.Join(got, "empty")); err != nil || !st.IsDir() {
		t.Fatal("empty dir lost")
	}
	if _, err := os.Lstat(filepath.Join(got, "link")); err == nil {
		t.Fatal("symlink must not be transferred")
	}
}

func tarOf(t *testing.T, hs ...tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hs {
		h := h
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			io.WriteString(tw, strings.Repeat("x", int(h.Size)))
		}
	}
	tw.Close()
	return &buf
}

func TestUnpackRejectsTraversalAndLinks(t *testing.T) {
	cases := map[string]tar.Header{
		"dotdot":   {Typeflag: tar.TypeReg, Name: "../evil", Size: 1, Mode: 0o644},
		"nested":   {Typeflag: tar.TypeReg, Name: "ok/../../evil", Size: 1, Mode: 0o644},
		"absolute": {Typeflag: tar.TypeReg, Name: "/tmp/evil", Size: 1, Mode: 0o644},
		"symlink":  {Typeflag: tar.TypeSymlink, Name: "d/l", Linkname: "/etc/passwd"},
		"hardlink": {Typeflag: tar.TypeLink, Name: "d/h", Linkname: "/etc/passwd"},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "in")
			_, err := Unpack(tarOf(t, h), dest, "x")
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("err = %v", err)
			}
			entries, _ := os.ReadDir(dest)
			if len(entries) != 0 {
				t.Fatalf("staging not cleaned: %v", entries)
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Fatal("file escaped destination")
			}
		})
	}
}

func TestSaveFileCollisionRename(t *testing.T) {
	dest := t.TempDir()
	var got []string
	for _, body := range []string{"one", "two", "three"} {
		p, err := SaveFile(strings.NewReader(body), dest, "pic.jpg")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.Base(p))
	}
	want := []string{"pic.jpg", "pic (1).jpg", "pic (2).jpg"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v", got)
		}
	}
	if read(t, filepath.Join(dest, "pic.jpg")) != "one" {
		t.Fatal("original overwritten")
	}
}

func TestSaveFileSanitizesName(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "in")
	p, err := SaveFile(strings.NewReader("x"), dest, "../../etc/passwd")
	if err != nil || filepath.Dir(p) != dest || filepath.Base(p) != "passwd" {
		t.Fatalf("p=%s err=%v", p, err)
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(b []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("connection reset")
	}
	f.n -= len(b)
	return len(b), nil
}

func TestFailedUploadLeavesNothing(t *testing.T) {
	dest := t.TempDir()
	if _, err := SaveFile(&failingReader{n: 10}, dest, "big.bin"); err == nil {
		t.Fatal("expected error")
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestLargeFileStreams(t *testing.T) {
	dest := t.TempDir()
	const size = 64 << 20
	p, err := SaveFile(io.LimitReader(zeroReader{}, size), dest, "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != size {
		t.Fatalf("size = %d", st.Size())
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestRegistryOneShotOwnershipExpiry(t *testing.T) {
	r := NewRegistry()
	o := r.Add("a", "/x", KindFile, 1)
	if _, ok := r.Take(o.ID, "b"); ok {
		t.Fatal("other peer must not take offer")
	}
	if _, ok := r.Take(o.ID, "a"); !ok {
		t.Fatal("owner should take offer")
	}
	if _, ok := r.Take(o.ID, "a"); ok {
		t.Fatal("offer must be single use")
	}

	now := time.Now()
	r.now = func() time.Time { return now }
	o = r.Add("a", "/x", KindFile, 1)
	r.now = func() time.Time { return now.Add(OfferTTL + time.Second) }
	if _, ok := r.Take(o.ID, "a"); ok {
		t.Fatal("expired offer taken")
	}
}

// --- end to end over the real HTTP server ---

type env struct {
	svc   *Service
	srv   *server.Server
	ts    *httptest.Server
	token string
	recv  string
}

func setup(t *testing.T) *env {
	t.Helper()
	store, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recv := filepath.Join(t.TempDir(), "recv")
	_ = store.UpdateSettings(func(s *config.Settings) { s.ReceiveDir = recv })
	srv := server.New(store)
	svc := Mount(srv)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body, _ := json.Marshal(protocol.PairRequest{Token: srv.Pairing.NewInvite(time.Minute), DeviceID: "phone", Name: "p"})
	resp, err := http.Post(ts.URL+"/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var pr protocol.PairResponse
	_ = json.NewDecoder(resp.Body).Decode(&pr)
	return &env{svc: svc, srv: srv, ts: ts, token: pr.PeerToken, recv: recv}
}

func (e *env) do(t *testing.T, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestUploadFileAndFolderRequireAuth(t *testing.T) {
	e := setup(t)

	req, _ := http.NewRequest("PUT", e.ts.URL+"/upload?name=a.txt", strings.NewReader("hi"))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth upload = %d", resp.StatusCode)
	}

	if resp := e.do(t, "PUT", "/upload?name=a.txt", strings.NewReader("hi")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("file upload = %d", resp.StatusCode)
	}
	if read(t, filepath.Join(e.recv, "a.txt")) != "hi" {
		t.Fatal("file content wrong")
	}

	dir := filepath.Join(t.TempDir(), "album")
	write(t, filepath.Join(dir, "x/y.txt"), "Y")
	var buf bytes.Buffer
	_ = Pack(&buf, dir)
	if resp := e.do(t, "PUT", "/upload?name=album&kind=dir", &buf); resp.StatusCode != http.StatusCreated {
		t.Fatalf("dir upload = %d", resp.StatusCode)
	}
	if read(t, filepath.Join(e.recv, "album/x/y.txt")) != "Y" {
		t.Fatal("folder content wrong")
	}

	evil := tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "../../escape", Size: 1, Mode: 0o644})
	if resp := e.do(t, "PUT", "/upload?name=x&kind=dir", evil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("traversal upload = %d", resp.StatusCode)
	}
}

func TestSendOfferAndDownload(t *testing.T) {
	e := setup(t)

	if _, err := e.svc.Send("phone", "/nonexistent"); err == nil {
		t.Fatal("missing path must error")
	}
	file := filepath.Join(t.TempDir(), "movie.mp4")
	write(t, file, "frames")
	if _, err := e.svc.Send("phone", file); !errors.Is(err, ErrPeerOffline) {
		t.Fatalf("offline err = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+e.ts.URL[4:]+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + e.token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	var m protocol.Message
	_ = wsjson.Read(ctx, ws, &m) // hello

	if _, err := e.svc.Send("phone", file); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, ws, &m); err != nil || m.Type != protocol.TypeOffer || m.Name != "movie.mp4" || m.Kind != KindFile || m.Size != 6 {
		t.Fatalf("offer = %+v err=%v", m, err)
	}
	resp := e.do(t, "GET", "/transfer/"+m.ID, nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "frames" || resp.Header.Get("X-Name") != "movie.mp4" {
		t.Fatalf("download = %d %q", resp.StatusCode, b)
	}
	if again := e.do(t, "GET", "/transfer/"+m.ID, nil); again.StatusCode != http.StatusNotFound {
		t.Fatalf("second download = %d", again.StatusCode)
	}

	// folder offer downloads as tar
	dir := filepath.Join(t.TempDir(), "docs")
	write(t, filepath.Join(dir, "r.md"), "R")
	if _, err := e.svc.Send("phone", dir); err != nil {
		t.Fatal(err)
	}
	_ = wsjson.Read(ctx, ws, &m)
	resp = e.do(t, "GET", "/transfer/"+m.ID, nil)
	got, err := Unpack(resp.Body, t.TempDir(), "x")
	if err != nil || read(t, filepath.Join(got, "r.md")) != "R" {
		t.Fatalf("folder download: %v", err)
	}
}
