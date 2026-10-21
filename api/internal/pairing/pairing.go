// Package pairing issues one-time invites and long-term peer tokens.
package pairing

import (
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

// Manager tracks outstanding invites. Tokens are single use.
type Manager struct {
	mu      sync.Mutex
	invites map[string]time.Time // token hash -> expiry
	now     func() time.Time
}

func NewManager() *Manager {
	return &Manager{invites: map[string]time.Time{}, now: time.Now}
}

// NewInvite returns a fresh token valid for ttl.
func (m *Manager) NewInvite(ttl time.Duration) string {
	token := NewToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gc()
	m.invites[HashToken(token)] = m.now().Add(ttl)
	return token
}

// Redeem consumes the token and reports whether it was valid and unexpired.
func (m *Manager) Redeem(token string) bool {
	h := HashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.invites[h]
	delete(m.invites, h)
	return ok && m.now().Before(exp)
}

func (m *Manager) gc() {
	for h, exp := range m.invites {
		if !m.now().Before(exp) {
			delete(m.invites, h)
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
