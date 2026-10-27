package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsAndPeersPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Settings().DeviceID
	if id == "" || s.Settings().Port != DefaultPort {
		t.Fatalf("defaults = %+v", s.Settings())
	}
	if err := s.UpdateSettings(func(x *Settings) { x.ReceiveDir = filepath.Join(dir, "in") }); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPeer(Peer{ID: "p", Name: "phone", TokenHash: "h", PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Settings().DeviceID != id || s2.Settings().ReceiveDir != filepath.Join(dir, "in") {
		t.Fatalf("settings not persisted: %+v", s2.Settings())
	}
	if len(s2.Peers()) != 1 {
		t.Fatalf("peers = %+v", s2.Peers())
	}
	if err := s2.RemovePeer("p"); err != nil || len(s2.Peers()) != 0 {
		t.Fatalf("remove failed: %v", err)
	}
}

func TestCertificateIsStable(t *testing.T) {
	s, _ := Open(t.TempDir())
	c1, err := s.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(c1) != Fingerprint(c2) || len(Fingerprint(c1)) != 64 {
		t.Fatal("certificate must persist across loads")
	}
}

func TestLimitDefaultsAndExplicitZero(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	set := s.Settings()
	if set.MaxUploadBytes != DefaultMaxUploadBytes || set.MaxEntries != 200_000 || set.MaxTransfers != 4 ||
		set.MinFreeBytes != 1<<30 || !set.ConfirmPairing {
		t.Fatalf("defaults = %+v", set)
	}
	// An old settings.json without the new keys keeps the defaults; an explicit 0 sticks.
	old := `{"deviceId":"d","name":"n","receiveDir":"/r","port":1,"maxUploadBytes":0}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	set = s2.Settings()
	if set.MaxUploadBytes != 0 || set.MaxEntries != 200_000 || !set.ConfirmPairing {
		t.Fatalf("migrated = %+v", set)
	}
}

func TestTouchPeerWritesAtMostOncePerMinute(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.AddPeer(Peer{ID: "p", Name: "n", TokenHash: "h"})
	t0 := time.Now()
	s.TouchPeer("p", t0)
	s.TouchPeer("p", t0.Add(10*time.Second))
	reopened, _ := Open(dir)
	if got := reopened.Peers()[0].LastSeen; !got.Equal(t0) {
		t.Fatalf("persisted lastSeen = %v, want the first touch %v", got, t0)
	}
	if got := s.Peers()[0].LastSeen; !got.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("in-memory lastSeen = %v", got)
	}
	s.TouchPeer("p", t0.Add(61*time.Second))
	reopened, _ = Open(dir)
	if got := reopened.Peers()[0].LastSeen; !got.Equal(t0.Add(61 * time.Second)) {
		t.Fatalf("lastSeen after a minute = %v", got)
	}
	s.TouchPeer("unknown", t0) // must not create a peer
	if len(s.Peers()) != 1 {
		t.Fatal("touching an unknown peer created it")
	}
}

func TestRotatePeerKeepsOldHashForTheGracePeriod(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.AddPeer(Peer{ID: "p", TokenHash: "old"})
	now := time.Now()
	if err := s.RotatePeer("p", "new", now); err != nil {
		t.Fatal(err)
	}
	p := s.Peers()[0]
	if !p.Matches("new", now) || !p.Matches("old", now.Add(59*time.Second)) || p.Matches("old", now.Add(61*time.Second)) || p.Matches("other", now) {
		t.Fatalf("matching wrong: %+v", p)
	}
	if err := s.RotatePeer("nobody", "x", now); err != ErrNoPeer {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenIndexFollowsPeerChanges(t *testing.T) {
	s, _ := Open(t.TempDir())
	now := time.Now()
	_ = s.AddPeer(Peer{ID: "a", TokenHash: "ha"})
	_ = s.AddPeer(Peer{ID: "b", TokenHash: "hb"})
	if p, ok := s.PeerByTokenHash("hb", now); !ok || p.ID != "b" {
		t.Fatalf("lookup b: %v %v", p, ok)
	}
	_ = s.RotatePeer("a", "ha2", now)
	for _, h := range []string{"ha", "ha2"} {
		if p, ok := s.PeerByTokenHash(h, now); !ok || p.ID != "a" {
			t.Fatalf("lookup %s during grace: %v %v", h, p, ok)
		}
	}
	if _, ok := s.PeerByTokenHash("ha", now.Add(2*RotationGrace)); ok {
		t.Fatal("old hash must stop matching after the grace period")
	}
	_ = s.RemovePeer("b")
	if _, ok := s.PeerByTokenHash("hb", now); ok {
		t.Fatal("removed peer still authenticates")
	}
	// Re-pairing the same device replaces its hash.
	_ = s.AddPeer(Peer{ID: "a", TokenHash: "ha3"})
	if _, ok := s.PeerByTokenHash("ha2", now); ok {
		t.Fatal("replaced token still authenticates")
	}
	reopened, _ := Open(s.Dir())
	if p, ok := reopened.PeerByTokenHash("ha3", now); !ok || p.ID != "a" {
		t.Fatal("index not rebuilt on open")
	}
}
