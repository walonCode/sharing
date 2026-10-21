// Package folders lets a paired phone browse the laptop's directories and choose
// where received files are saved, so both folders can be set from the phone.
// Everything is confined to the user's home directory.
package folders

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"api/internal/config"
	"api/internal/server"
)

type Service struct {
	Srv  *server.Server
	Home string
}

func Mount(srv *server.Server) (*Service, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return MountAt(srv, home), nil
}

// MountAt confines browsing to root (the home dir in production).
func MountAt(srv *server.Server, root string) *Service {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	s := &Service{Srv: srv, Home: root}
	srv.Handle("GET /fs", http.HandlerFunc(s.handleList))
	srv.Handle("POST /fs/mkdir", http.HandlerFunc(s.handleMkdir))
	srv.Handle("GET /settings", http.HandlerFunc(s.handleGetSettings))
	srv.Handle("PUT /settings", http.HandlerFunc(s.handlePutSettings))
	return s
}

var errOutside = errors.New("path is outside your home folder")

// resolve cleans p and verifies it (following symlinks) stays inside Home.
// For paths that do not exist yet, the nearest existing parent is checked.
func (s *Service) resolve(p string) (string, error) {
	if p == "" {
		return s.Home, nil
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	p = filepath.Clean(p)
	existing := p
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		next := filepath.Dir(existing)
		if next == existing {
			return "", errOutside
		}
		existing = next
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !within(s.Home, real) {
		return "", errOutside
	}
	return filepath.Join(real, strings.TrimPrefix(p, existing)), nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type ListResponse struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"` // empty at the top (home)
	Dirs   []string `json:"dirs"`
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	dir, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "cannot read folder", http.StatusNotFound)
		return
	}
	resp := ListResponse{Path: dir, Dirs: []string{}}
	if dir != s.Home {
		resp.Parent = filepath.Dir(dir)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// Stat follows symlinks, so linked folders show up, but only if they stay inside Home.
		full := filepath.Join(dir, e.Name())
		if real, err := filepath.EvalSymlinks(full); err != nil || !within(s.Home, real) {
			continue
		}
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			resp.Dirs = append(resp.Dirs, e.Name())
		}
	}
	sort.Strings(resp.Dirs)
	writeJSON(w, resp)
}

type mkdirRequest struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func (s *Service) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	parent, err := s.resolve(req.Parent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		http.Error(w, "cannot create folder", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": target})
}

type SettingsBody struct {
	ReceiveDir string `json:"receiveDir"`
	Name       string `json:"name"`
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	set := s.Srv.Store.Settings()
	writeJSON(w, SettingsBody{ReceiveDir: set.ReceiveDir, Name: set.Name})
}

func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req SettingsBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dir, err := s.resolve(req.ReceiveDir)
	if err != nil || req.ReceiveDir == "" {
		http.Error(w, "receive folder must be an absolute path inside your home folder", http.StatusForbidden)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "cannot create folder", http.StatusInternalServerError)
		return
	}
	if err := s.Srv.Store.UpdateSettings(func(x *config.Settings) { x.ReceiveDir = dir }); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	set := s.Srv.Store.Settings()
	writeJSON(w, SettingsBody{ReceiveDir: set.ReceiveDir, Name: set.Name})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
