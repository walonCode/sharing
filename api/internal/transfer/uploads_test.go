package transfer

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/internal/config"
)

type createdUpload struct {
	ID       string `json:"id"`
	DirID    string `json:"dirId"`
	Offset   int64  `json:"offset"`
	MaxChunk int64  `json:"maxChunk"`
}

func (e *env) create(t *testing.T, spec string) (*http.Response, createdUpload) {
	t.Helper()
	resp, body := e.doH(t, "POST", "/uploads", s(spec), map[string]string{"Content-Type": "application/json"})
	var c createdUpload
	_ = json.Unmarshal([]byte(body), &c)
	return resp, c
}

func (e *env) head(t *testing.T, id string) (int64, *http.Response) {
	t.Helper()
	resp, _ := e.doH(t, "HEAD", "/uploads/"+id, nil, nil)
	off, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	return off, resp
}

func (e *env) patch(t *testing.T, id string, offset int64, body string, extra ...string) (*http.Response, string) {
	t.Helper()
	h := map[string]string{"Upload-Offset": strconv.FormatInt(offset, 10)}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return e.doH(t, "PATCH", "/uploads/"+id, s(body), h)
}

func (e *env) phoneDir() string { return filepath.Join(e.recv, "My Phone") }

func visible(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, en := range entries {
		if !strings.HasPrefix(en.Name(), ".partial-") {
			out = append(out, en.Name())
		}
	}
	return out
}

func TestResumableUploadWholeFile(t *testing.T) {
	e := setup(t)
	resp, c := e.create(t, `{"name":"movie.mp4","size":10}`)
	if resp.StatusCode != http.StatusCreated || c.ID == "" || c.Offset != 0 {
		t.Fatalf("create = %d %+v", resp.StatusCode, c)
	}
	if off, r := e.head(t, c.ID); off != 0 || r.Header.Get("Upload-Length") != "10" {
		t.Fatalf("head: offset=%d length=%q", off, r.Header.Get("Upload-Length"))
	}
	r, _ := e.patch(t, c.ID, 0, "01234")
	if r.StatusCode != http.StatusNoContent || r.Header.Get("Upload-Offset") != "5" {
		t.Fatalf("first chunk = %d offset=%q", r.StatusCode, r.Header.Get("Upload-Offset"))
	}
	if got := visible(t, e.phoneDir()); len(got) != 0 {
		t.Fatalf("incomplete upload is visible: %v", got)
	}
	r, body := e.patch(t, c.ID, 5, "56789")
	if r.StatusCode != http.StatusCreated || !strings.Contains(body, `"movie.mp4"`) {
		t.Fatalf("last chunk = %d %s", r.StatusCode, body)
	}
	if read(t, filepath.Join(e.phoneDir(), "movie.mp4")) != "0123456789" {
		t.Fatal("content wrong")
	}
	if left, _ := filepath.Glob(filepath.Join(e.phoneDir(), ".partial-*")); len(left) != 0 {
		t.Fatalf("partial left behind: %v", left)
	}
}

// writeRaw sends a PATCH with a Content-Length larger than the bytes it writes,
// then drops the connection: a WiFi drop in the middle of a chunk.
func (e *env) killMidPatch(t *testing.T, id string, offset int64, declared int, sent string) {
	t.Helper()
	conn, err := net.Dial("tcp", e.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	req := "PATCH /uploads/" + id + " HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + e.token +
		"\r\nUpload-Offset: " + strconv.FormatInt(offset, 10) + "\r\nContent-Length: " + strconv.Itoa(declared) + "\r\n\r\n" + sent
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	conn.Close()
}

func TestUploadResumesAfterConnectionKilledMidPatch(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"clip.bin","size":12}`)
	e.killMidPatch(t, c.ID, 0, 12, "abcde")
	var off int64
	for range 100 { // the server notices the dead connection asynchronously
		if off, _ = e.head(t, c.ID); off == 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if off != 5 {
		t.Fatalf("server stored %d bytes, want the 5 that arrived", off)
	}
	// The first handler may still be draining; 423 tells the client to back off and retry.
	var r *http.Response
	var body string
	for range 100 {
		if r, body = e.patch(t, c.ID, off, "fghijkl"); r.StatusCode != http.StatusLocked {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("resume = %d %s", r.StatusCode, body)
	}
	if read(t, filepath.Join(e.phoneDir(), "clip.bin")) != "abcdefghijkl" {
		t.Fatal("resumed content wrong")
	}
}

func TestWrongOffsetGets409WithCorrectOffset(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"a.bin","size":10}`)
	e.patch(t, c.ID, 0, "abc")
	for _, off := range []int64{0, 2, 9} {
		r, _ := e.patch(t, c.ID, off, "zzz")
		if r.StatusCode != http.StatusConflict || r.Header.Get("Upload-Offset") != "3" {
			t.Fatalf("offset %d = %d (Upload-Offset %q)", off, r.StatusCode, r.Header.Get("Upload-Offset"))
		}
	}
	if off, _ := e.head(t, c.ID); off != 3 {
		t.Fatalf("a rejected PATCH changed the data: offset %d", off)
	}
	if r, _ := e.doH(t, "PATCH", "/uploads/"+c.ID, s("x"), nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing Upload-Offset = %d", r.StatusCode)
	}
}

func TestDuplicateFinalPatchDoesNotDuplicateTheFile(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"once.txt","size":4}`)
	r1, b1 := e.patch(t, c.ID, 0, "data")
	r2, b2 := e.patch(t, c.ID, 0, "data") // the response to the first was lost
	if r1.StatusCode != 201 || r2.StatusCode != 201 || b1 != b2 {
		t.Fatalf("%d %q / %d %q", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	if got := visible(t, e.phoneDir()); len(got) != 1 || got[0] != "once.txt" {
		t.Fatalf("files = %v, want exactly once.txt", got)
	}
	if off, r := e.head(t, c.ID); off != 4 || r.Header.Get("Upload-Complete") != "true" {
		t.Fatalf("head after done: %d %q", off, r.Header.Get("Upload-Complete"))
	}
}

func TestTwoConcurrentPatchesGet423(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"slow.bin","size":10}`)
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("PATCH", e.ts.URL+"/uploads/"+c.ID, pr)
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Upload-Offset", "0")
	done := make(chan struct{})
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	_, _ = pw.Write([]byte("ab"))
	var code int
	for range 100 {
		r, _ := e.patch(t, c.ID, 0, "zz")
		if code = r.StatusCode; code == http.StatusLocked {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusLocked {
		t.Fatalf("second writer = %d, want 423", code)
	}
	pw.Close()
	<-done
}

func TestUnknownSizeCompletesWithUploadComplete(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"stream.bin"}`)
	r, _ := e.patch(t, c.ID, 0, "abc")
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("no size, no complete flag = %d", r.StatusCode)
	}
	r, _ = e.patch(t, c.ID, 3, "def", "Upload-Complete", "true")
	if r.StatusCode != http.StatusCreated || read(t, filepath.Join(e.phoneDir(), "stream.bin")) != "abcdef" {
		t.Fatalf("complete = %d", r.StatusCode)
	}
}

func TestUploadCompleteFlagBeforeAllBytesIsRefused(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"a","size":10}`)
	if r, _ := e.patch(t, c.ID, 0, "abc", "Upload-Complete", "true"); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("early complete = %d", r.StatusCode)
	}
}

func TestPatchBeyondDeclaredSizeIsRefused(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"a","size":3}`)
	if r, _ := e.patch(t, c.ID, 0, "abcdef"); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("overlong body = %d", r.StatusCode)
	}
}

func TestUploadLimitsAtCreation(t *testing.T) {
	e := setup(t)
	e.settings(t, func(s *config.Settings) { s.MaxUploadBytes = 100; s.MaxPartialBytes = 150 })
	if r, _ := e.create(t, `{"name":"huge","size":101}`); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over MaxUploadBytes = %d", r.StatusCode)
	}
	if r, _ := e.create(t, `{"name":"a","size":100}`); r.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d", r.StatusCode)
	}
	if r, _ := e.create(t, `{"name":"b","size":60}`); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the per-peer partial cap = %d", r.StatusCode)
	}
	fakeFree(t, 10)
	e.settings(t, func(s *config.Settings) { s.MinFreeBytes = 0 })
	if r, _ := e.create(t, `{"name":"c","size":50}`); r.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("disk full = %d", r.StatusCode)
	}
}

func TestUploadsBelongToTheirPeer(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"mine.bin","size":4}`)
	_, tok := e.otherPeer(t)
	for _, m := range []string{"HEAD", "PATCH", "DELETE"} {
		resp, _ := e.doAs(t, tok, m, "/uploads/"+c.ID, s("zzzz"), map[string]string{"Upload-Offset": "0"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s by another peer = %d, want 404", m, resp.StatusCode)
		}
	}
	if r, _ := e.doAs(t, tok, "POST", "/dirs/"+c.ID+"/complete", s(`{"count":0}`), nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("complete by another peer = %d", r.StatusCode)
	}
	if off, _ := e.head(t, c.ID); off != 0 {
		t.Fatal("another peer wrote to the session")
	}
}

func TestAbandonRemovesPartial(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"gone.bin","size":4}`)
	e.patch(t, c.ID, 0, "ab")
	if r, _ := e.doH(t, "DELETE", "/uploads/"+c.ID, nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	if left, _ := filepath.Glob(filepath.Join(e.phoneDir(), ".partial-*")); len(left) != 0 {
		t.Fatalf("partial remains: %v", left)
	}
	if r, _ := e.doH(t, "HEAD", "/uploads/"+c.ID, nil, nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("head after delete = %d", r.StatusCode)
	}
}

func TestJanitorRemovesIdleAndFinishedSessions(t *testing.T) {
	e := setup(t)
	_, idle := e.create(t, `{"name":"idle.bin","size":4}`)
	_, fin := e.create(t, `{"name":"fin.bin","size":1}`)
	e.patch(t, fin.ID, 0, "x")
	_, fresh := e.create(t, `{"name":"fresh.bin","size":4}`)

	now := time.Now()
	e.svc.Uploads.now = func() time.Time { return now.Add(UploadIdleTTL + time.Hour) }
	// "fresh" was active just now relative to its own clock, so touch it forward.
	if up, ok := e.svc.Uploads.get(fresh.ID, ""); ok {
		up.Activity = now.Add(UploadIdleTTL + 30*time.Minute)
	}
	if n := e.svc.Uploads.Janitor(); n != 2 {
		t.Fatalf("janitor removed %d, want 2 (idle and finished)", n)
	}
	for _, id := range []string{idle.ID, fin.ID} {
		if _, ok := e.svc.Uploads.get(id, ""); ok {
			t.Errorf("session %s survived", id)
		}
		if _, err := os.Stat(e.svc.Uploads.sidecar(id)); err == nil {
			t.Errorf("sidecar of %s survived", id)
		}
	}
	if _, ok := e.svc.Uploads.get(fresh.ID, ""); !ok {
		t.Fatal("active session was removed")
	}
	if got := visible(t, e.phoneDir()); len(got) != 1 || got[0] != "fin.bin" {
		t.Fatalf("finished file must stay: %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(e.phoneDir(), ".partial-*")); len(left) != 1 {
		t.Fatalf("only the fresh partial should remain: %v", left)
	}
}

func TestUnpairMidUploadDeletesSessions(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"x.bin","size":10}`)
	e.patch(t, c.ID, 0, "abc")
	_, d := e.create(t, `{"name":"album","kind":"dir"}`)
	if err := e.srv.RemovePeer(phoneID); err != nil {
		t.Fatal(err)
	}
	if len(e.svc.Uploads.List()) != 0 {
		t.Fatalf("sessions left: %+v", e.svc.Uploads.List())
	}
	if left, _ := filepath.Glob(filepath.Join(e.phoneDir(), ".partial-*")); len(left) != 0 {
		t.Fatalf("partials left: %v", left)
	}
	if r, _ := e.doH(t, "PATCH", "/uploads/"+c.ID, s("d"), map[string]string{"Upload-Offset": "3"}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired token still works: %d", r.StatusCode)
	}
	_ = d
}

func TestSessionsSurviveDaemonRestart(t *testing.T) {
	e := setup(t)
	_, c := e.create(t, `{"name":"big.bin","size":8}`)
	e.patch(t, c.ID, 0, "abcd")

	e2 := e.restart(t)
	if off, r := e2.head(t, c.ID); off != 4 || r.StatusCode != http.StatusNoContent {
		t.Fatalf("after restart: offset=%d status=%d", off, r.StatusCode)
	}
	r, _ := e2.patch(t, c.ID, 4, "efgh")
	if r.StatusCode != http.StatusCreated || read(t, filepath.Join(e.phoneDir(), "big.bin")) != "abcdefgh" {
		t.Fatalf("finish after restart = %d", r.StatusCode)
	}
}

func TestFolderUploadPublishesAtomically(t *testing.T) {
	e := setup(t)
	_, d := e.create(t, `{"name":"album","kind":"dir"}`)
	if d.DirID == "" {
		t.Fatal("no dirId")
	}
	member := func(rel, content string) *http.Response {
		_, m := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"`+d.DirID+`","relPath":"`+rel+`","size":`+strconv.Itoa(len(content))+`}`)
		if m.ID == "" {
			t.Fatalf("no session for %s", rel)
		}
		r, _ := e.patch(t, m.ID, 0, content)
		return r
	}
	if r := member("a.txt", "A"); r.StatusCode != 201 {
		t.Fatalf("member a = %d", r.StatusCode)
	}
	if r := member("sub/deep/b.txt", "BB"); r.StatusCode != 201 {
		t.Fatalf("member b = %d", r.StatusCode)
	}
	if got := visible(t, e.phoneDir()); len(got) != 0 {
		t.Fatalf("folder visible before completion: %v", got)
	}
	complete := func(n int) (*http.Response, string) {
		return e.doH(t, "POST", "/dirs/"+d.DirID+"/complete", s(`{"count":`+strconv.Itoa(n)+`}`), nil)
	}
	if r, _ := complete(3); r.StatusCode != http.StatusConflict {
		t.Fatalf("wrong count = %d, want 409", r.StatusCode)
	}
	r, body := complete(2)
	if r.StatusCode != http.StatusCreated || !strings.Contains(body, `"album"`) {
		t.Fatalf("complete = %d %s", r.StatusCode, body)
	}
	if read(t, filepath.Join(e.phoneDir(), "album/a.txt")) != "A" || read(t, filepath.Join(e.phoneDir(), "album/sub/deep/b.txt")) != "BB" {
		t.Fatal("folder content wrong")
	}
	if r2, b2 := complete(2); r2.StatusCode != 201 || b2 != body {
		t.Fatalf("repeated complete = %d %s", r2.StatusCode, b2)
	}
	if got := visible(t, e.phoneDir()); len(got) != 1 {
		t.Fatalf("expected one folder, got %v", got)
	}
}

func TestFolderMembersAreValidated(t *testing.T) {
	e := setup(t)
	_, d := e.create(t, `{"name":"album","kind":"dir"}`)
	for _, rel := range []string{"../escape.txt", "/abs.txt", "a/../../b", "ctl\\u001bchar.txt", ""} {
		r, _ := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"`+d.DirID+`","relPath":"`+rel+`","size":1}`)
		if r.StatusCode != http.StatusUnprocessableEntity && r.StatusCode != http.StatusBadRequest {
			t.Errorf("relPath %q = %d", rel, r.StatusCode)
		}
	}
	if r, _ := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"nope","relPath":"a","size":1}`); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown dir = %d", r.StatusCode)
	}
	// A duplicate path in one folder is refused when the second file finishes.
	_, m1 := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"`+d.DirID+`","relPath":"same.txt","size":1}`)
	e.patch(t, m1.ID, 0, "1")
	_, m2 := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"`+d.DirID+`","relPath":"same.txt","size":1}`)
	if r, _ := e.patch(t, m2.ID, 0, "2"); r.StatusCode != http.StatusConflict {
		t.Errorf("duplicate path = %d", r.StatusCode)
	}
	// Incomplete members block completion.
	if r, _ := e.doH(t, "POST", "/dirs/"+d.DirID+"/complete", s(`{"count":1}`), nil); r.StatusCode != http.StatusConflict {
		t.Errorf("complete with an unfinished member = %d", r.StatusCode)
	}
}

func TestFolderEntryCap(t *testing.T) {
	e := setup(t)
	e.settings(t, func(s *config.Settings) { s.MaxEntries = 2 })
	_, d := e.create(t, `{"name":"album","kind":"dir"}`)
	for i, want := range []int{201, 201, 413} {
		r, _ := e.create(t, `{"name":"x","kind":"file-in-dir","dirId":"`+d.DirID+`","relPath":"f`+strconv.Itoa(i)+`","size":1}`)
		if r.StatusCode != want {
			t.Fatalf("member %d = %d, want %d", i, r.StatusCode, want)
		}
	}
}

func TestTooManyOpenUploadsAreRefused(t *testing.T) {
	e := setup(t)
	for i := 0; i < maxOpenUploads; i++ {
		if r, _ := e.create(t, `{"name":"f`+strconv.Itoa(i)+`","size":1}`); r.StatusCode != 201 {
			t.Fatalf("upload %d = %d", i, r.StatusCode)
		}
	}
	if r, _ := e.create(t, `{"name":"one-too-many","size":1}`); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over the open-session cap = %d", r.StatusCode)
	}
}

func TestCreateRejectsBadRequests(t *testing.T) {
	e := setup(t)
	for name, spec := range map[string]string{
		"no name":      `{"size":1}`,
		"bad kind":     `{"name":"a","kind":"socket"}`,
		"negative":     `{"name":"a","size":-5}`,
		"control name": `{"name":"a\u001bb"}`,
		"not json":     `nope`,
	} {
		if r, _ := e.create(t, spec); r.StatusCode < 400 || r.StatusCode >= 500 {
			t.Errorf("%s = %d, want 4xx", name, r.StatusCode)
		}
	}
	if r, _ := e.doH(t, "PATCH", "/uploads/does-not-exist", s("x"), map[string]string{"Upload-Offset": "0"}); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id = %d", r.StatusCode)
	}
}

func TestUploadsRequireAuth(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/uploads", "/uploads/x", "/dirs/x/complete"} {
		m := "POST"
		if strings.HasPrefix(p, "/uploads/") {
			m = "HEAD"
		}
		req, _ := http.NewRequest(m, e.ts.URL+p, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d", m, p, resp.StatusCode)
		}
	}
}
