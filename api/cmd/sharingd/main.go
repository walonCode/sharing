// Command sharingd is the laptop-side sharing daemon and its CLI.
package main

import (
	"fmt"
	"os"

	"api/internal/service"
)

const usage = "run, pair, peers, unpair, rotate, send, transfers, cancel, retry, setup, doctor, install, uninstall"

func main() {
	cmd, args := "run", []string(nil)
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	var err error
	switch cmd {
	case "run":
		err = run()
	case "pair":
		err = pair(args)
	case "setup":
		err = setup(args)
	case "doctor":
		err = doctor()
	case "install":
		err = install()
	case "uninstall":
		err = service.Uninstall()
	case "unpair":
		err = unpair(args)
	case "rotate":
		err = rotate(args)
	case "send":
		err = send(args)
	case "peers":
		err = peers()
	case "transfers":
		err = transfers()
	case "cancel":
		err = transferAction("cancel", args)
	case "retry":
		err = transferAction("retry", args)
	default:
		err = fmt.Errorf("unknown command %q (%s)", cmd, usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharingd:", err)
		os.Exit(1)
	}
}
