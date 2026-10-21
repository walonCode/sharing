// Package control is the local unix-socket API the CLI uses to drive the daemon.
package control

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"api/internal/pairing"
	"api/internal/server"
)

// SocketPath lives in the runtime dir so only the user can reach it.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "sharingd.sock")
}

type InviteResponse struct {
	URL string `json:"url"`
}

// Handler serves control requests for srv; fp is the pinned certificate fingerprint.
func Handler(srv *server.Server, fp string) http.Handler {
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
