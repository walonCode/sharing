// Package transfer moves files and folders: tar packing, safe extraction,
// collision-free naming, and the one-shot offer registry.
package transfer

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

var ErrUnsafePath = errors.New("unsafe path in archive")

// Pack streams dir as a tar whose single root entry is dir's base name.
// Symlinks and special files are skipped rather than followed.
func Pack(w io.Writer, dir string) error {
	dir = filepath.Clean(dir)
	root := filepath.Base(dir)
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := path.Join(root, filepath.ToSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		default:
			return nil
		}
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Unpack extracts a tar into a staging dir under destDir, then moves the result
// into place under a non-colliding name. It returns the final path. Entries that
// are absolute, escape the root, or are links are rejected, and nothing is
// published if extraction fails.
func Unpack(r io.Reader, destDir, fallbackName string) (string, error) {
	return UnpackLimited(r, destDir, fallbackName, Limits{})
}

// UnpackLimited is Unpack with caps on the number of entries and the total
// extracted bytes.
func UnpackLimited(r io.Reader, destDir, fallbackName string, lim Limits) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(destDir, ".partial-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	tr := tar.NewReader(r)
	var count, total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if count++; lim.MaxEntries > 0 && count > int64(lim.MaxEntries) {
			return "", ErrTooManyEntries
		}
		if err := checkName(h.Name); err != nil {
			return "", err
		}
		target, err := safeJoin(stage, h.Name)
		if err != nil {
			return "", err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return "", err
			}
			var src io.Reader = tr
			if lim.MaxBytes > 0 {
				// Reading one byte past the remaining budget detects an overrun.
				src = io.LimitReader(tr, lim.MaxBytes-total+1)
			}
			n, err := io.Copy(f, src)
			total += n
			if err == nil && lim.MaxBytes > 0 && total > lim.MaxBytes {
				err = ErrTooLarge
			}
			if err != nil {
				f.Close()
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("%w: %q has unsupported type %c", ErrUnsafePath, h.Name, h.Typeflag)
		}
	}

	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", err
	}
	src, name := stage, SanitizeName(fallbackName)
	if len(entries) == 1 {
		src, name = filepath.Join(stage, entries[0].Name()), entries[0].Name()
	}
	return publish(src, destDir, name)
}

// safeJoin resolves an archive entry name under root, rejecting traversal.
func safeJoin(root, name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

// SanitizeName reduces a client-supplied name to a safe single path element.
func SanitizeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		return "file"
	}
	return name
}

// SaveFile streams r into destDir/name via a temp file, publishing atomically
// under a collision-free name. A failed or truncated upload leaves nothing behind.
func SaveFile(r io.Reader, destDir, name string) (string, error) {
	return SaveFileLimited(r, destDir, name, Limits{})
}

// SaveFileLimited is SaveFile that refuses more than lim.MaxBytes.
func SaveFileLimited(r io.Reader, destDir, name string, lim Limits) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(destDir, ".partial-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if lim.MaxBytes > 0 {
		r = &capReader{r: r, left: lim.MaxBytes}
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	return publish(tmp.Name(), destDir, SanitizeName(name))
}

// publish moves src into dir as name, appending " (n)" until the name is free.
// Files are hard-linked into place (link fails if the name exists, so an
// existing file is never overwritten, even by a concurrent publish) and the
// caller removes the source. Directories use Rename, which fails if the target
// is a non-empty directory, so they are never merged into an existing one.
func publish(src, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		dst := filepath.Join(dir, candidate)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if info.IsDir() {
			if err := os.Rename(src, dst); err != nil {
				if errors.Is(err, fs.ErrExist) {
					continue
				}
				return "", err
			}
			return dst, nil
		}
		// Link then unlink gives an atomic no-overwrite publish for files.
		if err := os.Link(src, dst); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", err
		}
		return dst, nil
	}
	return "", errors.New("could not find a free file name")
}

// capReader fails with ErrTooLarge once more than left bytes have been read.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, ErrTooLarge
	}
	return n, err
}

// checkName rejects archive entry names carrying control characters.
func checkName(name string) error {
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q has control characters", ErrUnsafePath, name)
		}
	}
	return nil
}

// SweepPartials deletes leftovers of crashed transfers: .partial-* files and
// staging directories inside dir and its per-phone folders that were last
// modified before cutoff and are not named in keep (live upload sessions).
func SweepPartials(dir string, cutoff time.Time, keep map[string]bool) (removed int) {
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if strings.HasPrefix(e.Name(), ".partial-") {
				if info, err := e.Info(); err == nil && !keep[e.Name()] && info.ModTime().Before(cutoff) && os.RemoveAll(p) == nil {
					removed++
				}
			} else if e.IsDir() && depth < 1 {
				walk(p, depth+1)
			}
		}
	}
	walk(dir, 0)
	return removed
}
