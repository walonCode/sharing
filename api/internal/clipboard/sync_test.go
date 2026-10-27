package clipboard

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api/internal/protocol"
)

type fakeBackend struct {
	mu   sync.Mutex
	text string
}

func (f *fakeBackend) Read(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text, nil
}
func (f *fakeBackend) Write(_ context.Context, t string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text = t
	return nil
}
func (f *fakeBackend) set(t string) { _ = f.Write(context.Background(), t) }

type recorder struct {
	mu   sync.Mutex
	sent []protocol.Message
}

func (r *recorder) Broadcast(m protocol.Message) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return 1
}
func (r *recorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.sent) }

func newTest() (*Syncer, *fakeBackend, *recorder) {
	b, r := &fakeBackend{}, &recorder{}
	return NewSyncer(b, r), b, r
}

func TestLocalChangeIsBroadcastOnce(t *testing.T) {
	s, b, r := newTest()
	ctx := context.Background()
	b.set("hello")
	s.poll(ctx)
	s.poll(ctx) // unchanged: no resend
	if r.count() != 1 || r.sent[0].Data != "hello" || r.sent[0].Type != protocol.TypeClip {
		t.Fatalf("sent = %+v", r.sent)
	}
	b.set("world")
	s.poll(ctx)
	if r.count() != 2 {
		t.Fatalf("sent = %+v", r.sent)
	}
}

func TestRemoteClipIsWrittenAndNotEchoed(t *testing.T) {
	s, b, r := newTest()
	ctx := context.Background()
	s.HandleMessage(ctx, "phone", protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: "from phone", TS: 100})
	if got, _ := b.Read(ctx); got != "from phone" {
		t.Fatalf("clipboard = %q", got)
	}
	s.poll(ctx) // the poll sees our own write; must not bounce it back
	if r.count() != 0 {
		t.Fatalf("echoed: %+v", r.sent)
	}
}

func TestStaleRemoteClipDropped(t *testing.T) {
	s, b, _ := newTest()
	ctx := context.Background()
	s.now = func() time.Time { return time.UnixMilli(5000) }
	b.set("local newer")
	s.poll(ctx)
	s.HandleMessage(ctx, "phone", protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: "older", TS: 4000})
	if got, _ := b.Read(ctx); got != "local newer" {
		t.Fatalf("clipboard = %q", got)
	}
}

func TestOversizeAndWrongTypeIgnored(t *testing.T) {
	s, b, r := newTest()
	ctx := context.Background()
	b.set(strings.Repeat("x", MaxBytes+1))
	s.poll(ctx)
	s.HandleMessage(ctx, "p", protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: strings.Repeat("y", MaxBytes+1), TS: 1})
	s.HandleMessage(ctx, "p", protocol.Message{Type: protocol.TypeClip, Mime: "image/png", Data: "z", TS: 1})
	s.HandleMessage(ctx, "p", protocol.Message{Type: protocol.TypeOffer, Data: "z", TS: 1})
	if r.count() != 0 {
		t.Fatal("oversize local clip broadcast")
	}
	if got, _ := b.Read(ctx); len(got) != MaxBytes+1 {
		t.Fatal("remote changed clipboard")
	}
}

func TestStartupContentNotBroadcast(t *testing.T) {
	s, b, r := newTest()
	b.set("pre-existing secret")
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if r.count() != 0 {
		t.Fatalf("startup clipboard leaked: %+v", r.sent)
	}
}

func TestManagerWaitsForClipboardSession(t *testing.T) {
	b, r := &fakeBackend{}, &recorder{}
	var calls atomic.Int32
	m := NewManager(r)
	m.Retry = 10 * time.Millisecond
	m.Detect = func() (Backend, error) {
		if calls.Add(1) < 3 {
			return nil, ErrNoBackend // no desktop session yet
		}
		return b, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	// Before a backend exists, remote clips are dropped without panicking.
	m.HandleMessage(ctx, "p", protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: "early", TS: 1})

	deadline := time.Now().Add(2 * time.Second)
	for m.cur.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.cur.Load() == nil {
		t.Fatal("manager never picked up the clipboard")
	}
	m.HandleMessage(ctx, "p", protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: "late", TS: 2})
	if got, _ := b.Read(ctx); got != "late" {
		t.Fatalf("clipboard = %q", got)
	}
	cancel()
	<-done
}

func TestPollIntervalBacksOffWhenQuiet(t *testing.T) {
	if pollInterval(0) != fastPoll || pollInterval(quietAfter-time.Second) != fastPoll {
		t.Fatal("active clipboard must poll fast")
	}
	if pollInterval(quietAfter) != slowPoll || pollInterval(time.Hour) != slowPoll {
		t.Fatal("quiet clipboard must poll slowly")
	}
}

// watchBackend is a fakeBackend that announces changes itself.
type watchBackend struct {
	fakeBackend
	wmu    sync.Mutex
	events chan struct{}
	starts atomic.Int32
}

func (w *watchBackend) Watch(context.Context) (<-chan struct{}, error) {
	w.starts.Add(1)
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return w.events, nil
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 300 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchEventsDriveReadsWithoutPolling(t *testing.T) {
	b := &watchBackend{events: make(chan struct{}, 1)}
	r := &recorder{}
	s := NewSyncer(b, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(2 * fastPoll) // longer than a poll tick: a polling loop would have noticed a silent change
	b.set("changed silently")
	time.Sleep(2 * fastPoll)
	if r.count() != 0 {
		t.Fatal("a watch-driven syncer must not poll")
	}
	b.events <- struct{}{}
	waitUntil(t, "broadcast after the change event", func() bool { return r.count() == 1 })
	if r.sent[0].Data != "changed silently" {
		t.Fatalf("sent %+v", r.sent)
	}
}

func TestDeadWatcherFallsBackToPollingThenRewatches(t *testing.T) {
	b := &watchBackend{events: make(chan struct{})}
	r := &recorder{}
	s := NewSyncer(b, r)
	s.rewatch = 1500 * time.Millisecond // must exceed fastPoll or the fallback never gets a tick
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitUntil(t, "first watch", func() bool { return b.starts.Load() == 1 })
	b.wmu.Lock()
	dead := b.events
	b.events = make(chan struct{}) // what the next watch attempt gets
	b.wmu.Unlock()
	close(dead) // the watch tool died
	b.set("typed while the watcher was down")
	waitUntil(t, "poll fallback to notice the change", func() bool { return r.count() == 1 })
	waitUntil(t, "watch restart", func() bool { return b.starts.Load() >= 2 })
}

func TestExecReadIsCappedAndDoesNotDrainHugeClipboards(t *testing.T) {
	be := execBackend{read: []string{"sh", "-c", "head -c 209715200 /dev/zero"}}
	start := time.Now()
	v, err := be.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != MaxBytes+1 {
		t.Fatalf("read %d bytes, want exactly MaxBytes+1 so the caller can ignore it", len(v))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("reading a huge clipboard was not cut short")
	}
	// The syncer ignores it.
	s, fb, r := newTest()
	fb.set(v)
	s.poll(context.Background())
	if r.count() != 0 {
		t.Fatal("an oversized clip must not be sent")
	}
}

func TestExecReadTreatsToolFailureAsEmpty(t *testing.T) {
	v, err := execBackend{read: []string{"sh", "-c", "exit 1"}}.Read(context.Background())
	if v != "" || err != nil {
		t.Fatalf("got %q, %v", v, err)
	}
	if _, err := (execBackend{read: []string{"/definitely/not/installed"}}).Read(context.Background()); err == nil {
		t.Fatal("a missing tool is an error")
	}
	v, _ = execBackend{read: []string{"printf", "héllo"}}.Read(context.Background())
	if v != "héllo" {
		t.Fatalf("got %q", v)
	}
}

func TestExecWatchEmitsOneEventPerLine(t *testing.T) {
	be := execBackend{watch: []string{"sh", "-c", "echo; sleep 0.1; echo; sleep 0.1; echo"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := be.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range events {
		n++
	}
	if n < 2 || n > 3 {
		t.Fatalf("events = %d, want 2 or 3 (bursts may coalesce)", n)
	}
	if _, err := (execBackend{}).Watch(ctx); err != ErrNoWatch {
		t.Fatalf("no watch command = %v", err)
	}
}
