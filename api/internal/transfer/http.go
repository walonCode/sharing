package transfer

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"api/internal/protocol"
	"api/internal/server"
)

// Service wires transfers into the authenticated HTTP API.
type Service struct {
	Srv      *server.Server
	Registry *Registry
}

func Mount(srv *server.Server) *Service {
	s := &Service{Srv: srv, Registry: NewRegistry()}
	srv.Handle("PUT /upload", http.HandlerFunc(s.handleUpload))
	srv.Handle("GET /transfer/{id}", http.HandlerFunc(s.handleDownload))
	return s
}

// handleUpload receives a file (or a tar when kind=dir) from the phone.
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// Each phone gets its own folder named after it: <receive dir>/<phone name>.
	peer := server.PeerFrom(r.Context())
	dest := filepath.Join(s.Srv.Store.Settings().ReceiveDir, PeerFolder(peer.Name))
	var (
		got string
		err error
	)
	switch r.URL.Query().Get("kind") {
	case KindDir:
		got, err = Unpack(r.Body, dest, name)
	case KindFile, "":
		got, err = SaveFile(r.Body, dest, name)
	default:
		http.Error(w, "bad kind", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, got)
}

func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	peer := server.PeerFrom(r.Context())
	o, ok := s.Registry.Take(r.PathValue("id"), peer.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Kind", o.Kind)
	w.Header().Set("X-Name", baseName(o.Path))
	if o.Kind == KindDir {
		w.Header().Set("Content-Type", "application/x-tar")
		_ = Pack(w, o.Path)
		return
	}
	f, err := os.Open(o.Path)
	if err != nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
	_, _ = io.Copy(w, f)
}

// Send registers path as an offer for the peer and notifies it over its websocket.
func (s *Service) Send(peerID, path string) (Offer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Offer{}, err
	}
	if !s.Srv.Hub.Online(peerID) {
		return Offer{}, ErrPeerOffline
	}
	kind, size := KindFile, info.Size()
	if info.IsDir() {
		kind, size = KindDir, 0
	}
	o := s.Registry.Add(peerID, path, kind, size)
	if !s.Srv.Hub.Send(peerID, protocol.Message{Type: protocol.TypeOffer, ID: o.ID, Kind: kind, Name: baseName(path), Size: size}) {
		s.Registry.Take(o.ID, peerID)
		return Offer{}, ErrPeerOffline
	}
	return o, nil
}

// PeerFolder is the sub-folder used for files received from a phone.
func PeerFolder(name string) string {
	n := SanitizeName(strings.TrimSpace(name))
	if n == "file" || strings.HasPrefix(n, ".") {
		return "phone"
	}
	return n
}
