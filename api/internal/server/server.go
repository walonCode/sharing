// Package server exposes the HTTPS API: pairing, the websocket, and the
// authenticated routes other packages mount (transfers).
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"api/internal/config"
	"api/internal/pairing"
	"api/internal/protocol"

	"github.com/coder/websocket"
)

type ctxKey struct{}

type Server struct {
	Store   *config.Store
	Pairing *pairing.Manager
	Hub     *Hub

	// OnMessage receives every message a peer sends over its websocket.
	OnMessage func(peerID string, m protocol.Message)
	// OnConnect runs after a peer's websocket is established and its hello queued.
	OnConnect func(peerID string)
	// OnUnpair runs after a peer was removed (sessions and offers must go).
	OnUnpair func(peerID string)

	mux     *http.ServeMux
	limiter *failLimiter
	now     func() time.Time
}

func New(store *config.Store) *Server {
	s := &Server{
		Store: store, Pairing: pairing.NewManager(), Hub: NewHub(),
		mux: http.NewServeMux(), limiter: newFailLimiter(), now: time.Now,
	}
	s.mux.HandleFunc("POST /pair", s.handlePair)
	s.mux.Handle("GET /ws", s.auth(http.HandlerFunc(s.handleWS)))
	return s
}

// Handle registers an authenticated route (used by the transfer package wiring).
// Its request body and response are subject to the idle deadline.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, s.auth(withIdleDeadline(IdleTimeout, h)))
}

func (s *Server) Handler() http.Handler { return s.mux }

// PeerFrom returns the authenticated peer for a request passed through auth.
func PeerFrom(ctx context.Context) config.Peer {
	p, _ := ctx.Value(ctxKey{}).(config.Peer)
	return p
}

// RemovePeer unpairs a peer: its token stops working, its socket closes, and
// OnUnpair lets other packages drop its sessions and offers.
func (s *Server) RemovePeer(id string) error {
	if err := s.Store.RemovePeer(id); err != nil {
		return err
	}
	s.Hub.Disconnect(id)
	if s.OnUnpair != nil {
		s.OnUnpair(id)
	}
	return nil
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if wait := s.limiter.blocked(ip); wait > 0 {
			tooMany(w, wait)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && token != "" {
			hash := pairing.HashToken(token)
			now := s.now()
			for _, p := range s.Store.Peers() {
				if p.Matches(hash, now) {
					s.Store.TouchPeer(p.ID, now)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
					return
				}
			}
		}
		s.limiter.fail(ip)
		slog.Warn("auth failed", "ip", ip, "path", r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := s.limiter.blocked(ip); wait > 0 {
		tooMany(w, wait)
		return
	}
	var req protocol.PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, nameErr := CleanName(req.Name)
	if idErr := CheckDeviceID(req.DeviceID); idErr != nil || nameErr != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	needConfirm, ok := s.Pairing.RedeemFor(req.Token, req.DeviceID)
	if !ok {
		s.limiter.fail(ip)
		slog.Warn("pairing rejected: invalid or expired invite", "ip", ip)
		http.Error(w, "invalid or expired invite", http.StatusForbidden)
		return
	}
	if needConfirm && !s.Pairing.Ask(r.Context(), req.DeviceID, name) {
		slog.Warn("pairing declined", "ip", ip, "name", name)
		http.Error(w, "pairing declined", http.StatusForbidden)
		return
	}
	peerToken := pairing.NewToken()
	err := s.Store.AddPeer(config.Peer{
		ID:        req.DeviceID,
		Name:      name,
		TokenHash: pairing.HashToken(peerToken),
		PairedAt:  s.now(),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.Pairing.MarkIssued(req.Token, req.DeviceID)
	slog.Info("paired", "name", name, "device", req.DeviceID)
	set := s.Store.Settings()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.PairResponse{PeerToken: peerToken, DeviceID: set.DeviceID, LaptopName: set.Name})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	peer := PeerFrom(r.Context())
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	// The default 32 KiB read limit is below the clipboard cap and would close
	// the connection on a large clip instead of letting it be ignored.
	ws.SetReadLimit(protocol.MaxMessageBytes)
	hello := protocol.Message{Type: protocol.TypeHello, Name: s.Store.Settings().Name, Proto: protocol.Version, Caps: []string{"resume", "manifest", "sha256"}}
	s.Hub.serve(r.Context(), peer.ID, ws, hello, func(m protocol.Message) {
		if s.OnMessage != nil {
			s.OnMessage(peer.ID, m)
		}
	}, func() {
		if s.OnConnect != nil {
			s.OnConnect(peer.ID)
		}
	})
}
