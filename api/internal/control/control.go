// Package control is the local unix-socket API the CLI uses to drive the daemon.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"api/internal/config"
	"api/internal/pairing"
	"api/internal/protocol"
	"api/internal/server"
	"api/internal/transfer"
)

// SocketPath lives in a private directory: $XDG_RUNTIME_DIR/sharing/, or
// ~/.local/state/sharing/ when there is no runtime dir. It is never under /tmp,
// where another local user could pre-create or race the path.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "sharing", "sharingd.sock")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "state", "sharing", "sharingd.sock")
}

// secureDir creates dir with mode 0700 and verifies that it is a real
// directory (not a symlink) owned by this user and closed to everyone else.
func secureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() { //nolint:gosec // uid fits int
		return fmt.Errorf("%s is not owned by you", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%s is accessible to other users: %w", dir, err)
		}
	}
	return nil
}

type SendRequest struct {
	Path string `json:"path"`
	Peer string `json:"peer,omitempty"` // device id or name; optional with one peer
}

// SendResponse describes the offer created by a send.
type SendResponse struct {
	ID    string `json:"id"`
	State string `json:"state"` // "queued" when the phone is offline, else "offered"
}

// SettingsRequest changes settings on a running daemon; empty fields are left alone.
// A new Name takes effect for mDNS after the daemon restarts.
type SettingsRequest struct {
	ReceiveDir string `json:"receiveDir,omitempty"`
	Name       string `json:"name,omitempty"`
}

// InviteRequest is the optional body of POST /invite.
type InviteRequest struct {
	// Yes skips the on-terminal confirmation of the phone that scans the invite.
	Yes bool `json:"yes,omitempty"`
}

type InviteResponse struct {
	URL string `json:"url"`
	// Confirm is true when `pair` must wait for and ask about the phone.
	Confirm bool `json:"confirm,omitempty"`
}

// PeerInfo is one row of GET /peers.
type PeerInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
}

// Decision answers a pending pairing request.
type Decision struct {
	Accept bool `json:"accept"`
}

// API serves the control routes for one running daemon.
type API struct {
	Srv         *server.Server
	Svc         *transfer.Service
	Fingerprint string
}

// Handler serves control requests for srv; fp is the pinned certificate fingerprint.
func Handler(srv *server.Server, svc *transfer.Service, fp string) http.Handler {
	return (&API{Srv: srv, Svc: svc, Fingerprint: fp}).Routes()
}

// Routes returns the control mux.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /invite", a.invite)
	mux.HandleFunc("GET /pairing/pending", a.pairingPending)
	mux.HandleFunc("POST /pairing/{id}", a.pairingDecide)
	mux.HandleFunc("POST /send", a.send)
	mux.HandleFunc("POST /settings", a.settings)
	mux.HandleFunc("DELETE /peers/{id}", a.unpair)
	mux.HandleFunc("POST /peers/{id}/rotate", a.rotate)
	mux.HandleFunc("GET /peers", a.peers)
	mux.HandleFunc("GET /transfers", a.transfers)
	mux.HandleFunc("GET /transfers/{id}", a.transfer)
	mux.HandleFunc("POST /transfers/{id}/cancel", a.cancel)
	mux.HandleFunc("POST /transfers/{id}/retry", a.retry)
	return mux
}

func (a *API) invite(w http.ResponseWriter, r *http.Request) {
	var req InviteRequest
	if r.ContentLength != 0 {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req)
	}
	set := a.Srv.Store.Settings()
	confirm := set.ConfirmPairing && !req.Yes
	inv := pairing.Invite{
		Host:        LocalIP(),
		Port:        set.Port,
		Fingerprint: a.Fingerprint,
		Token:       a.Srv.Pairing.NewInviteConfirm(pairing.InviteTTL, confirm),
		Name:        set.Name,
	}
	writeJSON(w, InviteResponse{URL: inv.URL(), Confirm: confirm})
}

// pairingPending long-polls (up to ?wait seconds, default 25) for a phone that
// wants to pair; 204 when none showed up.
func (a *API) pairingPending(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(min(max(wait, 0), 60)) * time.Second)
	for {
		if pend := a.Srv.Pairing.Pending(); len(pend) > 0 {
			writeJSON(w, pend[0])
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (a *API) pairingDecide(w http.ResponseWriter, r *http.Request) {
	var d Decision
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&d); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !a.Srv.Pairing.Decide(r.PathValue("id"), d.Accept) {
		http.Error(w, "no such pairing request", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) send(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	peerID, err := resolvePeer(a.Srv, req.Peer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o, err := a.Svc.Send(peerID, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state := "offered"
	if o.State == transfer.StateQueued {
		state = "queued"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(SendResponse{ID: o.ID, State: state})
}

func (a *API) settings(w http.ResponseWriter, r *http.Request) {
	var req SettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ReceiveDir != "" && !filepath.IsAbs(req.ReceiveDir) {
		http.Error(w, "receiveDir must be an absolute path", http.StatusBadRequest)
		return
	}
	if req.Name != "" {
		if _, err := server.CleanName(req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	err := a.Srv.Store.UpdateSettings(func(s *config.Settings) {
		if req.ReceiveDir != "" {
			s.ReceiveDir = req.ReceiveDir
		}
		if req.Name != "" {
			s.Name = req.Name
		}
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, a.Srv.Store.Settings())
}

func (a *API) unpair(w http.ResponseWriter, r *http.Request) {
	id, err := resolvePeer(a.Srv, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := a.Srv.RemovePeer(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rotate issues a new device token and pushes it over the live websocket. The
// old token keeps working for config.RotationGrace so the phone can store the new one.
func (a *API) rotate(w http.ResponseWriter, r *http.Request) {
	id, err := resolvePeer(a.Srv, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !a.Srv.Hub.Online(id) {
		http.Error(w, "peer is offline; the new token can only be delivered to a connected phone", http.StatusConflict)
		return
	}
	token := pairing.NewToken()
	if err := a.Srv.Store.RotatePeer(id, pairing.HashToken(token), time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Srv.Hub.Send(id, protocol.Message{Type: protocol.TypeToken, Token: token}); err != nil {
		http.Error(w, "could not deliver the token: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) peers(w http.ResponseWriter, r *http.Request) {
	out := []PeerInfo{}
	for _, p := range a.Srv.Store.Peers() {
		out = append(out, PeerInfo{ID: p.ID, Name: p.Name, Online: a.Srv.Hub.Online(p.ID), LastSeen: p.LastSeen})
	}
	slices.SortFunc(out, func(x, y PeerInfo) int { return strings.Compare(x.Name, y.Name) })
	writeJSON(w, out)
}

func (a *API) transfers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.Svc.List())
}

func (a *API) transfer(w http.ResponseWriter, r *http.Request) {
	t, ok := a.Svc.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such transfer", http.StatusNotFound)
		return
	}
	writeJSON(w, t)
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	if err := a.Svc.Cancel(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) retry(w http.ResponseWriter, r *http.Request) {
	if err := a.Svc.Retry(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ErrAlreadyRunning means another daemon is already serving the control socket.
var ErrAlreadyRunning = errors.New("sharingd is already running")

// Listen listens on path with 0600 permissions. A socket left behind by a crashed
// daemon is replaced, but one that still answers belongs to a live daemon and is
// never touched: a second instance must not take over its control socket.
func Listen(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, ErrAlreadyRunning
	}
	if err := secureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	// The socket is created with the process umask; tighten it for the
	// duration of Listen so there is no window where it is group/world accessible.
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
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
