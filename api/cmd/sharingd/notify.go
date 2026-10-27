package main

import (
	"fmt"
	"os"
	"os/exec"
)

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// notify reports the outcome of a send as a desktop notification, if possible.
func notify(count int, sendErr error) {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	title, body := "Sent to phone", fmt.Sprintf("%d item(s) offered", count)
	if sendErr != nil {
		title, body = "Could not send to phone", sendErr.Error()
	}
	_ = exec.Command(bin, "--app-name=sharing", title, body).Run() //nolint:gosec // bin comes from LookPath
}
