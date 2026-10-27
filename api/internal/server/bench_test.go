package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/internal/config"
	"api/internal/pairing"
	"api/internal/protocol"
)

func benchServer(b *testing.B, peers int) (*Server, string) {
	b.Helper()
	store, err := config.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	s := New(store)
	var last string
	for i := 0; i < peers; i++ {
		last = pairing.NewToken()
		if err := store.AddPeer(config.Peer{ID: fmt.Sprintf("device-%04d", i), Name: "p", TokenHash: pairing.HashToken(last)}); err != nil {
			b.Fatal(err)
		}
	}
	return s, last
}

// BenchmarkAuth measures the bearer-token check on the last peer (worst case for a scan).
func BenchmarkAuth(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("peers=%d", n), func(b *testing.B) {
			s, token := benchServer(b, n)
			h := s.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				h.ServeHTTP(w, req)
			}
		})
	}
}

// BenchmarkClipMarshal is the per-peer cost wsjson.Write pays to encode a
// clip; Broadcast does it once per connected peer.
func BenchmarkClipMarshal(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			m := protocol.Message{Type: protocol.TypeClip, Mime: "text/plain", Data: strings.Repeat("a", size), TS: 1}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := json.Marshal(m); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
