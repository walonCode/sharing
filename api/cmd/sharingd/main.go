// Command sharingd is the laptop-side sharing daemon and its CLI.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"api/internal/config"
	"api/internal/control"
	"api/internal/discovery"
	"api/internal/server"
	"api/internal/transfer"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "run":
		err = run()
	case "pair":
		err = pair()
	case "send":
		err = send(os.Args[2:])
	case "peers":
		err = peers()
	default:
		err = fmt.Errorf("unknown command %q (run, pair, peers, send)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharingd:", err)
		os.Exit(1)
	}
}

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

	httpSrv := &http.Server{
		Addr:      ":" + strconv.Itoa(store.Settings().Port),
		Handler:   srv.Handler(),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
	}
	sock, err := control.Listen(control.SocketPath())
	if err != nil {
		return err
	}
	ctrl := &http.Server{Handler: control.Handler(srv, svc, config.Fingerprint(cert))}

	errc := make(chan error, 3)
	go func() { errc <- httpSrv.ListenAndServeTLS("", "") }()
	go func() { errc <- ctrl.Serve(sock) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	set := store.Settings()
	go func() {
		errc <- discovery.Advertise(ctx, discovery.Info{ID: set.DeviceID, Name: set.Name, Fingerprint: config.Fingerprint(cert), Port: set.Port})
	}()
	log.Printf("sharingd listening on %s as %q", httpSrv.Addr, store.Settings().Name)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	_ = ctrl.Shutdown(sctx)
	_ = os.Remove(control.SocketPath())
	return nil
}

func pair() error {
	resp, err := control.Client(control.SocketPath()).Post("http://sharingd/invite", "application/json", nil)
	if err != nil {
		return fmt.Errorf("is the daemon running? %w", err)
	}
	defer resp.Body.Close()
	var out control.InviteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	fmt.Println("Open the app on your phone and enter this pairing link (valid 5 minutes):")
	fmt.Println(out.URL)
	return nil
}

func peers() error {
	resp, err := control.Client(control.SocketPath()).Get("http://sharingd/peers")
	if err != nil {
		return fmt.Errorf("is the daemon running? %w", err)
	}
	defer resp.Body.Close()
	var list []struct {
		ID, Name string
		Online   bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	for _, p := range list {
		state := "offline"
		if p.Online {
			state = "online"
		}
		fmt.Printf("%s\t%s\t%s\n", p.ID, p.Name, state)
	}
	return nil
}

// send asks the running daemon to offer a file or folder to a paired phone.
// Usage: sharingd send [--to <id|name>] <path>...
func send(args []string) error {
	var to string
	var paths []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--to" && i+1 < len(args) {
			to = args[i+1]
			i++
			continue
		}
		paths = append(paths, args[i])
	}
	if len(paths) == 0 {
		return fmt.Errorf("usage: sharingd send [--to <peer>] <path>...")
	}
	client := control.Client(control.SocketPath())
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(control.SendRequest{Path: abs, Peer: to})
		resp, err := client.Post("http://sharingd/send", "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("is the daemon running? %w", err)
		}
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			return fmt.Errorf("%s: %s", p, strings.TrimSpace(string(msg)))
		}
		fmt.Println("offered", abs)
	}
	return nil
}
