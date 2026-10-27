package clipboard

import (
	"context"
	"testing"
	"time"
)

// Round-trips through the real clipboard tool; skipped without a session.
// It restores the previous clipboard text afterwards.
func TestDetectedBackendRoundTrip(t *testing.T) {
	b, err := Detect()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prev, err := b.Read(ctx)
	if err != nil {
		t.Skipf("no usable clipboard session: %v", err)
	}
	defer func() { _ = b.Write(ctx, prev) }()
	if err := b.Write(ctx, "sharing-test ✓"); err != nil {
		t.Skipf("cannot write clipboard: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got, _ := b.Read(ctx); got != "sharing-test ✓" {
		t.Fatalf("got %q", got)
	}
}
