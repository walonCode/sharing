package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Upload session kinds.
const (
	UploadFile      = "file"
	UploadDir       = "dir"
	UploadFileInDir = "file-in-dir"
)

const (
	// UploadIdleTTL removes a session nobody has touched for this long.
	UploadIdleTTL = 72 * time.Hour
	// UploadResultTTL is how long a finished session keeps answering a repeated
	// final PATCH or HEAD, so a lost response never produces a duplicate file.
	UploadResultTTL = 10 * time.Minute
	// maxOpenUploads bounds incomplete sessions per peer.
	maxOpenUploads = 64
	syncEvery      = 64 << 20
)

var (
	ErrNoUpload   = errors.New("no such upload")
	ErrOffset     = errors.New("offset does not match the stored length")
	ErrUploadBusy = errors.New("another request is writing this upload")
	ErrIncomplete = errors.New("upload is incomplete")
)

// Upload is a resumable phone-to-laptop transfer: a file, a folder, or a file
// that belongs to a folder. Its state is a JSON sidecar in <config>/uploads so
// it survives a daemon restart; the bytes live in a .partial-* file next to
// the final destination.
type Upload struct {
	ID       string    `json:"id"`
	PeerID   string    `json:"peerId"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Size     int64     `json:"size"` // -1 when unknown
	DirID    string    `json:"dirId,omitempty"`
	RelPath  string    `json:"relPath,omitempty"`
	DestDir  string    `json:"destDir"`
	Partial  string    `json:"partial"`
	Created  time.Time `json:"created"`
	Activity time.Time `json:"activity"`
	Done     bool      `json:"done"`
	Result   string    `json:"result,omitempty"`
	DoneAt   time.Time `json:"doneAt,omitzero"`
	Attempts int       `json:"attempts"`
	Error    string    `json:"error,omitempty"`

	busy    bool
	members int
	rate    rate
}

// Uploads is the set of upload sessions.
type Uploads struct {
	dir string // sidecar directory

	mu   sync.Mutex
	byID map[string]*Upload
	now  func() time.Time
}

// OpenUploads loads the sessions persisted in dir.
func OpenUploads(dir string) (*Uploads, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	u := &Uploads{dir: dir, byID: map[string]*Upload{}, now: time.Now}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var up Upload
		if json.Unmarshal(b, &up) != nil || up.ID == "" || up.ID+".json" != e.Name() {
			continue
		}
		u.byID[up.ID] = &up
	}
	for _, up := range u.byID {
		if up.DirID != "" {
			if d := u.byID[up.DirID]; d != nil {
				d.members++
			}
		}
	}
	return u, nil
}

func (u *Uploads) sidecar(id string) string { return filepath.Join(u.dir, id+".json") }

// saveLocked persists one session atomically (mode 0600).
func (u *Uploads) saveLocked(up *Upload) {
	b, err := json.Marshal(up)
	if err != nil {
		return
	}
	tmp := u.sidecar(up.ID) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		if os.Rename(tmp, u.sidecar(up.ID)) != nil {
			_ = os.Remove(tmp)
		}
	}
}

// get returns the session if peerID owns it; other peers see "not found".
func (u *Uploads) get(id, peerID string) (*Upload, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	up := u.byID[id]
	if up == nil || (peerID != "" && up.PeerID != peerID) {
		return nil, false
	}
	return up, true
}

// UploadInfo is a copy of a session's public state.
func (u *Uploads) snapshot(up *Upload) Upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := *up
	return c
}

// List returns copies of all sessions.
func (u *Uploads) List() []Upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]Upload, 0, len(u.byID))
	for _, up := range u.byID {
		out = append(out, *up)
	}
	return out
}

// partialBytes sums the sizes of a peer's unfinished sessions.
func (u *Uploads) openStats(peerID string) (open int, declared int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, up := range u.byID {
		if up.PeerID == peerID && !up.Done && up.Kind != UploadDir {
			open++
			declared += max(up.Size, 0)
		}
	}
	return open, declared
}

// remove deletes a session, its partial data, and (for folders) its members.
func (u *Uploads) remove(up *Upload) {
	u.mu.Lock()
	var gone []*Upload
	gone = append(gone, up)
	if up.Kind == UploadDir {
		for _, m := range u.byID {
			if m.DirID == up.ID {
				gone = append(gone, m)
			}
		}
	}
	for _, g := range gone {
		delete(u.byID, g.ID)
		_ = os.Remove(u.sidecar(g.ID))
	}
	u.mu.Unlock()
	for _, g := range gone {
		if g.Partial != "" && !g.Done {
			_ = os.RemoveAll(g.Partial)
		}
	}
}

// RemovePeer deletes every session of a peer (unpair).
func (u *Uploads) RemovePeer(peerID string) {
	for _, up := range u.List() {
		if up.PeerID == peerID {
			if cur, ok := u.get(up.ID, ""); ok {
				u.remove(cur)
			}
		}
	}
}

// Janitor removes finished sessions after UploadResultTTL and idle ones after
// UploadIdleTTL, and returns how many it removed.
func (u *Uploads) Janitor() int {
	now := u.now()
	n := 0
	for _, up := range u.List() {
		cur, ok := u.get(up.ID, "")
		if !ok || up.busy {
			continue
		}
		stale := (up.Done && now.Sub(up.DoneAt) > UploadResultTTL) || (!up.Done && now.Sub(up.Activity) > UploadIdleTTL)
		if up.DirID != "" && !up.Done {
			if _, parent := u.get(up.DirID, ""); !parent {
				stale = true // orphaned member
			}
		}
		if stale {
			u.remove(cur)
			n++
		}
	}
	return n
}

// Referenced returns the base names of partial data owned by live sessions.
func (u *Uploads) Referenced() map[string]bool {
	keep := map[string]bool{}
	for _, up := range u.List() {
		if !up.Done && up.Partial != "" {
			keep[filepath.Base(up.Partial)] = true
		}
	}
	return keep
}

// --- operations used by the HTTP handlers ---

// UploadSpec is the body of POST /uploads.
type UploadSpec struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Size    *int64 `json:"size,omitempty"`
	DirID   string `json:"dirId,omitempty"`
	RelPath string `json:"relPath,omitempty"`
}

// create validates spec and registers a session for peerID whose files go under destDir.
func (u *Uploads) create(peerID, destDir string, spec UploadSpec, lim Limits, maxPartial int64) (*Upload, error) {
	kind := spec.Kind
	if kind == "" {
		kind = UploadFile
	}
	size := int64(-1)
	if spec.Size != nil {
		if *spec.Size < 0 {
			return nil, errors.New("negative size")
		}
		size = *spec.Size
	}
	if err := checkName(spec.Name); err != nil {
		return nil, err
	}
	up := &Upload{
		ID: newID(), PeerID: peerID, Kind: kind, Size: size, DestDir: destDir,
		Name: SanitizeName(spec.Name), Created: u.now(), Activity: u.now(),
	}
	open, declared := u.openStats(peerID)
	if open >= maxOpenUploads {
		return nil, ErrTooManyOpen
	}
	switch kind {
	case UploadDir:
		up.Size = -1
		up.Partial = filepath.Join(destDir, ".partial-dir-"+up.ID)
	case UploadFile:
		up.Partial = filepath.Join(destDir, ".partial-"+up.ID)
	case UploadFileInDir:
		parent, ok := u.get(spec.DirID, peerID)
		if !ok || parent.Kind != UploadDir || parent.Done {
			return nil, ErrNoUpload
		}
		if err := checkName(spec.RelPath); err != nil {
			return nil, err
		}
		if _, err := safeJoin("/x", spec.RelPath); err != nil {
			return nil, err
		}
		u.mu.Lock()
		over := lim.MaxEntries > 0 && parent.members >= lim.MaxEntries
		u.mu.Unlock()
		if over {
			return nil, ErrTooManyEntries
		}
		up.DirID, up.RelPath = parent.ID, cleanRel(spec.RelPath)
		up.Name = filepath.Base(up.RelPath)
		up.Partial = filepath.Join(parent.DestDir, ".partial-"+up.ID)
		up.DestDir = parent.DestDir
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	if size >= 0 {
		if maxPartial > 0 && declared+size > maxPartial {
			return nil, ErrTooLarge
		}
		if err := lim.CheckLength(destDir, size); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	if kind == UploadDir {
		if err := os.Mkdir(up.Partial, 0o755); err != nil {
			return nil, err
		}
	} else {
		f, err := os.OpenFile(up.Partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	u.mu.Lock()
	u.byID[up.ID] = up
	if parent := u.byID[up.DirID]; parent != nil {
		parent.members++
	}
	u.saveLocked(up)
	u.mu.Unlock()
	return up, nil
}

var ErrTooManyOpen = errors.New("too many unfinished uploads")

func cleanRel(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(p, `\`, "/")))), "/")
}

// Offset is the number of bytes durably stored for the session.
func (u *Uploads) Offset(up *Upload) int64 {
	u.mu.Lock()
	done, size, partial := up.Done, up.Size, up.Partial
	u.mu.Unlock()
	if done {
		return max(size, 0)
	}
	if up.Kind == UploadDir {
		return 0
	}
	info, err := os.Stat(partial)
	if err != nil {
		return 0
	}
	return info.Size()
}

// busyWait is how long a second request waits for the first writer to let go
// before getting 423. A client that retries right after its connection died
// otherwise races the handler that is still noticing the dead connection.
var busyWait = 2 * time.Second

// acquire claims the session for one writer, waiting up to busyWait for a previous one.
func (u *Uploads) acquire(up *Upload) error {
	deadline := time.Now().Add(busyWait)
	for {
		u.mu.Lock()
		if !up.busy {
			up.busy = true
			u.mu.Unlock()
			return nil
		}
		u.mu.Unlock()
		if time.Now().After(deadline) {
			return ErrUploadBusy
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (u *Uploads) release(up *Upload) {
	u.mu.Lock()
	up.busy = false
	u.mu.Unlock()
}

// append writes r to the partial file at offset, which must equal its length.
// It returns the new offset. The data is fsynced before returning, even when r
// fails midway, so the reported offset never runs ahead of what is on disk.
func (u *Uploads) append(up *Upload, offset int64, r io.Reader, allowed int64) (n int64, err error) {
	f, err := os.OpenFile(up.Partial, os.O_WRONLY, 0)
	if err != nil {
		return offset, err
	}
	defer func() {
		serr := f.Sync()
		cerr := f.Close()
		if err == nil {
			err = errors.Join(serr, cerr)
		}
	}()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	buf := make([]byte, 256<<10)
	var sinceSync int64
	src := r
	if allowed >= 0 {
		src = io.LimitReader(r, allowed)
	}
	for {
		m, rerr := src.Read(buf)
		if m > 0 {
			if _, werr := f.Write(buf[:m]); werr != nil {
				return offset + n, werr
			}
			n += int64(m)
			sinceSync += int64(m)
			if sinceSync >= syncEvery {
				if err := f.Sync(); err != nil {
					return offset + n, err
				}
				sinceSync = 0
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return offset + n, rerr
		}
	}
	if allowed >= 0 {
		// One more byte than allowed means the client sent too much.
		var one [1]byte
		if k, _ := r.Read(one[:]); k > 0 {
			return offset + n, ErrTooLarge
		}
	}
	return offset + n, nil
}

// touch records activity and, optionally, an error for the transfer list.
func (u *Uploads) touch(up *Upload, offset int64, errMsg string, failed bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	up.Activity = now
	up.rate.sample(now, offset)
	if failed {
		up.Attempts++
		up.Error = errMsg
	} else if errMsg == "" {
		up.Error = ""
	}
	u.saveLocked(up)
}

// finish publishes a fully received upload and records the result.
func (u *Uploads) finish(up *Upload) (string, error) {
	switch up.Kind {
	case UploadFile:
		if err := os.Chmod(up.Partial, 0o644); err != nil {
			return "", err
		}
		final, err := publish(up.Partial, up.DestDir, up.Name)
		if err != nil {
			return "", err
		}
		_ = os.Remove(up.Partial)
		return u.markDone(up, filepath.Base(final)), nil
	case UploadFileInDir:
		parent, ok := u.get(up.DirID, up.PeerID)
		if !ok {
			return "", ErrNoUpload
		}
		dst, err := safeJoin(parent.Partial, up.RelPath)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.Chmod(up.Partial, 0o644); err != nil {
			return "", err
		}
		if err := os.Link(up.Partial, dst); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("duplicate path %q in folder", up.RelPath)
			}
			return "", err
		}
		_ = os.Remove(up.Partial)
		return u.markDone(up, up.RelPath), nil
	}
	return "", errors.New("not a file upload")
}

func (u *Uploads) markDone(up *Upload, result string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	up.Done, up.Result, up.DoneAt, up.Activity, up.Error = true, result, u.now(), u.now(), ""
	if up.Size < 0 {
		if info, err := os.Stat(filepath.Join(up.DestDir, result)); err == nil && up.Kind == UploadFile {
			up.Size = info.Size()
		}
	}
	u.saveLocked(up)
	return result
}

// completeDir verifies the folder holds count files and publishes it atomically.
func (u *Uploads) completeDir(dir *Upload, count int) (string, error) {
	u.mu.Lock()
	var pending int
	for _, m := range u.byID {
		if m.DirID == dir.ID && !m.Done {
			pending++
		}
	}
	u.mu.Unlock()
	if pending > 0 {
		return "", fmt.Errorf("%w: %d files still uploading", ErrIncomplete, pending)
	}
	files := 0
	err := filepath.WalkDir(dir.Partial, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files++
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if files != count {
		return "", fmt.Errorf("%w: have %d files, expected %d", ErrIncomplete, files, count)
	}
	final, err := publish(dir.Partial, dir.DestDir, dir.Name)
	if err != nil {
		return "", err
	}
	result := u.markDone(dir, filepath.Base(final))
	u.mu.Lock()
	for id, m := range u.byID {
		if m.DirID == dir.ID {
			delete(u.byID, id)
			_ = os.Remove(u.sidecar(id))
		}
	}
	u.mu.Unlock()
	return result, nil
}
