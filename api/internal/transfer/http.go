package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"api/internal/config"
	"api/internal/protocol"
	"api/internal/server"
)

// Service wires transfers into the authenticated HTTP API.
type Service struct {
	Srv      *server.Server
	Registry *Registry
	Uploads  *Uploads

	slots *slots
}

// Mount registers the transfer routes on srv and loads persisted offers and
// upload sessions from the config directory.
func Mount(srv *server.Server) *Service {
	dir := srv.Store.Dir()
	reg, err := OpenRegistry(filepath.Join(dir, "offers.json"))
	if err != nil {
		reg = NewRegistry() // an unreadable file costs only the old offers
	}
	ups, err := OpenUploads(filepath.Join(dir, "uploads"))
	if err != nil {
		ups = &Uploads{byID: map[string]*Upload{}, now: time.Now, dir: filepath.Join(dir, "uploads")}
	}
	s := &Service{Srv: srv, Registry: reg, Uploads: ups, slots: newSlots()}
	srv.OnConnect = s.Replay
	srv.OnUnpair = func(peerID string) {
		s.Registry.RemovePeer(peerID)
		s.Uploads.RemovePeer(peerID)
	}

	srv.Handle("PUT /upload", http.HandlerFunc(s.handleUpload))
	srv.Handle("POST /uploads", http.HandlerFunc(s.handleUploadCreate))
	srv.Handle("HEAD /uploads/{id}", http.HandlerFunc(s.handleUploadHead))
	srv.Handle("PATCH /uploads/{id}", http.HandlerFunc(s.handleUploadPatch))
	srv.Handle("DELETE /uploads/{id}", http.HandlerFunc(s.handleUploadDelete))
	srv.Handle("POST /dirs/{id}/complete", http.HandlerFunc(s.handleDirComplete))

	srv.Handle("GET /transfer/{id}", http.HandlerFunc(s.handleDownload))
	srv.Handle("DELETE /transfer/{id}", http.HandlerFunc(s.handleOfferDelete))
	srv.Handle("POST /transfer/{id}/ack", http.HandlerFunc(s.handleAck))
	srv.Handle("GET /transfer/{id}/manifest", http.HandlerFunc(s.handleManifest))
	srv.Handle("GET /transfer/{id}/files/{n}", http.HandlerFunc(s.handleEntry))
	srv.Handle("GET /transfer/{id}/sha256", http.HandlerFunc(s.handleSum))
	return s
}

// Maintain sweeps leftovers now and then keeps expiring offers and sessions
// until stop is closed.
func (s *Service) Maintain(stop <-chan struct{}) {
	s.Uploads.Janitor()
	root := s.Srv.Store.Settings().ReceiveDir
	SweepPartials(root, time.Now().Add(-UploadIdleTTL), s.Uploads.Referenced())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Registry.GC()
			s.Uploads.Janitor()
		}
	}
}

func (s *Service) limits() (config.Settings, Limits) {
	set := s.Srv.Store.Settings()
	return set, Limits{MaxBytes: set.MaxUploadBytes, MaxEntries: set.MaxEntries, MinFree: set.MinFreeBytes}
}

func (s *Service) peerDir(set config.Settings, peerName string) string {
	return filepath.Join(set.ReceiveDir, PeerFolder(peerName))
}

func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	http.Error(w, "too many concurrent transfers", http.StatusServiceUnavailable)
}

func fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), HTTPStatus(err))
}

// handleUpload receives a file (or a tar when kind=dir) from a revision 1 phone.
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// Each phone gets its own folder named after it: <receive dir>/<phone name>.
	peer := server.PeerFrom(r.Context())
	set, lim := s.limits()
	dest := s.peerDir(set, peer.Name)
	release, ok := s.slots.acquire(peer.ID, set.MaxTransfers)
	if !ok {
		busy(w)
		return
	}
	defer release()
	if err := lim.CheckLength(dest, r.ContentLength); err != nil {
		fail(w, err)
		return
	}
	body := io.Reader(r.Body)
	if lim.MaxBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, lim.MaxBytes)
	}
	var (
		got string
		err error
	)
	switch r.URL.Query().Get("kind") {
	case KindDir:
		got, err = UnpackLimited(body, dest, name, lim)
	case KindFile, "":
		got, err = SaveFileLimited(body, dest, name, lim)
	default:
		http.Error(w, "bad kind", http.StatusBadRequest)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, got)
}

// --- resumable uploads ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) (*Upload, bool) {
	up, ok := s.Uploads.get(r.PathValue("id"), server.PeerFrom(r.Context()).ID)
	if !ok {
		http.NotFound(w, r)
	}
	return up, ok
}

func (s *Service) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	var spec UploadSpec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&spec); err != nil || spec.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	peer := server.PeerFrom(r.Context())
	set, lim := s.limits()
	up, err := s.Uploads.create(peer.ID, s.peerDir(set, peer.Name), spec, lim, set.MaxPartialBytes)
	switch {
	case errors.Is(err, ErrNoUpload):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, ErrTooManyOpen):
		busy(w)
		return
	case err != nil:
		fail(w, err)
		return
	}
	resp := map[string]any{"id": up.ID, "offset": 0, "maxChunk": 0}
	if up.Kind == UploadDir {
		resp["dirId"] = up.ID
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Service) handleUploadHead(w http.ResponseWriter, r *http.Request) {
	up, ok := s.upload(w, r)
	if !ok {
		return
	}
	snap := s.Uploads.snapshot(up)
	w.Header().Set("Upload-Offset", strconv.FormatInt(s.Uploads.Offset(up), 10))
	if snap.Size >= 0 {
		w.Header().Set("Upload-Length", strconv.FormatInt(snap.Size, 10))
	}
	if snap.Done {
		w.Header().Set("Upload-Complete", "true")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request, up *Upload) {
	peer := server.PeerFrom(r.Context())
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "Upload-Offset required", http.StatusBadRequest)
		return
	}
	snap := s.Uploads.snapshot(up)
	if snap.Done { // a repeated final PATCH: same answer, no duplicate
		writeJSON(w, http.StatusCreated, map[string]string{"name": snap.Result})
		return
	}
	if snap.Kind == UploadDir {
		http.Error(w, "folder sessions take no data", http.StatusBadRequest)
		return
	}
	set, lim := s.limits()
	release, ok := s.slots.acquire(peer.ID, set.MaxTransfers)
	if !ok {
		busy(w)
		return
	}
	defer release()
	if err := s.Uploads.acquire(up); err != nil {
		http.Error(w, err.Error(), http.StatusLocked)
		return
	}
	defer s.Uploads.release(up)

	stored := s.Uploads.Offset(up)
	if offset != stored {
		w.Header().Set("Upload-Offset", strconv.FormatInt(stored, 10))
		http.Error(w, ErrOffset.Error(), http.StatusConflict)
		return
	}
	// How many more bytes this upload may take.
	allowed := int64(-1)
	if snap.Size >= 0 {
		allowed = snap.Size - offset
	}
	if lim.MaxBytes > 0 && (allowed < 0 || offset+allowed > lim.MaxBytes) {
		allowed = max(lim.MaxBytes-offset, 0)
	}
	// Space needed is what this request will write, not the most it could.
	need := max(r.ContentLength, 0)
	if r.ContentLength < 0 && snap.Size >= 0 {
		need = snap.Size - offset
	}
	if err := lim.CheckSpace(filepath.Dir(snap.Partial), need); err != nil {
		fail(w, err)
		return
	}
	end, err := s.Uploads.append(up, offset, r.Body, allowed)
	if err != nil {
		s.Uploads.touch(up, end, err.Error(), true)
		// A body that died midway is the usual case: what arrived is kept and
		// synced, and the client resumes from the offset a HEAD reports.
		code := HTTPStatus(err)
		if code == http.StatusUnprocessableEntity {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	s.Uploads.touch(up, end, "", false)

	complete := snap.Size >= 0 && end == snap.Size
	if snap.Size < 0 {
		complete = r.Header.Get("Upload-Complete") == "true"
	} else if r.Header.Get("Upload-Complete") == "true" && !complete {
		http.Error(w, ErrIncomplete.Error(), http.StatusUnprocessableEntity)
		return
	}
	if !complete {
		w.Header().Set("Upload-Offset", strconv.FormatInt(end, 10))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result, err := s.Uploads.finish(up)
	if err != nil {
		s.Uploads.touch(up, end, err.Error(), true)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "duplicate path") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": result})
}

func (s *Service) handleUploadPatch(w http.ResponseWriter, r *http.Request) {
	if up, ok := s.upload(w, r); ok {
		s.handlePatch(w, r, up)
	}
}

func (s *Service) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	if up, ok := s.upload(w, r); ok {
		s.Uploads.remove(up)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Service) handleDirComplete(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.upload(w, r)
	if !ok {
		return
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	snap := s.Uploads.snapshot(dir)
	if snap.Kind != UploadDir {
		http.Error(w, "not a folder session", http.StatusBadRequest)
		return
	}
	if snap.Done {
		writeJSON(w, http.StatusCreated, map[string]string{"name": snap.Result})
		return
	}
	if err := s.Uploads.acquire(dir); err != nil {
		http.Error(w, err.Error(), http.StatusLocked)
		return
	}
	defer s.Uploads.release(dir)
	result, err := s.Uploads.completeDir(dir, body.Count)
	if errors.Is(err, ErrIncomplete) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": result})
}

// --- downloads ---

// offer fetches the offer named in the path for the authenticated peer. It
// writes the response and returns false when the phone must stop retrying.
func (s *Service) offer(w http.ResponseWriter, r *http.Request) (Offer, bool) {
	o, ok := s.Registry.Get(r.PathValue("id"), server.PeerFrom(r.Context()).ID)
	if !ok {
		http.NotFound(w, r)
		return Offer{}, false
	}
	if !o.Live() {
		http.Error(w, "offer is finished", http.StatusGone)
		return Offer{}, false
	}
	return o, true
}

// markActive notes a download request on the offer and returns the updated copy.
func (s *Service) markActive(o Offer, countAttempt bool) {
	s.Registry.Update(o.ID, o.State != StateActive, func(x *Offer) {
		x.State = StateActive
		if countAttempt {
			x.Attempts++
		}
	})
}

func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offer(w, r)
	if !ok {
		return
	}
	set := s.Srv.Store.Settings()
	release, ok := s.slots.acquire(o.PeerID, set.MaxTransfers)
	if !ok {
		busy(w)
		return
	}
	defer release()
	legacy := r.Header.Get(protocol.HeaderProto) == ""
	s.markActive(o, r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-"))
	w.Header().Set("X-Kind", o.Kind)
	w.Header().Set("X-Name", baseName(o.Path))
	w.Header().Set("Content-Type", "application/octet-stream")

	if o.Kind == KindDir {
		// Revision 1 phones read a live tar of the folder. It cannot be resumed.
		w.Header().Set("Content-Type", "application/x-tar")
		if err := Pack(w, o.Path); err == nil && legacy {
			s.finish(o.ID, protocol.StatusOK, "")
		}
		return
	}
	f, err := openOffered(o)
	if err != nil {
		s.answerOpenError(w, o, err)
		return
	}
	defer f.Close()
	w.Header().Set("ETag", o.ETag)
	cw := &countWriter{ResponseWriter: w}
	http.ServeContent(cw, r, "", time.Unix(0, o.MTimeNs), f)
	if legacy && cw.status == http.StatusOK && cw.err == nil && cw.n == o.Size {
		s.finish(o.ID, protocol.StatusOK, "")
	}
}

func (s *Service) answerOpenError(w http.ResponseWriter, o Offer, err error) {
	switch {
	case errors.Is(err, ErrChanged):
		http.Error(w, err.Error(), http.StatusPreconditionFailed)
	case errors.Is(err, os.ErrNotExist):
		s.finish(o.ID, protocol.StatusFailed, "file is gone")
		http.Error(w, "gone", http.StatusGone)
	default:
		http.Error(w, "cannot open", http.StatusInternalServerError)
	}
}

// openOffered opens a file offer without following a symlink swapped in after
// the offer was made, and checks it still matches the frozen size and mtime.
func openOffered(o Offer) (*os.File, error) {
	f, err := os.OpenFile(o.Path, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != o.Size || info.ModTime().UnixNano() != o.MTimeNs {
		f.Close()
		return nil, ErrChanged
	}
	return f, nil
}

type countWriter struct {
	http.ResponseWriter
	status int
	n      int64
	err    error
}

func (c *countWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	if err != nil {
		c.err = err
	}
	return n, err
}

func (c *countWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (s *Service) handleOfferDelete(w http.ResponseWriter, r *http.Request) {
	o, ok := s.Registry.Get(r.PathValue("id"), server.PeerFrom(r.Context()).ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.finish(o.ID, protocol.StatusCancelled, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleAck lets the phone finish an offer over HTTP, so the result is not lost
// when the websocket happens to be down at the end of a download.
func (s *Service) handleAck(w http.ResponseWriter, r *http.Request) {
	o, ok := s.Registry.Get(r.PathValue("id"), server.PeerFrom(r.Context()).ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch body.Status {
	case protocol.StatusOK, protocol.StatusFailed, protocol.StatusCancelled:
	default:
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	s.finish(o.ID, body.Status, body.Error)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleManifest(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offer(w, r)
	if !ok {
		return
	}
	if o.Manifest == nil {
		http.Error(w, "not a folder offer", http.StatusBadRequest)
		return
	}
	s.markActive(o, false)
	writeJSON(w, http.StatusOK, o.Manifest)
}

func (s *Service) handleEntry(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offer(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if o.Manifest == nil || err != nil || n < 0 || n >= len(o.Manifest.Files) {
		http.NotFound(w, r)
		return
	}
	set := s.Srv.Store.Settings()
	release, ok := s.slots.acquire(o.PeerID, set.MaxTransfers)
	if !ok {
		busy(w)
		return
	}
	defer release()
	e := o.Manifest.Files[n]
	s.markActive(o, false)
	f, err := OpenEntry(o.Path, e)
	if err != nil {
		s.answerOpenError(w, o, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", fmt.Sprintf(`"%d-%d"`, e.Size, e.MTime))
	http.ServeContent(w, r, "", time.Unix(0, e.MTime), f)
}

// handleSum answers with the digests once the background hash has finished.
func (s *Service) handleSum(w http.ResponseWriter, r *http.Request) {
	o, ok := s.offer(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"ready": false}
	if o.Manifest == nil && o.SHA256 != "" {
		resp = map[string]any{"ready": true, "sha256": o.SHA256}
	} else if o.Manifest != nil && len(o.FileSums) == len(o.Manifest.Files) && o.FileSums != nil {
		resp = map[string]any{"ready": true, "files": o.FileSums}
	}
	writeJSON(w, http.StatusOK, resp)
}

// hashOffer computes the digests of an offer in the background.
func (s *Service) hashOffer(o Offer) {
	sumFile := func(f *os.File) (string, error) {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if o.Manifest == nil {
		f, err := openOffered(o)
		if err != nil {
			return
		}
		defer f.Close()
		if sum, err := sumFile(f); err == nil {
			s.Registry.Update(o.ID, true, func(x *Offer) { x.SHA256 = sum })
		}
		return
	}
	sums := make([]string, 0, len(o.Manifest.Files))
	for _, e := range o.Manifest.Files {
		if cur, ok := s.Registry.Get(o.ID, ""); !ok || !cur.Live() {
			return
		}
		f, err := OpenEntry(o.Path, e)
		if err != nil {
			return
		}
		sum, err := sumFile(f)
		f.Close()
		if err != nil {
			return
		}
		sums = append(sums, sum)
	}
	s.Registry.Update(o.ID, true, func(x *Offer) { x.FileSums = sums })
}

// PeerFolder is the sub-folder used for files received from a phone.
func PeerFolder(name string) string {
	n := SanitizeName(strings.TrimSpace(name))
	if n == "file" || strings.HasPrefix(n, ".") {
		return "phone"
	}
	return n
}
