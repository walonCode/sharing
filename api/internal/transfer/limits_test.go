package transfer

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"api/internal/config"
)

type noLenReader struct{ r io.Reader }

func (n noLenReader) Read(p []byte) (int, error) { return n.r.Read(p) }

func TestOversizeUploadsAreRefused(t *testing.T) {
	e := setup(t)
	e.settings(t, func(s *config.Settings) { s.MaxUploadBytes = 10 })

	// Declared length: refused before a byte is read.
	resp := e.do(t, "PUT", "/upload?name=big.bin", strings.NewReader(strings.Repeat("x", 11)))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize = %d", resp.StatusCode)
	}
	// Unknown length (chunked): enforced while streaming.
	resp = e.do(t, "PUT", "/upload?name=big2.bin", noLenReader{strings.NewReader(strings.Repeat("x", 50))})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed oversize = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/upload?name=ok.bin", strings.NewReader("0123456789")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("exactly at the limit = %d", resp.StatusCode)
	}
	entries, _ := os.ReadDir(filepath.Join(e.recv, "My Phone"))
	if len(entries) != 1 || entries[0].Name() != "ok.bin" {
		t.Fatalf("oversize uploads left files behind: %v", entries)
	}
}

func TestUnpackEntryAndByteCaps(t *testing.T) {
	mk := func(n int) *bytes.Buffer {
		var hs []tar.Header
		for i := 0; i < n; i++ {
			hs = append(hs, tar.Header{Typeflag: tar.TypeReg, Name: "d/" + strings.Repeat("f", i+1), Size: 4, Mode: 0o644})
		}
		return tarOf(t, hs...)
	}
	dest := t.TempDir()
	if _, err := UnpackLimited(mk(5), dest, "x", Limits{MaxEntries: 4}); !errors.Is(err, ErrTooManyEntries) {
		t.Fatalf("entries: %v", err)
	}
	if _, err := UnpackLimited(mk(3), dest, "x", Limits{MaxBytes: 11}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bytes: %v", err)
	}
	if left, _ := os.ReadDir(dest); len(left) != 0 {
		t.Fatalf("refused archives left %v", left)
	}
	if _, err := UnpackLimited(mk(3), dest, "x", Limits{MaxEntries: 3, MaxBytes: 12}); err != nil {
		t.Fatalf("at the limits: %v", err)
	}
}

func TestUnpackRejectsControlCharactersInNames(t *testing.T) {
	_, err := Unpack(tarOf(t, tar.Header{Typeflag: tar.TypeReg, Name: "evil\x1b[2J.txt", Size: 1, Mode: 0o644}), t.TempDir(), "x")
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
}

func fakeFree(t *testing.T, free uint64) {
	t.Helper()
	old := freeBytes
	freeBytes = func(string) (uint64, error) { return free, nil }
	t.Cleanup(func() { freeBytes = old })
}

func TestDiskFullIsReportedAs507(t *testing.T) {
	e := setup(t)
	fakeFree(t, 100)
	e.settings(t, func(s *config.Settings) { s.MinFreeBytes = 50 })
	if resp := e.do(t, "PUT", "/upload?name=a.bin", strings.NewReader(strings.Repeat("x", 60))); resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("60 bytes with 100 free and 50 reserved = %d, want 507", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/upload?name=a.bin", strings.NewReader(strings.Repeat("x", 40))); resp.StatusCode != http.StatusCreated {
		t.Fatalf("40 bytes = %d", resp.StatusCode)
	}
}

func TestSlotsLimitConcurrentTransfersPerPeer(t *testing.T) {
	s := newSlots()
	var rel []func()
	for i := 0; i < 2; i++ {
		r, ok := s.acquire("a", 2)
		if !ok {
			t.Fatal("slot refused under the limit")
		}
		rel = append(rel, r)
	}
	if _, ok := s.acquire("a", 2); ok {
		t.Fatal("third slot must be refused")
	}
	if r, ok := s.acquire("b", 2); !ok {
		t.Fatal("another peer has its own slots")
	} else {
		r()
	}
	rel[0]()
	rel[0]() // releasing twice must not free a second slot
	if _, ok := s.acquire("a", 2); !ok {
		t.Fatal("slot not freed")
	}
	if _, ok := s.acquire("a", 2); ok {
		t.Fatal("double release freed too many slots")
	}
	if _, ok := s.acquire("a", 0); !ok {
		t.Fatal("limit 0 means unlimited")
	}
}

func TestSweepPartialsKeepsLiveSessions(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-100 * time.Hour)
	mk := func(rel string, mt time.Time) string {
		p := filepath.Join(root, rel)
		write(t, p, "x")
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk("Phone/.partial-old", old)
	keep := mk("Phone/.partial-live", old)
	fresh := mk("Phone/.partial-new", time.Now())
	normal := mk("Phone/photo.jpg", old)
	top := mk(".partial-top", old)

	n := SweepPartials(root, time.Now().Add(-72*time.Hour), map[string]bool{".partial-live": true})
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for _, p := range []string{stale, top} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s should be gone", p)
		}
	}
	for _, p := range []string{keep, fresh, normal} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive: %v", p, err)
		}
	}
}

func TestV1PutStillWorksAndIsLimited(t *testing.T) {
	e := setup(t)
	if resp := e.do(t, "PUT", "/upload?name=old.txt", strings.NewReader("legacy")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("v1 PUT = %d", resp.StatusCode)
	}
}
