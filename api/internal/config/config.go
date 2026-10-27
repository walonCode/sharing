// Package config persists daemon settings, paired peers, and the TLS identity.
package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DefaultPort = 53317

type Settings struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	ReceiveDir string `json:"receiveDir"`
	Port       int    `json:"port"`

	// Limits protect the laptop from a buggy or misbehaving paired peer.
	// Keys missing from settings.json keep these defaults; an explicit 0 in
	// MaxUploadBytes means "limited only by free disk space".
	MaxUploadBytes int64 `json:"maxUploadBytes"`
	MaxEntries     int   `json:"maxEntries"`
	MaxTransfers   int   `json:"maxTransfersPerPeer"`
	MinFreeBytes   int64 `json:"minFreeBytes"`
	// MaxPartialBytes caps the declared size of one peer's unfinished uploads together.
	MaxPartialBytes int64 `json:"maxPartialBytesPerPeer"`

	// ConfirmPairing makes `sharingd pair` ask on the terminal before a phone is accepted.
	ConfirmPairing bool `json:"confirmPairing"`
}

// Default limits.
const (
	DefaultMaxUploadBytes = 64 << 30
	DefaultMaxEntries     = 200_000
	DefaultMaxTransfers   = 4
	DefaultMinFreeBytes   = 1 << 30
	DefaultMaxPartial     = 256 << 30
)

// RotationGrace is how long a rotated-out device token keeps working.
const RotationGrace = 60 * time.Second

type Peer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	PairedAt  time.Time `json:"pairedAt"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`

	// PrevTokenHash stays valid until PrevExpires after a token rotation.
	PrevTokenHash string    `json:"prevTokenHash,omitempty"`
	PrevExpires   time.Time `json:"prevExpires,omitzero"`
}

// Matches reports whether hash is this peer's current token hash, or the
// previous one while its grace period lasts.
func (p Peer) Matches(hash string, now time.Time) bool {
	if subtle.ConstantTimeCompare([]byte(hash), []byte(p.TokenHash)) == 1 {
		return true
	}
	return p.PrevTokenHash != "" && now.Before(p.PrevExpires) &&
		subtle.ConstantTimeCompare([]byte(hash), []byte(p.PrevTokenHash)) == 1
}

// Store is safe for concurrent use.
type Store struct {
	dir string

	mu       sync.RWMutex
	settings Settings
	peers    map[string]Peer
	lastSave map[string]time.Time // last time a peer's lastSeen reached disk
}

// DefaultDir returns ~/.config/sharing (honouring XDG_CONFIG_HOME).
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sharing"), nil
}

// Open loads the store from dir, creating defaults on first run.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, peers: map[string]Peer{}, lastSave: map[string]time.Time{}}
	s.settings = Settings{
		MaxUploadBytes:  DefaultMaxUploadBytes,
		MaxEntries:      DefaultMaxEntries,
		MaxTransfers:    DefaultMaxTransfers,
		MinFreeBytes:    DefaultMinFreeBytes,
		MaxPartialBytes: DefaultMaxPartial,
		ConfirmPairing:  true,
	}

	if err := readJSON(filepath.Join(dir, "settings.json"), &s.settings); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		host, _ := os.Hostname()
		home, _ := os.UserHomeDir()
		s.settings.DeviceID = randomID()
		s.settings.Name = host
		s.settings.ReceiveDir = filepath.Join(home, "Downloads", "Sharing")
		s.settings.Port = DefaultPort
		if err := s.saveSettings(); err != nil {
			return nil, err
		}
	}

	var peers []Peer
	if err := readJSON(filepath.Join(dir, "peers.json"), &peers); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, p := range peers {
		s.peers[p.ID] = p
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Store) UpdateSettings(fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.settings)
	return s.saveSettings()
}

func (s *Store) Peers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	return out
}

// AddPeer stores or replaces a peer (re-pairing the same device ID rotates its token).
func (s *Store) AddPeer(p Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[p.ID] = p
	return s.savePeers()
}

// TouchPeer records that the peer was just seen. The timestamp is kept in
// memory and written to disk at most once a minute per peer.
func (s *Store) TouchPeer(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	if !ok {
		return
	}
	p.LastSeen = now
	s.peers[id] = p
	if now.Sub(s.lastSave[id]) >= time.Minute {
		s.lastSave[id] = now
		_ = s.savePeers()
	}
}

// RotatePeer replaces the peer's token hash; the old one keeps working for RotationGrace.
func (s *Store) RotatePeer(id, newHash string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	if !ok {
		return ErrNoPeer
	}
	p.PrevTokenHash, p.PrevExpires, p.TokenHash = p.TokenHash, now.Add(RotationGrace), newHash
	s.peers[id] = p
	return s.savePeers()
}

var ErrNoPeer = errors.New("no such peer")

func (s *Store) RemovePeer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, id)
	return s.savePeers()
}

func (s *Store) saveSettings() error {
	return writeJSON(filepath.Join(s.dir, "settings.json"), s.settings)
}

func (s *Store) savePeers() error {
	list := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		list = append(list, p)
	}
	return writeJSON(filepath.Join(s.dir, "peers.json"), list)
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes atomically so a crash never leaves a truncated file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
