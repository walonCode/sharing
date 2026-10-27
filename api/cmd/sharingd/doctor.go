package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"api/internal/clipboard"
	"api/internal/config"
	"api/internal/control"
	"api/internal/discovery"
	"api/internal/transfer"
)

// check is one line of `sharingd doctor` output.
type check struct {
	name   string
	status string // ok, warn or FAIL
	detail string
}

func (c check) String() string { return fmt.Sprintf("%-5s %-18s %s", c.status, c.name, c.detail) }

// doctor prints what is and is not working, without needing the daemon to cooperate.
func doctor() error {
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
	failed := false
	for _, c := range runChecks(store) {
		fmt.Println(c)
		failed = failed || c.status == "FAIL"
	}
	if failed {
		return fmt.Errorf("some checks failed")
	}
	return nil
}

func runChecks(store *config.Store) []check {
	set := store.Settings()
	var out []check

	if err := call(http.MethodGet, "/peers", nil, nil); err != nil {
		out = append(out, check{"daemon", "FAIL", err.Error()})
	} else {
		out = append(out, check{"daemon", "ok", "answering on " + control.SocketPath()})
	}

	cert, err := store.Certificate()
	if err != nil {
		out = append(out, check{"certificate", "FAIL", err.Error()})
	} else {
		fp := config.Fingerprint(cert)
		out = append(out, check{"certificate", "ok", "fingerprint " + fp[:min(len(fp), 16)] + "..."})
		out = append(out, checkPort(set.Port, fp))
	}

	if b, err := clipboard.Detect(); err != nil {
		out = append(out, check{"clipboard", "warn", err.Error()})
	} else {
		out = append(out, check{"clipboard", "ok", clipboard.Describe(b)})
	}

	out = append(out, checkDisk(set))
	out = append(out, checkMDNS(set))
	return out
}

// checkPort dials the HTTPS port and compares the certificate it presents with ours.
func checkPort(port int, want string) check {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // we compare the fingerprint ourselves
	if err != nil {
		return check{"port", "FAIL", fmt.Sprintf("nothing answering TLS on :%d (%v)", port, err)}
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || config.FingerprintDER(certs[0].Raw) != want {
		return check{"port", "FAIL", fmt.Sprintf(":%d presents a different certificate than this config directory", port)}
	}
	return check{"port", "ok", fmt.Sprintf(":%d presents the expected certificate", port)}
}

func checkDisk(set config.Settings) check {
	free, err := transfer.FreeSpace(set.ReceiveDir)
	if err != nil {
		return check{"receive folder", "FAIL", err.Error()}
	}
	detail := fmt.Sprintf("%s, %s free", set.ReceiveDir, humanBytes(int64(free))) //nolint:gosec // free fits int64
	if int64(free) < set.MinFreeBytes {                                           //nolint:gosec // free fits int64
		return check{"receive folder", "warn", detail + " (below the reserve; uploads will be refused)"}
	}
	return check{"receive folder", "ok", detail}
}

// checkMDNS looks for our own advertisement on the network.
func checkMDNS(set config.Settings) check {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	info, host, err := discovery.Browse(ctx, func(i discovery.Info) bool { return i.ID == set.DeviceID })
	if err != nil {
		return check{"mdns", "warn", "advertisement not seen (the phone may need a manual address): " + err.Error()}
	}
	return check{"mdns", "ok", fmt.Sprintf("advertised as %q at %s", info.Name, host)}
}
