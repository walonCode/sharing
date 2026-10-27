package transfer

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Limits bound what one incoming transfer may consume. Zero values mean "no
// limit" for MaxBytes and MaxEntries; MinFree is a free-space reserve.
type Limits struct {
	MaxBytes   int64
	MaxEntries int
	MinFree    int64
}

var (
	ErrTooLarge       = errors.New("upload exceeds the size limit")
	ErrTooManyEntries = errors.New("archive has too many entries")
	ErrNoSpace        = errors.New("not enough free disk space")
)

// freeBytes reports free space for the filesystem holding dir. It is a
// variable so tests can simulate a full disk.
var freeBytes = func(dir string) (uint64, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(dir, &st)
		if err == nil {
			return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // Bsize is positive
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, os.ErrNotExist) || parent == dir {
			return 0, err
		}
		dir = parent
	}
}

// CheckSpace fails with ErrNoSpace unless need bytes fit while keeping the reserve.
func (l Limits) CheckSpace(dir string, need int64) error {
	free, err := freeBytes(dir)
	if err != nil {
		return nil // cannot tell; the write itself will fail if the disk is full
	}
	if int64(free) < need+l.MinFree { //nolint:gosec // free fits int64 on any real disk
		return ErrNoSpace
	}
	return nil
}

// CheckLength validates a declared Content-Length / upload size up front.
func (l Limits) CheckLength(dir string, size int64) error {
	if size > 0 && l.MaxBytes > 0 && size > l.MaxBytes {
		return ErrTooLarge
	}
	return l.CheckSpace(dir, max(size, 0))
}

// HTTPStatus maps a transfer failure to a response code.
func HTTPStatus(err error) int {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrTooManyEntries), errors.As(err, &mbe):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrNoSpace), errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return http.StatusInsufficientStorage
	default:
		return http.StatusUnprocessableEntity
	}
}

// slots limits concurrent transfers per peer.
type slots struct {
	mu sync.Mutex
	n  map[string]int
}

func newSlots() *slots { return &slots{n: map[string]int{}} }

// acquire takes a slot for peerID if fewer than limit are in use (limit <= 0: unlimited).
func (s *slots) acquire(peerID string, limit int) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > 0 && s.n[peerID] >= limit {
		return nil, false
	}
	s.n[peerID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.n[peerID]--; s.n[peerID] <= 0 {
				delete(s.n, peerID)
			}
		})
	}, true
}
