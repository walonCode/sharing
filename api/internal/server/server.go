// Package server exposes the HTTPS API: pairing, websocket, and (later) transfers.
package server

import (
	"context"
	"encoding/json"
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

	mux *http.ServeMux
}

func New(store *config.Store) *Server {
	s := &Server{Store: store, Pairing: pairing.NewManager(), Hub: NewHub(), mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /pair", s.handlePair)
	s.mux.Handle("GET /ws", s.auth(http.HandlerFunc(s.handleWS)))
	return s
}

// Handle registers an authenticated route (used by the transfer package wiring).
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, s.auth(h)) }

func (s *Server) Handler() http.Handler { return s.mux }

// PeerFrom returns the authenticated peer for a request passed through auth.
func PeerFrom(ctx context.Context) config.Peer {
	p, _ := ctx.Value(ctxKey{}).(config.Peer)
	return p
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		for _, p := range s.Store.Peers() {
			if pairing.TokenMatches(token, p.TokenHash) {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.DeviceID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.Pairing.Redeem(req.Token) {
		http.Error(w, "invalid or expired invite", http.StatusForbidden)
		return
	}
	peerToken := pairing.NewToken()
	err := s.Store.AddPeer(config.Peer{
		ID:        req.DeviceID,
		Name:      req.Name,
		TokenHash: pairing.HashToken(peerToken),
		PairedAt:  time.Now(),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
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
	hello := protocol.Message{Type: protocol.TypeHello, Name: s.Store.Settings().Name}
	s.Hub.serve(r.Context(), peer.ID, ws, hello, func(m protocol.Message) {
		if s.OnMessage != nil {
			s.OnMessage(peer.ID, m)
		}
	})
}
