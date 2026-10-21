// Package control is the local unix-socket API the CLI uses to drive the daemon.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"api/internal/pairing"
	"api/internal/server"
	"api/internal/transfer"
)

// SocketPath lives in the runtime dir so only the user can reach it.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "sharingd.sock")
}

type SendRequest struct {
	Path string `json:"path"`
	Peer string `json:"peer,omitempty"` // device id or name; optional with one peer
}

// resolvePeer picks the target peer: by id/name, or the only one paired.
func resolvePeer(srv *server.Server, want string) (string, error) {
	peers := srv.Store.Peers()
	if want == "" {
		if len(peers) == 1 {
			return peers[0].ID, nil
		}
		return "", fmt.Errorf("%d paired peers; pass --to <id or name>", len(peers))
	}
	for _, p := range peers {
		if p.ID == want || p.Name == want {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no paired peer %q", want)
}

type InviteResponse struct {
	URL string `json:"url"`
}

// Handler serves control requests for srv; fp is the pinned certificate fingerprint.
func Handler(srv *server.Server, svc *transfer.Service, fp string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /invite", func(w http.ResponseWriter, r *http.Request) {
		set := srv.Store.Settings()
		inv := pairing.Invite{
			Host:        LocalIP(),
			Port:        set.Port,
			Fingerprint: fp,
			Token:       srv.Pairing.NewInvite(pairing.InviteTTL),
			Name:        set.Name,
		}
		writeJSON(w, InviteResponse{URL: inv.URL()})
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path required", http.StatusBadRequest)
			return
		}
		peerID, err := resolvePeer(srv, req.Peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := svc.Send(peerID, req.Path); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, transfer.ErrPeerOffline) {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		type peer struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Online bool   `json:"online"`
		}
		var out []peer
		for _, p := range srv.Store.Peers() {
			out = append(out, peer{p.ID, p.Name, srv.Hub.Online(p.ID)})
		}
		writeJSON(w, out)
	})
	return mux
}

// Listen removes a stale socket and listens on path with 0600 permissions.
func Listen(path string) (net.Listener, error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return l, os.Chmod(path, 0o600)
}

// Client returns an http.Client that talks to the daemon socket.
func Client(path string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}},
	}
}

// LocalIP returns the address of the interface used for the default route.
// The UDP "connection" sends no packets.
func LocalIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		h, _ := os.Hostname()
		return h
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
