package discovery

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTXTCarriesFingerprintPrefixOnly(t *testing.T) {
	txt := strings.Join(Info{ID: "i", Name: "n", Fingerprint: strings.Repeat("a", 64)}.txt(), "|")
	if !strings.Contains(txt, "fp="+strings.Repeat("a", 16)) || strings.Contains(txt, strings.Repeat("a", 17)) {
		t.Fatalf("txt = %s", txt)
	}
}

func TestAdvertiseThenBrowse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info := Info{ID: "test-" + time.Now().Format("150405.000"), Name: "laptop", Fingerprint: "deadbeefdeadbeefcafe", Port: 45678}
	adCtx, stopAd := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- Advertise(adCtx, info) }()

	got, host, err := Browse(ctx, func(i Info) bool { return i.ID == info.ID })
	if err != nil {
		t.Skipf("mDNS not available in this environment: %v", err)
	}
	if got.Name != "laptop" || got.Port != 45678 || got.Fingerprint != "deadbeefdeadbeef" || host == "" {
		t.Fatalf("browse = %+v host=%q", got, host)
	}
	stopAd()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReannouncesOnAddressChange(t *testing.T) {
	var addr atomic.Value
	addr.Store("a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info := Info{ID: "re-" + time.Now().Format("150405.000"), Name: "x", Port: 45679}
	done := make(chan error, 1)
	go func() {
		done <- advertise(ctx, info, 20*time.Millisecond, func() string { return addr.Load().(string) })
	}()
	time.Sleep(100 * time.Millisecond)
	addr.Store("b") // simulate a network change; must not error or deadlock
	time.Sleep(150 * time.Millisecond)
	addr.Store("") // network gone
	time.Sleep(100 * time.Millisecond)
	addr.Store("c")
	time.Sleep(150 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
