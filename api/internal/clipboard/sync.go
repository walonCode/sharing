package clipboard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"api/internal/protocol"
)

const (
	MaxBytes     = 1 << 20 // larger clips are ignored
	pollInterval = 400 * time.Millisecond
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
	now    func() time.Time
}

func NewSyncer(b Backend, out Broadcaster) *Syncer {
	return &Syncer{Backend: b, Out: out, now: time.Now}
}

// Run polls the local clipboard until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	// Treat what is already on the clipboard at startup as seen, not as news.
	if v, err := s.Backend.Read(ctx); err == nil {
		s.mu.Lock()
		s.last = v
		s.mu.Unlock()
	}
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.poll(ctx)
		}
	}
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
