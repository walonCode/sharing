package pairing

import (
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
