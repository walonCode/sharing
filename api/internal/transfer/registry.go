package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"time"
)

const (
	KindFile = "file"
	KindDir  = "dir"

	OfferTTL = 10 * time.Minute
)

type Offer struct {
	ID      string
	PeerID  string
	Path    string
	Kind    string
	Size    int64
	Expires time.Time
}

// Registry holds offers waiting for the phone to download them. Offers are
// single use and expire.
type Registry struct {
	mu     sync.Mutex
	offers map[string]Offer
	now    func() time.Time
}

func NewRegistry() *Registry { return &Registry{offers: map[string]Offer{}, now: time.Now} }

func (r *Registry) Add(peerID, path, kind string, size int64) Offer {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	o := Offer{ID: hex.EncodeToString(b), PeerID: peerID, Path: path, Kind: kind, Size: size}
	r.mu.Lock()
	defer r.mu.Unlock()
	o.Expires = r.now().Add(OfferTTL)
	for id, old := range r.offers {
		if !r.now().Before(old.Expires) {
			delete(r.offers, id)
		}
	}
	r.offers[o.ID] = o
	return o
}

// Take removes and returns the offer if it exists, is unexpired, and belongs to peerID.
func (r *Registry) Take(id, peerID string) (Offer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.offers[id]
	if !ok || o.PeerID != peerID || !r.now().Before(o.Expires) {
		return Offer{}, false
	}
	delete(r.offers, id)
	return o, true
}

var ErrPeerOffline = errors.New("peer is offline")

func baseName(p string) string { return filepath.Base(filepath.Clean(p)) }
