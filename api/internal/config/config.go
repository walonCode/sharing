// Package config persists daemon settings, paired peers, and the TLS identity.
package config

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

const DefaultPort = 53317

type Settings struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	ReceiveDir string `json:"receiveDir"`
	Port       int    `json:"port"`
}

type Peer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	PairedAt  time.Time `json:"pairedAt"`
}

// Store is safe for concurrent use.
type Store struct {
	dir string

	mu       sync.RWMutex
	settings Settings
	peers    map[string]Peer
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
	s := &Store{dir: dir, peers: map[string]Peer{}}

	if err := readJSON(filepath.Join(dir, "settings.json"), &s.settings); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		host, _ := os.Hostname()
		home, _ := os.UserHomeDir()
		s.settings = Settings{
			DeviceID:   randomID(),
			Name:       host,
			ReceiveDir: filepath.Join(home, "Downloads", "Sharing"),
			Port:       DefaultPort,
		}
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
