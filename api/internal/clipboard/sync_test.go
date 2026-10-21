package clipboard

import (
	"context"
	"strings"
	"sync"
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
func (f *fakeBackend) set(t string) { f.Write(context.Background(), t) }

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
