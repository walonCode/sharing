package config

import (
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
