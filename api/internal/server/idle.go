package server

import (
	"io"
	"net/http"
	"time"
)

// Timeouts of the public listener. They are variables only so tests can
// shorten them; nothing changes them at run time.
var (
	// IdleTimeout is how long a transfer may go without moving a byte.
	IdleTimeout = 30 * time.Second
	// HeaderTimeout bounds reading a request's headers. net/http also applies it to the TLS
	// handshake, so a client that connects and says nothing is cut after the same time.
	HeaderTimeout = 10 * time.Second
)

// withIdleDeadline cuts a request whose peer stalls. A global ReadTimeout or
// WriteTimeout would kill large transfers that are merely long, so instead the
// read and write deadlines are pushed forward on every read and write: a
// stalled peer is dropped after d, a slow but steady one is never.
func withIdleDeadline(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(d))
		_ = rc.SetWriteDeadline(time.Now().Add(d))
		r.Body = &idleBody{ReadCloser: r.Body, rc: rc, d: d}
		next.ServeHTTP(&idleWriter{ResponseWriter: w, rc: rc, d: d}, r)
	})
}

type idleBody struct {
	io.ReadCloser
	rc *http.ResponseController
	d  time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.d))
	return b.ReadCloser.Read(p)
}

type idleWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
	d  time.Duration
}

func (w *idleWriter) Write(p []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.d))
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController (and sendfile via io.Copy's ReaderFrom) reach the real writer.
func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// NewHTTPServer returns an http.Server with header, idle, and size limits for
// a listener that strangers on the LAN can reach. Per-request body timeouts come from
// withIdleDeadline instead of ReadTimeout/WriteTimeout.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: HeaderTimeout,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
}
