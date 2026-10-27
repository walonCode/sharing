package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	KindFile = "file"
	KindDir  = "dir"

	// Offer states. An offer is queued until the phone has been told about it,
	// pending until the phone starts downloading, active while it does, and
	// finally done, failed or cancelled.
	StateQueued    = "queued"
	StatePending   = "pending"
	StateActive    = "active"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"

	// IdleTTL removes a delivered offer the phone has not touched for this long.
	IdleTTL = 30 * time.Minute
	// MaxAge removes any offer this long after it was created.
	MaxAge = 24 * time.Hour
	// FinishedTTL keeps finished offers visible in `sharingd transfers`.
	FinishedTTL = time.Hour
)

// Offer is a file or folder the laptop offers to a phone. Size, MTimeNs and the
// manifest are frozen when the offer is made so a download can be resumed
// against exactly the bytes that were promised.
type Offer struct {
	ID      string    `json:"id"`
	PeerID  string    `json:"peerId"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	MTimeNs int64     `json:"mtimeNs"`
	ETag    string    `json:"etag"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`

	Offset   int64  `json:"offset"` // bytes the phone reported downloading
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`

	Manifest *Manifest `json:"manifest,omitempty"`
	// SHA256 is the hex digest of a file offer; FileSums holds one per manifest
	// file. Both are empty until the background hash finishes.
	SHA256   string   `json:"sha256,omitempty"`
	FileSums []string `json:"fileSums,omitempty"`

	rate rate
}

// Live reports whether the phone may still download the offer.
func (o Offer) Live() bool {
	return o.State == StateQueued || o.State == StatePending || o.State == StateActive
}

// Registry holds offers. It is persisted to a JSON file (mode 0600) so offers
// survive a daemon restart.
type Registry struct {
	mu     sync.Mutex
	offers map[string]*Offer
	path   string // "" keeps the registry in memory only
	now    func() time.Time
}

func NewRegistry() *Registry { return &Registry{offers: map[string]*Offer{}, now: time.Now} }

// OpenRegistry loads path (if present) and persists later changes to it.
// Entries whose file or folder no longer exists, and expired ones, are dropped.
func OpenRegistry(path string) (*Registry, error) {
	r := NewRegistry()
	r.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Offer
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, o := range list {
		if _, err := os.Stat(o.Path); err != nil && o.Live() {
			continue
		}
		if !r.expired(o) {
			r.offers[o.ID] = o
		}
	}
	return r, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Registry) expired(o *Offer) bool {
	now := r.now()
	switch {
	case now.Sub(o.Created) > MaxAge:
		return true
	case !o.Live():
		return now.Sub(o.Updated) > FinishedTTL
	case o.State == StateQueued:
		return false
	default:
		return now.Sub(o.Updated) > IdleTTL
	}
}

// Put stores a new offer (its ID, times and state are filled in) and returns it.
func (r *Registry) Put(o Offer) Offer {
	r.mu.Lock()
	defer r.mu.Unlock()
	o.ID = newID()
	o.Created = r.now()
	o.Updated = o.Created
	if o.State == "" {
		o.State = StateQueued
	}
	r.offers[o.ID] = &o
	r.saveLocked()
	return o
}

// Get returns the live-or-finished offer if it exists, is unexpired, and
// belongs to peerID (peerID "" skips the ownership check, for the control API).
func (r *Registry) Get(id, peerID string) (Offer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.offers[id]
	if !ok || (peerID != "" && o.PeerID != peerID) {
		return Offer{}, false
	}
	if r.expired(o) {
		delete(r.offers, id)
		r.saveLocked()
		return Offer{}, false
	}
	return *o, true
}

// Update applies fn to the offer. persist writes the registry to disk; frequent
// updates (progress) pass false.
func (r *Registry) Update(id string, persist bool, fn func(*Offer)) (Offer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.offers[id]
	if !ok {
		return Offer{}, false
	}
	fn(o)
	o.Updated = r.now()
	if persist {
		r.saveLocked()
	}
	return *o, true
}

// List returns all unexpired offers, optionally only a peer's live ones.
func (r *Registry) List(peerID string, liveOnly bool) []Offer {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Offer
	for id, o := range r.offers {
		if r.expired(o) {
			delete(r.offers, id)
			continue
		}
		if (peerID == "" || o.PeerID == peerID) && (!liveOnly || o.Live()) {
			out = append(out, *o)
		}
	}
	return out
}

// RemovePeer deletes every offer of a peer (unpair).
func (r *Registry) RemovePeer(peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, o := range r.offers {
		if o.PeerID == peerID {
			delete(r.offers, id)
		}
	}
	r.saveLocked()
}

// GC drops expired offers; the service calls it on a timer.
func (r *Registry) GC() {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.offers)
	for id, o := range r.offers {
		if r.expired(o) {
			delete(r.offers, id)
		}
	}
	if len(r.offers) != n {
		r.saveLocked()
	}
}

// saveLocked writes atomically; a failure only costs persistence, never the live offer.
func (r *Registry) saveLocked() {
	if r.path == "" {
		return
	}
	list := make([]*Offer, 0, len(r.offers))
	for _, o := range r.offers {
		list = append(list, o)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		_ = os.Remove(tmp)
	}
}

var (
	ErrPeerOffline = errors.New("peer is offline")
	ErrBusy        = errors.New("peer connection is busy; try again")
)

func baseName(p string) string { return filepath.Base(filepath.Clean(p)) }

// rate is a smoothed bytes-per-second estimate from offset samples.
type rate struct {
	at     time.Time
	offset int64
	bps    float64
}

func (r *rate) sample(now time.Time, offset int64) {
	if !r.at.IsZero() && now.After(r.at) && offset >= r.offset {
		inst := float64(offset-r.offset) / now.Sub(r.at).Seconds()
		if r.bps == 0 {
			r.bps = inst
		} else {
			r.bps = 0.7*r.bps + 0.3*inst
		}
	}
	r.at, r.offset = now, offset
}
