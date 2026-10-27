// Package discovery advertises the daemon on the LAN via mDNS so the phone can
// find it without a fixed IP.
package discovery

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	Service = "_sharing._tcp"
	Domain  = "local."

	// pollInterval is how often the interface set is checked for changes.
	pollInterval = 10 * time.Second
)

// Info is published in TXT records and returned by Browse.
type Info struct {
	ID          string
	Name        string
	Fingerprint string
	Port        int
}

func (i Info) txt() []string {
	// Only a fingerprint prefix: the phone verifies the full value on connect.
	fp := i.Fingerprint
	if len(fp) > 16 {
		fp = fp[:16]
	}
	return []string{"id=" + i.ID, "name=" + i.Name, "fp=" + fp}
}

// Advertise registers the service and re-registers whenever the machine's
// addresses change (WiFi roam, VPN up/down), until ctx is cancelled.
func Advertise(ctx context.Context, info Info) error {
	return advertise(ctx, info, pollInterval, currentAddrs)
}

func advertise(ctx context.Context, info Info, every time.Duration, addrs func() string) error {
	var srv *zeroconf.Server
	stop := func() {
		if srv != nil {
			srv.Shutdown()
			srv = nil
		}
	}
	defer stop()

	var last string
	register := func() error {
		stop()
		s, err := zeroconf.Register(info.ID, Service, Domain, info.Port, info.txt(), nil)
		if err != nil {
			return err
		}
		srv = s
		return nil
	}

	last = addrs()
	if err := register(); err != nil {
		return err
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			// Without addresses there is nothing useful to announce; retry next tick.
			if now := addrs(); now != last {
				last = now
				if now == "" {
					stop()
				} else if err := register(); err != nil {
					return err
				}
			}
		}
	}
}

// Browse returns the first matching service seen before ctx ends. It backs
// `sharingd doctor` and the tests; the phone does the same via NsdManager.
func Browse(ctx context.Context, match func(Info) bool) (Info, string, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return Info{}, "", err
	}
	entries := make(chan *zeroconf.ServiceEntry)
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := resolver.Browse(bctx, Service, Domain, entries); err != nil {
		return Info{}, "", err
	}
	for {
		select {
		case e := <-entries:
			if e == nil {
				continue
			}
			info := parse(e)
			if match(info) {
				host := ""
				if len(e.AddrIPv4) > 0 {
					host = e.AddrIPv4[0].String()
				}
				return info, host, nil
			}
		case <-bctx.Done():
			return Info{}, "", bctx.Err()
		}
	}
}

func parse(e *zeroconf.ServiceEntry) Info {
	info := Info{Port: e.Port}
	for _, kv := range e.Text {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "id":
			info.ID = v
		case "name":
			info.Name = v
		case "fp":
			info.Fingerprint = v
		}
	}
	return info
}

// currentAddrs returns a stable string of the up, non-loopback unicast addresses.
func currentAddrs() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			out = append(out, ifc.Name+"="+a.String())
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
