// Package pairing issues one-time invites and long-term peer tokens.
package pairing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const InviteTTL = 5 * time.Minute

// RetryWindow is how long a repeated redemption of an invite by the device
// that already used it issues a fresh token (the first response may be lost).
const RetryWindow = 2 * time.Minute

// ConfirmTimeout bounds how long a phone waits for the laptop user to accept.
const ConfirmTimeout = 90 * time.Second

type invite struct {
	exp     time.Time
	confirm bool
}

type issued struct {
	deviceID string
	until    time.Time
}

// Request is a phone waiting for the laptop user to accept or reject it.
type Request struct {
	ID       string `json:"id"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`

	reply chan bool
}

// Manager tracks outstanding invites. Tokens are single use.
type Manager struct {
	mu      sync.Mutex
	invites map[string]invite // token hash -> invite
	issued  map[string]issued // token hash -> device that redeemed it
	pending map[string]*Request
	now     func() time.Time
}

func NewManager() *Manager {
	return &Manager{
		invites: map[string]invite{},
		issued:  map[string]issued{},
		pending: map[string]*Request{},
		now:     time.Now,
	}
}

// NewInvite returns a fresh token valid for ttl that needs no confirmation.
func (m *Manager) NewInvite(ttl time.Duration) string { return m.NewInviteConfirm(ttl, false) }

// NewInviteConfirm is NewInvite, optionally requiring the laptop user to accept the phone.
func (m *Manager) NewInviteConfirm(ttl time.Duration, confirm bool) string {
	token := NewToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gc()
	m.invites[HashToken(token)] = invite{exp: m.now().Add(ttl), confirm: confirm}
	return token
}

// Redeem consumes the token and reports whether it was valid and unexpired.
func (m *Manager) Redeem(token string) bool {
	_, ok := m.RedeemFor(token, "")
	return ok
}

// RedeemFor consumes the token for deviceID. needConfirm reports whether the
// laptop user must accept the device. A repeat by the same device within
// RetryWindow succeeds again without confirmation.
func (m *Manager) RedeemFor(token, deviceID string) (needConfirm, ok bool) {
	h := HashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, found := m.invites[h]
	delete(m.invites, h)
	if found && m.now().Before(inv.exp) {
		return inv.confirm, true
	}
	if rec, seen := m.issued[h]; seen && deviceID != "" && rec.deviceID == deviceID && m.now().Before(rec.until) {
		return false, true
	}
	return false, false
}

// MarkIssued remembers that deviceID received a token for this invite.
func (m *Manager) MarkIssued(token, deviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.issued[HashToken(token)] = issued{deviceID: deviceID, until: m.now().Add(RetryWindow)}
}

// Ask registers a pending request and blocks until Decide answers it, the
// timeout passes, or ctx ends. Only an explicit accept returns true.
func (m *Manager) Ask(ctx context.Context, deviceID, name string) bool {
	req := &Request{ID: NewToken()[:12], DeviceID: deviceID, Name: name, reply: make(chan bool, 1)}
	m.mu.Lock()
	m.pending[req.ID] = req
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
	}()
	select {
	case ok := <-req.reply:
		return ok
	case <-time.After(ConfirmTimeout):
		return false
	case <-ctx.Done():
		return false
	}
}

// Pending lists the requests awaiting a decision.
func (m *Manager) Pending() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Request, 0, len(m.pending))
	for _, r := range m.pending {
		out = append(out, Request{ID: r.ID, DeviceID: r.DeviceID, Name: r.Name})
	}
	return out
}

// Decide answers a pending request; false if it is gone.
func (m *Manager) Decide(id string, accept bool) bool {
	m.mu.Lock()
	r, ok := m.pending[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case r.reply <- accept:
	default:
	}
	return true
}

func (m *Manager) gc() {
	now := m.now()
	for h, inv := range m.invites {
		if !now.Before(inv.exp) {
			delete(m.invites, h)
		}
	}
	for h, rec := range m.issued {
		if !now.Before(rec.until) {
			delete(m.issued, h)
		}
	}
}

// NewToken returns 32 random bytes, base64url encoded.
func NewToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches compares a presented token with a stored hash in constant time.
func TokenMatches(token, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(hash)) == 1
}

// Invite is what the phone needs to reach and trust the laptop.
type Invite struct {
	Host        string
	Port        int
	Fingerprint string
	Token       string
	Name        string
}

func (i Invite) URL() string {
	q := url.Values{}
	q.Set("host", i.Host)
	q.Set("port", strconv.Itoa(i.Port))
	q.Set("fp", i.Fingerprint)
	q.Set("token", i.Token)
	q.Set("name", i.Name)
	return "sharing://pair?" + q.Encode()
}
