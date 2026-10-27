package server

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// CleanName validates a peer-supplied display name: trimmed, 1 to 64 runes,
// no control characters (they would let a peer inject terminal escapes into
// `sharingd peers` output, logs, and notifications).
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 {
		return "", errors.New("name must be 1 to 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == utf8.RuneError || unicode.Is(unicode.Cf, r) {
			return "", errors.New("name contains control characters")
		}
	}
	return name, nil
}

// CheckDeviceID validates a peer-supplied device id.
func CheckDeviceID(id string) error {
	if !deviceIDPattern.MatchString(id) {
		return errors.New("deviceId must be 8 to 64 letters, digits or dashes")
	}
	return nil
}
