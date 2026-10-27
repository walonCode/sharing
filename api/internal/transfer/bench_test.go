package transfer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const benchBytes = 256 << 20

func BenchmarkSaveFile(b *testing.B) {
	dest := b.TempDir()
	b.SetBytes(benchBytes)
	for b.Loop() {
		p, err := SaveFile(io.LimitReader(zeroReader{}, benchBytes), dest, "big.bin")
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		_ = removeFile(p)
		b.StartTimer()
	}
}

func smallFolder(b *testing.B, n int) string {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "many")
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("d%02d", i%50), fmt.Sprintf("f%05d.txt", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("small file content"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

func BenchmarkPackSmallFiles(b *testing.B) {
	dir := smallFolder(b, 10000)
	for b.Loop() {
		if err := Pack(io.Discard, dir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnpackSmallFiles(b *testing.B) {
	dir := smallFolder(b, 10000)
	var buf bytes.Buffer
	if err := Pack(&buf, dir); err != nil {
		b.Fatal(err)
	}
	dest := b.TempDir()
	for b.Loop() {
		got, err := Unpack(bytes.NewReader(buf.Bytes()), dest, "x")
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		_ = removeAll(got)
		b.StartTimer()
	}
}

func BenchmarkBuildManifest(b *testing.B) {
	dir := smallFolder(b, 10000)
	for b.Loop() {
		if _, err := BuildManifest(dir, 0); err != nil {
			b.Fatal(err)
		}
	}
}
