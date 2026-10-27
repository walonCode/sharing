package transfer

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"api/internal/protocol"

	"github.com/coder/websocket/wsjson"
)

var v2 = map[string]string{protocol.HeaderProto: "2"}

func hdr(extra ...string) map[string]string {
	h := map[string]string{protocol.HeaderProto: "2"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func (e *env) offerFile(t *testing.T, content string) (Offer, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "data.bin")
	write(t, p, content)
	o, err := e.svc.Send(phoneID, p)
	if err != nil {
		t.Fatal(err)
	}
	return o, p
}

func TestRangeRequests(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "0123456789")
	etag := o.ETag
	cases := []struct {
		name   string
		header map[string]string
		code   int
		body   string
		crange string
	}{
		{"whole", hdr(), 200, "0123456789", ""},
		{"start", hdr("Range", "bytes=0-3"), 206, "0123", "bytes 0-3/10"},
		{"middle", hdr("Range", "bytes=4-6"), 206, "456", "bytes 4-6/10"},
		{"open ended", hdr("Range", "bytes=5-"), 206, "56789", "bytes 5-9/10"},
		{"suffix", hdr("Range", "bytes=-3"), 206, "789", "bytes 7-9/10"},
		{"end past size is clamped", hdr("Range", "bytes=8-99"), 206, "89", "bytes 8-9/10"},
		{"start past end", hdr("Range", "bytes=20-"), 416, "", ""},
		{"If-Range hit", hdr("Range", "bytes=5-", "If-Range", etag), 206, "56789", "bytes 5-9/10"},
		{"If-Range miss sends it all", hdr("Range", "bytes=5-", "If-Range", `"stale"`), 200, "0123456789", ""},
	}
	for _, c := range cases {
		resp, body := e.doH(t, "GET", "/transfer/"+o.ID, nil, c.header)
		if resp.StatusCode != c.code {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.code)
			continue
		}
		if c.code != 416 && body != c.body {
			t.Errorf("%s: body %q, want %q", c.name, body, c.body)
		}
		if got := resp.Header.Get("Content-Range"); c.crange != "" && got != c.crange {
			t.Errorf("%s: Content-Range %q, want %q", c.name, got, c.crange)
		}
		if c.code != 416 && (resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("ETag") != etag) {
			t.Errorf("%s: headers %v", c.name, resp.Header)
		}
	}
}

func TestOfferSurvivesFailedGetAndEndsOnAck(t *testing.T) {
	e := setup(t)
	ws, ctx := e.connect(t)
	o, _ := e.offerFile(t, strings.Repeat("x", 1000))
	if m := next(t, ctx, ws); m.Type != protocol.TypeOffer || m.ID != o.ID {
		t.Fatalf("offer = %+v", m)
	}
	// A download that dies after a few bytes: ask for a prefix and drop it.
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr("Range", "bytes=0-9")); resp.StatusCode != 206 {
		t.Fatalf("first attempt = %d", resp.StatusCode)
	}
	if cur, _ := e.svc.Registry.Get(o.ID, ""); cur.State != StateActive || cur.Attempts != 1 {
		t.Fatalf("after a partial GET: %+v", cur)
	}
	if resp, body := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr("Range", "bytes=10-")); resp.StatusCode != 206 || len(body) != 990 {
		t.Fatalf("resume = %d len %d", resp.StatusCode, len(body))
	}
	if cur, _ := e.svc.Registry.Get(o.ID, ""); cur.Attempts != 1 {
		t.Fatalf("a resumed range must not count as a new attempt: %+v", cur)
	}

	_ = wsjson.Write(ctx, ws, protocol.Message{Type: protocol.TypeProgress, ID: o.ID, Offset: 500})
	waitFor(t, func() bool { c, _ := e.svc.Registry.Get(o.ID, ""); return c.Offset == 500 })

	_ = wsjson.Write(ctx, ws, protocol.Message{Type: protocol.TypeAck, ID: o.ID, Status: protocol.StatusOK})
	waitFor(t, func() bool { c, _ := e.svc.Registry.Get(o.ID, ""); return c.State == StateDone })
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr()); resp.StatusCode != http.StatusGone {
		t.Fatalf("after ack = %d, want 410", resp.StatusCode)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestAckFailedAndCancelledRecordTheReason(t *testing.T) {
	e := setup(t)
	ws, ctx := e.connect(t)
	o, _ := e.offerFile(t, "abc")
	next(t, ctx, ws)
	_ = wsjson.Write(ctx, ws, protocol.Message{Type: protocol.TypeAck, ID: o.ID, Status: protocol.StatusFailed, Error: "phone storage full"})
	waitFor(t, func() bool { c, _ := e.svc.Registry.Get(o.ID, ""); return c.State == StateFailed })
	if c, _ := e.svc.Registry.Get(o.ID, ""); c.Error != "phone storage full" {
		t.Fatalf("error = %q", c.Error)
	}
	// Retry re-queues and re-delivers it.
	if err := e.svc.Retry(o.ID); err != nil {
		t.Fatal(err)
	}
	if m := next(t, ctx, ws); m.ID != o.ID || m.Type != protocol.TypeOffer {
		t.Fatalf("redelivery = %+v", m)
	}
	if c, _ := e.svc.Registry.Get(o.ID, ""); c.State != StatePending || c.Error != "" {
		t.Fatalf("after retry: %+v", c)
	}
}

func TestPhoneCanCancelWithDelete(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "abc")
	if resp, _ := e.doH(t, "DELETE", "/transfer/"+o.ID, nil, hdr()); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if c, _ := e.svc.Registry.Get(o.ID, ""); c.State != StateCancelled {
		t.Fatalf("state = %s", c.State)
	}
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr()); resp.StatusCode != http.StatusGone {
		t.Fatalf("download of a cancelled offer = %d", resp.StatusCode)
	}
}

func TestLaptopCancelTellsThePhone(t *testing.T) {
	e := setup(t)
	ws, ctx := e.connect(t)
	o, _ := e.offerFile(t, "abc")
	next(t, ctx, ws)
	if err := e.svc.Cancel(o.ID[:8]); err != nil { // a unique prefix is enough
		t.Fatal(err)
	}
	if m := next(t, ctx, ws); m.Type != protocol.TypeCancel || m.ID != o.ID {
		t.Fatalf("cancel message = %+v", m)
	}
}

func TestLegacyClientCompletingTheDownloadFinishesTheOffer(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "legacy bytes")
	resp, body := e.doH(t, "GET", "/transfer/"+o.ID, nil, nil) // no X-Sharing-Proto
	if resp.StatusCode != 200 || body != "legacy bytes" {
		t.Fatalf("download = %d %q", resp.StatusCode, body)
	}
	waitFor(t, func() bool { c, _ := e.svc.Registry.Get(o.ID, ""); return c.State == StateDone })
}

func TestOffersSurviveDaemonRestartAndDropVanishedFiles(t *testing.T) {
	e := setup(t)
	o1, _ := e.offerFile(t, "keep me")
	o2, gone := e.offerFile(t, "delete me")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart(t)
	if _, ok := e2.svc.Registry.Get(o2.ID, ""); ok {
		t.Fatal("offer for a vanished file must not be reloaded")
	}
	resp, body := e2.doH(t, "GET", "/transfer/"+o1.ID, nil, hdr())
	if resp.StatusCode != 200 || body != "keep me" {
		t.Fatalf("after restart: %d %q", resp.StatusCode, body)
	}
	// The registry file is private.
	st, err := os.Stat(filepath.Join(e.srv.Store.Dir(), "offers.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("offers.json: %v %v", err, st)
	}
}

func TestFileModifiedAfterOfferGets412(t *testing.T) {
	e := setup(t)
	o, p := e.offerFile(t, "original")
	write(t, p, "changed!!!")
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr()); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("modified file = %d, want 412", resp.StatusCode)
	}
	// Same size, different mtime is also a change.
	o2, p2 := e.offerFile(t, "12345678")
	write(t, p2, "abcdefgh")
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(p2, future, future)
	if resp, _ := e.doH(t, "GET", "/transfer/"+o2.ID, nil, hdr()); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("touched file = %d, want 412", resp.StatusCode)
	}
}

func TestSymlinkSwappedInAfterOfferIsNotFollowed(t *testing.T) {
	e := setup(t)
	o, p := e.offerFile(t, "secret-free")
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "secret-free")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, p); err != nil {
		t.Fatal(err)
	}
	resp, body := e.doH(t, "GET", "/transfer/"+o.ID, nil, hdr())
	if resp.StatusCode == 200 || strings.Contains(body, "secret-free") {
		t.Fatalf("followed a swapped-in symlink: %d %q", resp.StatusCode, body)
	}
}

func TestOffersBelongToTheirPeer(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "private")
	_, tok := e.otherPeer(t)
	for _, p := range []string{"", "/manifest", "/sha256", "/files/0"} {
		if resp, _ := e.doAs(t, tok, "GET", "/transfer/"+o.ID+p, nil, hdr()); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s by another peer = %d", p, resp.StatusCode)
		}
	}
	if resp, _ := e.doAs(t, tok, "DELETE", "/transfer/"+o.ID, nil, hdr()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE by another peer = %d", resp.StatusCode)
	}
	if c, _ := e.svc.Registry.Get(o.ID, ""); c.State == StateCancelled {
		t.Fatal("another peer cancelled the offer")
	}
}

func TestOfferExpiry(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.now = func() time.Time { return now }
	pending := r.Put(Offer{PeerID: "a", Path: "/x", Kind: KindFile, State: StatePending})
	queued := r.Put(Offer{PeerID: "a", Path: "/y", Kind: KindFile, State: StateQueued})
	done := r.Put(Offer{PeerID: "a", Path: "/z", Kind: KindFile, State: StateDone})

	now = now.Add(IdleTTL + time.Minute)
	if _, ok := r.Get(pending.ID, "a"); ok {
		t.Fatal("an idle delivered offer must expire after 30 minutes")
	}
	if _, ok := r.Get(queued.ID, "a"); !ok {
		t.Fatal("a queued offer waits for the phone and must not idle out")
	}
	if _, ok := r.Get(done.ID, "a"); !ok {
		t.Fatal("finished offers stay visible for an hour")
	}
	now = now.Add(FinishedTTL)
	if _, ok := r.Get(done.ID, "a"); ok {
		t.Fatal("finished offer should have been dropped")
	}
	now = now.Add(MaxAge)
	if _, ok := r.Get(queued.ID, "a"); ok {
		t.Fatal("nothing outlives the absolute cap")
	}
	// Activity keeps an offer alive.
	r2 := NewRegistry()
	now2 := time.Now()
	r2.now = func() time.Time { return now2 }
	o := r2.Put(Offer{PeerID: "a", Path: "/x", State: StateActive})
	for range 5 {
		now2 = now2.Add(20 * time.Minute)
		r2.Update(o.ID, false, func(*Offer) {})
	}
	if _, ok := r2.Get(o.ID, "a"); !ok {
		t.Fatal("an offer with activity must not idle out")
	}
}

func TestOutboxQueuesWhileOfflineAndReplaysOnConnect(t *testing.T) {
	e := setup(t)
	o1, _ := e.offerFile(t, "first")
	o2, _ := e.offerFile(t, "second")
	if o1.State != StateQueued || o2.State != StateQueued {
		t.Fatalf("offline offers: %s %s", o1.State, o2.State)
	}
	ws, ctx := e.connect(t)
	m1, m2 := next(t, ctx, ws), next(t, ctx, ws)
	if m1.ID != o1.ID || m2.ID != o2.ID || m1.Type != protocol.TypeOffer {
		t.Fatalf("replay order: %+v %+v", m1, m2)
	}
	waitFor(t, func() bool { c, _ := e.svc.Registry.Get(o1.ID, ""); return c.State == StatePending })
}

func TestFolderManifestAndEntries(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(t.TempDir(), "docs")
	write(t, filepath.Join(dir, "b.txt"), "BBB")
	write(t, filepath.Join(dir, "a/x.txt"), "X")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink("/etc/passwd", filepath.Join(dir, "link"))
	o, err := e.svc.Send(phoneID, dir)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != KindDir || o.Size != 4 {
		t.Fatalf("offer = %+v", o)
	}
	resp, body := e.doH(t, "GET", "/transfer/"+o.ID+"/manifest", nil, hdr())
	var m Manifest
	if err := json.Unmarshal([]byte(body), &m); err != nil || resp.StatusCode != 200 {
		t.Fatalf("manifest %d %v", resp.StatusCode, err)
	}
	if m.Name != "docs" || len(m.Files) != 2 || m.Files[0].Path != "a/x.txt" || m.Files[1].Path != "b.txt" {
		t.Fatalf("files not sorted/complete: %+v", m.Files)
	}
	if got := strings.Join(m.Dirs, ","); got != "a,empty" {
		t.Fatalf("dirs = %q (symlinks must be skipped, empty dirs kept)", got)
	}
	if resp, body := e.doH(t, "GET", "/transfer/"+o.ID+"/files/1", nil, hdr("Range", "bytes=1-")); resp.StatusCode != 206 || body != "BB" {
		t.Fatalf("entry range = %d %q", resp.StatusCode, body)
	}
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID+"/files/2", nil, hdr()); resp.StatusCode != 404 {
		t.Fatalf("entry out of range = %d", resp.StatusCode)
	}
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID+"/files/-1", nil, hdr()); resp.StatusCode != 404 {
		t.Fatalf("negative entry = %d", resp.StatusCode)
	}
	// A concurrent modification invalidates only the changed entry.
	write(t, filepath.Join(dir, "b.txt"), "CHANGED")
	if resp, _ := e.doH(t, "GET", "/transfer/"+o.ID+"/files/1", nil, hdr()); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("modified entry = %d, want 412", resp.StatusCode)
	}
	if resp, body := e.doH(t, "GET", "/transfer/"+o.ID+"/files/0", nil, hdr()); resp.StatusCode != 200 || body != "X" {
		t.Fatalf("untouched entry = %d %q", resp.StatusCode, body)
	}
	// A file offer has no manifest.
	fo, _ := e.offerFile(t, "plain")
	if resp, _ := e.doH(t, "GET", "/transfer/"+fo.ID+"/manifest", nil, hdr()); resp.StatusCode != 400 {
		t.Fatalf("manifest of a file offer = %d", resp.StatusCode)
	}
}

func TestEntryOpenDoesNotEscapeThroughSymlinkedDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "TOPSECRET")
	write(t, filepath.Join(root, "sub/ok.txt"), "ok")
	m, err := BuildManifest(root, 0)
	if err != nil || len(m.Files) != 1 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	// After the manifest was frozen, "sub" becomes a link to a directory outside.
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outside, "ok.txt"), "ok")
	if f, err := OpenEntry(root, m.Files[0]); err == nil {
		f.Close()
		t.Fatal("OpenEntry followed a symlinked directory out of the offered folder")
	}
}

func TestBuildManifestEntryCap(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(dir, n), n)
	}
	if _, err := BuildManifest(dir, 2); err != ErrTooManyEntries {
		t.Fatalf("err = %v", err)
	}
	if _, err := BuildManifest(dir, 3); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}

func TestParseManifestRejectsHostilePaths(t *testing.T) {
	bad := []Manifest{
		{Files: []Entry{{Path: "../x"}}},
		{Files: []Entry{{Path: "/abs"}}},
		{Files: []Entry{{Path: "a\x1bb"}}},
		{Files: []Entry{{Path: `a\..\b`}}},
		{Files: []Entry{{Path: "dup"}, {Path: "dup"}}},
		{Files: []Entry{{Path: "neg", Size: -1}}},
		{Dirs: []string{"../up"}},
	}
	for i, m := range bad {
		if err := ParseManifest(m, 0); err == nil {
			t.Errorf("case %d accepted: %+v", i, m)
		}
	}
	if err := ParseManifest(Manifest{Dirs: []string{"d"}, Files: []Entry{{Path: "d/ok.txt", Size: 1}}}, 0); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if err := ParseManifest(Manifest{Files: []Entry{{Path: "a"}, {Path: "b"}}}, 1); err != ErrTooManyEntries {
		t.Fatalf("entry cap: %v", err)
	}
}

func TestSha256EndpointWhenReady(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "hello")
	var out struct {
		Ready  bool   `json:"ready"`
		Sha256 string `json:"sha256"`
	}
	waitFor(t, func() bool {
		_, body := e.doH(t, "GET", "/transfer/"+o.ID+"/sha256", nil, hdr())
		out.Ready = false
		_ = json.Unmarshal([]byte(body), &out)
		return out.Ready
	})
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if out.Sha256 != want {
		t.Fatalf("sha256 = %s", out.Sha256)
	}

	dir := filepath.Join(t.TempDir(), "d")
	write(t, filepath.Join(dir, "a"), "hello")
	do, _ := e.svc.Send(phoneID, dir)
	var files struct {
		Ready bool     `json:"ready"`
		Files []string `json:"files"`
	}
	waitFor(t, func() bool {
		_, body := e.doH(t, "GET", "/transfer/"+do.ID+"/sha256", nil, hdr())
		files.Ready = false
		_ = json.Unmarshal([]byte(body), &files)
		return files.Ready
	})
	if len(files.Files) != 1 || files.Files[0] != want {
		t.Fatalf("manifest sums = %v", files.Files)
	}
}

func TestTransferListCoversBothDirectionsAndControlActions(t *testing.T) {
	e := setup(t)
	o, _ := e.offerFile(t, "abcdef")
	_, c := e.create(t, `{"name":"up.bin","size":10}`)
	e.patch(t, c.ID, 0, "12345")

	list := e.svc.List()
	var send, recv *Info
	for i := range list {
		switch list[i].Direction {
		case "send":
			send = &list[i]
		case "receive":
			recv = &list[i]
		}
	}
	if send == nil || recv == nil || send.ID != o.ID || recv.ID != c.ID {
		t.Fatalf("list = %+v", list)
	}
	if recv.Offset != 5 || recv.Size != 10 || recv.Peer != "My Phone" || send.Size != 6 || send.State != StateQueued {
		t.Fatalf("send=%+v recv=%+v", send, recv)
	}
	if _, ok := e.svc.Get(c.ID[:6]); !ok {
		t.Fatal("prefix lookup failed")
	}
	if err := e.svc.Retry(c.ID); err == nil {
		t.Fatal("uploads are retried by the phone, not the CLI")
	}
	if err := e.svc.Cancel(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.Uploads.get(c.ID, ""); ok {
		t.Fatal("cancelled upload still exists")
	}
	if err := e.svc.Retry(o.ID); err == nil {
		t.Fatal("a live offer cannot be retried")
	}
	if err := e.svc.Cancel("nope"); err == nil {
		t.Fatal("unknown id must error")
	}
}
