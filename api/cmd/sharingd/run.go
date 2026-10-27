package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"api/internal/clipboard"
	"api/internal/config"
	"api/internal/control"
	"api/internal/discovery"
	"api/internal/protocol"
	"api/internal/server"
	"api/internal/transfer"
)

func run() error {
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	if d := os.Getenv("SHARING_CONFIG_DIR"); d != "" {
		dir = d
	}
	store, err := config.Open(dir)
	if err != nil {
		return err
	}
	cert, err := store.Certificate()
	if err != nil {
		return err
	}
	srv := server.New(store)
	svc := transfer.Mount(srv)

	httpSrv := server.NewHTTPServer(":"+strconv.Itoa(store.Settings().Port), srv.Handler())
	httpSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	sockPath := control.SocketPath()
	sock, err := control.Listen(sockPath)
	if err != nil {
		if errors.Is(err, control.ErrAlreadyRunning) {
			return fmt.Errorf("%w (check: systemctl --user status sharingd)", err)
		}
		return err
	}
	ctrl := &http.Server{Handler: control.Handler(srv, svc, config.Fingerprint(cert)), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 3)
	go func() { errc <- httpSrv.ListenAndServeTLS("", "") }()
	go func() { errc <- ctrl.Serve(sock) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	set := store.Settings()
	go func() {
		errc <- discovery.Advertise(ctx, discovery.Info{ID: set.DeviceID, Name: set.Name, Fingerprint: config.Fingerprint(cert), Port: set.Port})
	}()
	clip := clipboard.NewManager(srv.Hub)
	srv.OnMessage = func(peerID string, m protocol.Message) {
		svc.HandleMessage(peerID, m)
		clip.HandleMessage(ctx, peerID, m)
	}
	go clip.Run(ctx)
	go svc.Maintain(ctx.Done())
	slog.Info("sharingd listening", "addr", httpSrv.Addr, "name", set.Name)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	_ = ctrl.Shutdown(sctx)
	_ = os.Remove(sockPath)
	return nil
}
