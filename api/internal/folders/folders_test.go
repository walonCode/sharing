package folders

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/protocol"
	"api/internal/server"
)

type env struct {
	home, token string
	ts          *httptest.Server
	store       *config.Store
}

func setup(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{"Documents/work", "Downloads", ".ssh", "Pictures"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(home, "file.txt"), []byte("x"), 0o644)
	store, _ := config.Open(t.TempDir())
	srv := server.New(store)
	MountAt(srv, home)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body, _ := json.Marshal(protocol.PairRequest{Token: srv.Pairing.NewInvite(time.Minute), DeviceID: "p", Name: "p"})
	resp, err := http.Post(ts.URL+"/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var pr protocol.PairResponse
	_ = json.NewDecoder(resp.Body).Decode(&pr)
	real, _ := filepath.EvalSymlinks(home)
	return &env{home: real, token: pr.PeerToken, ts: ts, store: store}
}

func (e *env) do(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestRequiresAuth(t *testing.T) {
	e := setup(t)
	resp, err := http.Get(e.ts.URL + "/fs")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestListDefaultsToHomeAndHidesFilesAndDotDirs(t *testing.T) {
	e := setup(t)
	resp, b := e.do(t, "GET", "/fs", "")
	var out ListResponse
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != 200 || out.Path != e.home || out.Parent != "" {
		t.Fatalf("out = %+v status=%d", out, resp.StatusCode)
	}
	want := []string{"Documents", "Downloads", "Pictures"}
	if len(out.Dirs) != 3 || out.Dirs[0] != want[0] || out.Dirs[1] != want[1] || out.Dirs[2] != want[2] {
		t.Fatalf("dirs = %v", out.Dirs)
	}

	_, b = e.do(t, "GET", "/fs?path="+filepath.Join(e.home, "Documents"), "")
	_ = json.Unmarshal(b, &out)
	if out.Parent != e.home || len(out.Dirs) != 1 || out.Dirs[0] != "work" {
		t.Fatalf("out = %+v", out)
	}
}

func TestCannotEscapeHome(t *testing.T) {
	e := setup(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.home, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/etc", e.home + "/../", outside, filepath.Join(e.home, "escape"), "relative"} {
		if resp, _ := e.do(t, "GET", "/fs?path="+p, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("list %q = %d", p, resp.StatusCode)
		}
	}
	// A symlink leading outside must not be offered as a folder.
	_, b := e.do(t, "GET", "/fs", "")
	var out ListResponse
	_ = json.Unmarshal(b, &out)
	for _, d := range out.Dirs {
		if d == "escape" {
			t.Fatal("escaping symlink listed")
		}
	}
	if resp, _ := e.do(t, "PUT", "/settings", `{"receiveDir":"/etc/evil"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("settings outside home = %d", resp.StatusCode)
	}
}

func TestMkdir(t *testing.T) {
	e := setup(t)
	resp, b := e.do(t, "POST", "/fs/mkdir", `{"parent":"`+e.home+`","name":"Phone Files"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	if st, err := os.Stat(filepath.Join(e.home, "Phone Files")); err != nil || !st.IsDir() {
		t.Fatal("folder not created")
	}
	for _, name := range []string{"", "..", "a/b", `a\b`} {
		body, _ := json.Marshal(map[string]string{"parent": e.home, "name": name})
		if resp, _ := e.do(t, "POST", "/fs/mkdir", string(body)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("name %q = %d", name, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "POST", "/fs/mkdir", `{"parent":"/etc","name":"x"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mkdir outside home = %d", resp.StatusCode)
	}
}

func TestSetReceiveFolder(t *testing.T) {
	e := setup(t)
	target := filepath.Join(e.home, "Documents", "FromPhone", "new")
	resp, b := e.do(t, "PUT", "/settings", `{"receiveDir":"`+target+`"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		t.Fatal("folder not created")
	}
	if got := e.store.Settings().ReceiveDir; got != target {
		t.Fatalf("receiveDir = %q", got)
	}
	_, b = e.do(t, "GET", "/settings", "")
	var s SettingsBody
	_ = json.Unmarshal(b, &s)
	if s.ReceiveDir != target {
		t.Fatalf("get settings = %+v", s)
	}
	if resp, _ := e.do(t, "PUT", "/settings", `{"receiveDir":""}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("empty dir = %d", resp.StatusCode)
	}
}
