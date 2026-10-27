package transfer

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzSafeJoin(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "/abs", "a/../../b", `a\..\b`, ".", "..", "a//b", "a/./b", "\x00", "é/ü"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		root := "/srv/root"
		got, err := safeJoin(root, name)
		if err != nil {
			return
		}
		rel, rerr := filepath.Rel(root, got)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			t.Fatalf("safeJoin(%q) = %q escapes the root", name, got)
		}
	})
}

func FuzzSanitizeName(f *testing.F) {
	for _, s := range []string{"a.txt", "../../etc/passwd", `..\..\x`, "", ".", "..", "/", "a/b/", "x\x00y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := SanitizeName(name)
		if got == "" || got == "." || got == ".." || strings.ContainsAny(got, `/\`) && !strings.Contains(name, `\`) {
			t.Fatalf("SanitizeName(%q) = %q", name, got)
		}
		if path.Base(got) != got && !strings.Contains(got, `\`) {
			t.Fatalf("SanitizeName(%q) = %q is not a single element", name, got)
		}
	})
}

func FuzzUnpack(f *testing.F) {
	var seed bytes.Buffer
	tw := tar.NewWriter(&seed)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "d/a.txt", Size: 1, Mode: 0o644})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	f.Add(seed.Bytes())
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parent := t.TempDir()
		dest := filepath.Join(parent, "in")
		got, err := UnpackLimited(bytes.NewReader(data), dest, "x", Limits{MaxEntries: 50, MaxBytes: 1 << 20})
		if err == nil && filepath.Dir(got) != dest {
			t.Fatalf("published outside the destination: %s", got)
		}
		// Nothing may appear next to the destination, and failures leave no staging dirs.
		if left, _ := os.ReadDir(parent); len(left) > 1 {
			t.Fatalf("unexpected entries beside the destination: %v", left)
		}
		if err != nil {
			if left, _ := os.ReadDir(dest); len(left) != 0 {
				t.Fatalf("failed unpack left %v", left)
			}
		}
	})
}

func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(`{"name":"d","dirs":["a"],"files":[{"path":"a/b","size":1,"mtime":2}]}`))
	f.Add([]byte(`{"files":[{"path":"../x"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Manifest
		if json.Unmarshal(data, &m) != nil {
			return
		}
		if ParseManifest(m, 1000) != nil {
			return
		}
		for _, e := range m.Files {
			got, err := safeJoin("/root", e.Path)
			if err != nil || !strings.HasPrefix(got, "/root/") {
				t.Fatalf("accepted manifest path %q resolves to %q (%v)", e.Path, got, err)
			}
		}
	})
}
