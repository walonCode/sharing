package clipboard

import (
	"context"
	"testing"
)

// BenchmarkExecRead is the cost of one clipboard poll through an external
// tool; "true" stands in for wl-paste/xclip, so this is mostly fork+exec.
func BenchmarkExecRead(b *testing.B) {
	be := execBackend{read: []string{"true"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := be.Read(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExecReadHugeClipboard reads a 200 MB clipboard through the backend.
func BenchmarkExecReadHugeClipboard(b *testing.B) {
	be := execBackend{read: []string{"sh", "-c", "head -c 209715200 /dev/zero"}}
	b.ReportAllocs()
	for b.Loop() {
		v, err := be.Read(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		_ = v
	}
}

// BenchmarkPollUnchanged is the in-process cost of one idle poll with a fake backend.
func BenchmarkPollUnchanged(b *testing.B) {
	s, be, _ := newTest()
	be.set("steady")
	s.poll(context.Background())
	b.ReportAllocs()
	for b.Loop() {
		s.poll(context.Background())
	}
}
