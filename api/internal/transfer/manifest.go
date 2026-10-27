package transfer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Entry is one regular file of a folder offer, frozen at send time.
type Entry struct {
	Path  string `json:"path"` // slash separated, relative to the folder
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix nanoseconds
}

// Manifest is the frozen listing of a folder offer. Files are sorted by path.
// Symlinks and special files are skipped, as in Pack.
type Manifest struct {
	Name  string   `json:"name"`
	Dirs  []string `json:"dirs"` // every directory, including empty ones
	Files []Entry  `json:"files"`
}

// TotalSize is the sum of all file sizes.
func (m Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// BuildManifest walks dir. maxEntries <= 0 means unlimited.
func BuildManifest(dir string, maxEntries int) (Manifest, error) {
	dir = filepath.Clean(dir)
	m := Manifest{Name: filepath.Base(dir), Dirs: []string{}, Files: []Entry{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if maxEntries > 0 && len(m.Dirs)+len(m.Files) >= maxEntries {
			return ErrTooManyEntries
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			m.Dirs = append(m.Dirs, filepath.ToSlash(rel))
		case info.Mode().IsRegular():
			m.Files = append(m.Files, Entry{Path: filepath.ToSlash(rel), Size: info.Size(), MTime: info.ModTime().UnixNano()})
		}
		return nil
	})
	return m, err
}

// ErrChanged means a file differs from what the offer promised.
var ErrChanged = errors.New("file changed since it was offered")

// OpenEntry opens manifest entry e beneath root without following links that
// leave root, and verifies it still has the size and mtime recorded at send time.
func OpenEntry(root string, e Entry) (*os.File, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(e.Path))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != e.Size || info.ModTime().UnixNano() != e.MTime {
		f.Close()
		return nil, ErrChanged
	}
	return f, nil
}

// ParseManifest validates a manifest received from outside (for example a
// phone-side listing or a persisted file): paths must be relative, clean,
// control-character free, and unique.
func ParseManifest(m Manifest, maxEntries int) error {
	if maxEntries > 0 && len(m.Dirs)+len(m.Files) > maxEntries {
		return ErrTooManyEntries
	}
	seen := make(map[string]bool, len(m.Files))
	check := func(p string) error {
		if err := checkName(p); err != nil {
			return err
		}
		if _, err := safeJoin("/x", p); err != nil || strings.Contains(p, `\`) {
			return fmt.Errorf("%w: %q", ErrUnsafePath, p)
		}
		return nil
	}
	for _, d := range m.Dirs {
		if err := check(d); err != nil {
			return err
		}
	}
	for _, f := range m.Files {
		if err := check(f.Path); err != nil {
			return err
		}
		if f.Size < 0 || seen[f.Path] {
			return fmt.Errorf("%w: bad or duplicate entry %q", ErrUnsafePath, f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}
