package clipboard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"api/internal/protocol"
)

const (
	MaxBytes = protocol.MaxClipBytes // larger clips are ignored
)

// Broadcaster delivers a message to all connected peers (the server hub).
type Broadcaster interface {
	Broadcast(protocol.Message) int
}

// Syncer mirrors clipboard changes both ways. The loop guard: `last` is the
// most recent value known to be in the clipboard, whether it got there locally
// or from a peer, so a value we just wrote is never echoed back, and remote
// updates older than what we already applied are dropped (last writer wins).
type Syncer struct {
	Backend Backend
	Out     Broadcaster

	mu     sync.Mutex
	last   string
	lastTS int64
	active time.Time // last change seen, local or remote
	now    func() time.Time

	rewatch time.Duration // pause between watch attempts
}

func NewSyncer(b Backend, out Broadcaster) *Syncer {
	return &Syncer{Backend: b, Out: out, now: time.Now, rewatch: rewatchDelay}
}

// Polling cadence when the backend cannot watch: fast while the clipboard is
// active, slow once it has been quiet for quietAfter. Any change (local or
// from a peer) returns it to fast.
const (
	fastPoll   = 400 * time.Millisecond
	slowPoll   = 2 * time.Second
	quietAfter = 30 * time.Second
	// rewatchDelay is how long to wait before restarting a watch that ended.
	rewatchDelay = 5 * time.Second
)

// pollInterval chooses the delay before the next poll.
func pollInterval(sinceChange time.Duration) time.Duration {
	if sinceChange >= quietAfter {
		return slowPoll
	}
	return fastPoll
}

// Run follows the local clipboard until ctx ends. It reacts to change events
// when the backend can watch, and otherwise polls with an adaptive interval.
func (s *Syncer) Run(ctx context.Context) {
	// Treat what is already on the clipboard at startup as seen, not as news.
	if v, err := s.Backend.Read(ctx); err == nil {
		s.mu.Lock()
		s.last = v
		s.mu.Unlock()
	}
	s.markActive()
	for ctx.Err() == nil {
		w, ok := s.Backend.(Watcher)
		if !ok {
			s.pollFor(ctx, 0)
			return
		}
		events, err := w.Watch(ctx)
		if errors.Is(err, ErrNoWatch) {
			s.pollFor(ctx, 0)
			return
		}
		if err == nil {
			s.follow(ctx, events)
		}
		// The watcher failed to start or died: poll for a while, then try it again.
		s.pollFor(ctx, s.rewatch)
	}
}

// follow polls once per change event until the channel closes.
func (s *Syncer) follow(ctx context.Context, events <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			s.poll(ctx)
		}
	}
}

// pollFor polls adaptively; for d > 0 it returns after d, else it runs until ctx ends.
func (s *Syncer) pollFor(ctx context.Context, d time.Duration) {
	var stop <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		stop = t.C
	}
	timer := time.NewTimer(fastPoll)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-timer.C:
			s.poll(ctx)
			timer.Reset(pollInterval(s.now().Sub(s.activeAt())))
		}
	}
}

func (s *Syncer) markActive() {
	s.mu.Lock()
	s.active = s.now()
	s.mu.Unlock()
}

func (s *Syncer) activeAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *Syncer) poll(ctx context.Context) {
	v, err := s.Backend.Read(ctx)
	if err != nil || v == "" || len(v) > MaxBytes {
		return
	}
	s.mu.Lock()
	if v == s.last {
		s.mu.Unlock()
		return
	}
	s.last = v
	ts := s.now().UnixMilli()
	s.lastTS = ts
	s.active = s.now()
	s.mu.Unlock()

	s.Out.Broadcast(protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: v, TS: ts})
}

// HandleMessage applies a clip received from a peer; it is the server's OnMessage hook.
func (s *Syncer) HandleMessage(ctx context.Context, peerID string, m protocol.Message) {
	if m.Type != protocol.TypeClip || m.Mime != "text/plain" || len(m.Data) > MaxBytes {
		return
	}
	s.mu.Lock()
	if m.Data == s.last || m.TS < s.lastTS {
		s.mu.Unlock()
		return
	}
	s.last = m.Data
	s.lastTS = m.TS
	s.active = s.now()
	s.mu.Unlock()

	_ = s.Backend.Write(ctx, m.Data)
}

// Manager keeps clipboard sync alive across desktop sessions: when the daemon
// starts before login (or no clipboard tool is usable yet) it keeps retrying
// until a backend appears, so no restart is ever needed.
type Manager struct {
	Out    Broadcaster
	Detect func() (Backend, error)
	Retry  time.Duration

	cur atomic.Pointer[Syncer]
}

func NewManager(out Broadcaster) *Manager {
	return &Manager{Out: out, Detect: Detect, Retry: 15 * time.Second}
}

// Run blocks until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for ctx.Err() == nil {
		b, err := m.Detect()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.Retry):
			}
			continue
		}
		s := NewSyncer(b, m.Out)
		m.cur.Store(s)
		s.Run(ctx)
	}
}

// HandleMessage forwards to the active syncer; clips arriving before a
// clipboard exists are dropped.
func (m *Manager) HandleMessage(ctx context.Context, peerID string, msg protocol.Message) {
	if s := m.cur.Load(); s != nil {
		s.HandleMessage(ctx, peerID, msg)
	}
}
