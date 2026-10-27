package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"api/internal/protocol"
)

// Send registers path as an offer for the peer. When the phone is connected it
// is told right away; otherwise the offer stays queued and is delivered the
// moment the phone's websocket connects (Replay).
func (s *Service) Send(peerID, path string) (Offer, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Offer{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Offer{}, err
	}
	o := Offer{PeerID: peerID, Path: resolved, Kind: KindFile, Size: info.Size(), MTimeNs: info.ModTime().UnixNano()}
	switch {
	case info.IsDir():
		set, _ := s.limits()
		m, err := BuildManifest(resolved, set.MaxEntries)
		if err != nil {
			return Offer{}, err
		}
		o.Kind, o.Manifest, o.Size = KindDir, &m, m.TotalSize()
	case !info.Mode().IsRegular():
		return Offer{}, fmt.Errorf("%s is not a regular file or folder", path)
	}
	o.ETag = fmt.Sprintf(`"%d-%d"`, o.Size, o.MTimeNs)
	o = s.Registry.Put(o)
	s.deliver(o)
	go s.hashOffer(o)
	cur, _ := s.Registry.Get(o.ID, "")
	return cur, nil
}

func offerMessage(o Offer) protocol.Message {
	m := protocol.Message{Type: protocol.TypeOffer, ID: o.ID, Kind: o.Kind, Name: baseName(o.Path), Size: o.Size}
	if o.Manifest != nil {
		m.Count = len(o.Manifest.Files)
	}
	return m
}

// deliver tells the phone about a queued offer and marks it pending on success.
func (s *Service) deliver(o Offer) {
	if err := s.Srv.Hub.Send(o.PeerID, offerMessage(o)); err == nil && o.State == StateQueued {
		s.Registry.Update(o.ID, true, func(x *Offer) { x.State = StatePending })
	}
}

// Replay re-sends every live offer of a peer that has just connected. The
// phone ignores ids it already knows.
func (s *Service) Replay(peerID string) {
	offers := s.Registry.List(peerID, true)
	slices.SortFunc(offers, func(a, b Offer) int { return a.Created.Compare(b.Created) })
	for _, o := range offers {
		if o.State == StateActive {
			continue // the phone is mid-download and will resume it by itself
		}
		s.deliver(o)
	}
}

// finish ends an offer with the given protocol status.
func (s *Service) finish(id, status, errMsg string) {
	state := StateDone
	switch status {
	case protocol.StatusFailed:
		state = StateFailed
	case protocol.StatusCancelled:
		state = StateCancelled
	}
	s.Registry.Update(id, true, func(o *Offer) {
		if !o.Live() {
			return
		}
		o.State, o.Error = state, errMsg
		if state == StateDone {
			o.Offset = o.Size
		}
	})
}

// HandleMessage processes ack and progress messages from a phone.
func (s *Service) HandleMessage(peerID string, m protocol.Message) {
	switch m.Type {
	case protocol.TypeAck:
		if o, ok := s.Registry.Get(m.ID, peerID); ok {
			s.finish(o.ID, m.Status, m.Error)
		}
	case protocol.TypeProgress:
		if _, ok := s.Registry.Get(m.ID, peerID); ok {
			s.Registry.Update(m.ID, false, func(o *Offer) {
				o.Offset = min(max(m.Offset, 0), o.Size)
				o.rate.sample(time.Now(), o.Offset)
			})
		}
	}
}

// --- control API views ---

// Info is one row of `sharingd transfers`.
type Info struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"` // "send" (laptop to phone) or "receive"
	PeerID    string    `json:"peerId"`
	Peer      string    `json:"peer"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	Size      int64     `json:"size"`
	Offset    int64     `json:"offset"`
	BytesPerS float64   `json:"bytesPerSecond"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error,omitempty"`
	Updated   time.Time `json:"updated"`
}

func (s *Service) peerName(id string) string {
	for _, p := range s.Srv.Store.Peers() {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}

func (s *Service) offerInfo(o Offer) Info {
	return Info{ID: o.ID, Direction: "send", PeerID: o.PeerID, Peer: s.peerName(o.PeerID), Name: baseName(o.Path),
		Kind: o.Kind, State: o.State, Size: o.Size, Offset: o.Offset, BytesPerS: o.rate.bps,
		Attempts: o.Attempts, Error: o.Error, Updated: o.Updated}
}

func (s *Service) uploadInfo(u Upload) Info {
	state := "active"
	if u.Done {
		state = StateDone
	}
	off := s.Uploads.Offset(&u)
	name := u.Name
	if u.RelPath != "" {
		name = u.RelPath
	}
	return Info{ID: u.ID, Direction: "receive", PeerID: u.PeerID, Peer: s.peerName(u.PeerID), Name: name,
		Kind: u.Kind, State: state, Size: u.Size, Offset: off, BytesPerS: u.rate.bps,
		Attempts: u.Attempts, Error: u.Error, Updated: u.Activity}
}

// List returns every transfer in both directions, newest first. Files that
// belong to a folder upload are folded into their folder.
func (s *Service) List() []Info {
	out := []Info{}
	for _, o := range s.Registry.List("", false) {
		out = append(out, s.offerInfo(o))
	}
	for _, u := range s.Uploads.List() {
		if u.DirID == "" {
			out = append(out, s.uploadInfo(u))
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return b.Updated.Compare(a.Updated) })
	return out
}

// Get finds a transfer by id or unique id prefix.
func (s *Service) Get(id string) (Info, bool) {
	var hits []Info
	for _, t := range s.List() {
		if t.ID == id {
			return t, true
		}
		if id != "" && strings.HasPrefix(t.ID, id) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return Info{}, false
}

var ErrNoTransfer = errors.New("no such transfer")

// Cancel stops an offer, or deletes an unfinished upload session.
func (s *Service) Cancel(id string) error {
	t, ok := s.Get(id)
	if !ok {
		return ErrNoTransfer
	}
	if t.Direction == "receive" {
		if up, ok := s.Uploads.get(t.ID, ""); ok {
			s.Uploads.remove(up)
		}
		return nil
	}
	s.finish(t.ID, protocol.StatusCancelled, "cancelled on the laptop")
	_ = s.Srv.Hub.Send(t.PeerID, protocol.Message{Type: protocol.TypeCancel, ID: t.ID})
	return nil
}

// Retry re-queues a failed or cancelled offer. Uploads are retried by the phone.
func (s *Service) Retry(id string) error {
	t, ok := s.Get(id)
	if !ok {
		return ErrNoTransfer
	}
	if t.Direction == "receive" {
		return errors.New("uploads are retried by the phone; they resume by themselves")
	}
	o, ok := s.Registry.Get(t.ID, "")
	if !ok || o.Live() {
		return errors.New("transfer is still in progress")
	}
	if _, err := os.Stat(o.Path); err != nil {
		return fmt.Errorf("%s is gone", o.Path)
	}
	o, _ = s.Registry.Update(o.ID, true, func(x *Offer) {
		x.State, x.Error, x.Offset, x.Attempts = StateQueued, "", 0, 0
		x.Created = time.Now()
	})
	s.deliver(o)
	return nil
}
