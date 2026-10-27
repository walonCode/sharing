package pairing

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestRedeemSingleUseAndExpiry(t *testing.T) {
	m := NewManager()
	tok := m.NewInvite(time.Minute)
	if !m.Redeem(tok) || m.Redeem(tok) {
		t.Fatal("token must redeem exactly once")
	}

	now := time.Now()
	m.now = func() time.Time { return now }
	tok = m.NewInvite(time.Minute)
	m.now = func() time.Time { return now.Add(2 * time.Minute) }
	if m.Redeem(tok) {
		t.Fatal("expired token redeemed")
	}
}

func TestTokenMatches(t *testing.T) {
	tok := NewToken()
	if !TokenMatches(tok, HashToken(tok)) || TokenMatches("other", HashToken(tok)) {
		t.Fatal("token matching broken")
	}
}

func TestInviteURL(t *testing.T) {
	u, err := url.Parse(Invite{Host: "192.168.1.5", Port: 53317, Fingerprint: "ab", Token: "t", Name: "my laptop"}.URL())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "sharing" || q.Get("host") != "192.168.1.5" || q.Get("port") != "53317" || q.Get("fp") != "ab" || q.Get("name") != "my laptop" {
		t.Fatalf("url = %s", u)
	}
}

func TestConfirmationAcceptReject(t *testing.T) {
	m := NewManager()
	for _, accept := range []bool{true, false} {
		res := make(chan bool, 1)
		go func() { res <- m.Ask(context.Background(), "device-0001", "Pixel") }()
		var id string
		for i := 0; i < 200 && id == ""; i++ {
			if p := m.Pending(); len(p) > 0 {
				id = p[0].ID
			}
			time.Sleep(5 * time.Millisecond)
		}
		if id == "" {
			t.Fatal("request never became pending")
		}
		if !m.Decide(id, accept) {
			t.Fatal("decide failed")
		}
		if got := <-res; got != accept {
			t.Fatalf("Ask = %v, want %v", got, accept)
		}
		if len(m.Pending()) != 0 || m.Decide(id, true) {
			t.Fatal("answered request must be gone")
		}
	}
}

func TestAskEndsWhenContextEnds(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { res <- m.Ask(ctx, "device-0001", "x") }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if <-res {
		t.Fatal("a cancelled request must not be accepted")
	}
}

func TestRepeatRedeemBySameDeviceWithinWindow(t *testing.T) {
	m := NewManager()
	now := time.Now()
	m.now = func() time.Time { return now }
	tok := m.NewInviteConfirm(time.Minute, true)
	if confirm, ok := m.RedeemFor(tok, "device-0001"); !ok || !confirm {
		t.Fatalf("first redeem = %v %v", confirm, ok)
	}
	m.MarkIssued(tok, "device-0001")
	if _, ok := m.RedeemFor(tok, "device-9999"); ok {
		t.Fatal("another device must not reuse the invite")
	}
	if confirm, ok := m.RedeemFor(tok, "device-0001"); !ok || confirm {
		t.Fatalf("retry by the same device = %v %v (want ok, no confirmation)", confirm, ok)
	}
	now = now.Add(RetryWindow + time.Second)
	if _, ok := m.RedeemFor(tok, "device-0001"); ok {
		t.Fatal("retry window must close")
	}
}
