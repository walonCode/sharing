// Package retry is the one retry policy shared by the daemon, the CLI and
// (mirrored in Kotlin) the phone: exponential backoff with full jitter, a
// counter that resets whenever a transfer makes progress, and a classifier
// that decides which outcomes can succeed later.
package retry

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Action says what to do after a failed attempt.
type Action int

const (
	// Backoff: wait, then try again (resuming from the server's offset).
	Backoff Action = iota
	// Now: try again immediately (an offset mismatch carries the right offset).
	Now
	// Restart: start the file again from byte 0, once; a second time it is a failure.
	Restart
	// Auth: stop; the device token is no longer accepted (pair again).
	Auth
	// Drop: stop and forget the item (offer gone, expired, or revoked).
	Drop
	// Permanent: stop with a reason (too large, bad name, disk full).
	Permanent
	// GiveUp: the attempt or time budget is spent.
	GiveUp
)

func (a Action) String() string {
	return [...]string{"backoff", "now", "restart", "auth", "drop", "permanent", "give-up"}[a]
}

// Outcome describes one finished attempt.
type Outcome struct {
	Status     int           // HTTP status; 0 when there was no response
	Err        error         // transport error; nil when a response arrived
	RetryAfter time.Duration // parsed Retry-After, if any
	Progress   bool          // the stored offset advanced during this attempt
	Patch      bool          // the request was a PATCH of an upload
	Ranged     bool          // the request carried a Range header
}

// OK reports a successful attempt.
func (o Outcome) OK() bool { return o.Err == nil && o.Status >= 200 && o.Status < 300 }

// Classify applies the table: which outcomes are worth retrying.
func Classify(o Outcome) Action {
	if o.Err != nil {
		if errors.Is(o.Err, context.Canceled) {
			return Drop
		}
		return Backoff // IO error, timeout, reset, stall
	}
	switch s := o.Status; {
	case s == http.StatusRequestTimeout, s == http.StatusTooManyRequests,
		s == http.StatusBadGateway, s == http.StatusServiceUnavailable, s == http.StatusGatewayTimeout,
		s == http.StatusLocked: // another request still holds the upload
		return Backoff
	case s == http.StatusConflict && o.Patch:
		return Now
	case (s == http.StatusPreconditionFailed || s == http.StatusRequestedRangeNotSatisfiable) && o.Ranged:
		return Restart
	case s == http.StatusUnauthorized:
		return Auth
	case s == http.StatusForbidden, s == http.StatusNotFound, s == http.StatusGone:
		return Drop
	case s == http.StatusRequestEntityTooLarge, s == http.StatusUnprocessableEntity, s == http.StatusInsufficientStorage:
		return Permanent
	case s >= 500:
		return Backoff
	default:
		return Permanent
	}
}

// ParseRetryAfter reads a Retry-After header holding seconds; anything else is zero.
func ParseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// Policy configures the backoff. The zero value is not usable; see Default.
type Policy struct {
	Base        time.Duration
	Cap         time.Duration
	MaxAttempts int           // attempts without progress before giving up
	MaxElapsed  time.Duration // total time in the queue before giving up
	// Jitter returns a value in [0, max]. Tests inject a fixed source.
	Jitter func(max time.Duration) time.Duration
	Now    func() time.Time
}

// Default is the policy from the plan: base 1s, factor 2, cap 60s, full jitter,
// 8 attempts without progress or 24 hours, whichever comes first.
func Default() Policy {
	return Policy{
		Base: time.Second, Cap: time.Minute, MaxAttempts: 8, MaxElapsed: 24 * time.Hour,
		Jitter: func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(max) + 1))
		},
		Now: time.Now,
	}
}

// State tracks one item through its attempts.
type State struct {
	Attempts  int // consecutive attempts without progress
	Started   time.Time
	Restarted bool
}

// Decision is what to do next and how long to wait first.
type Decision struct {
	Action Action
	Delay  time.Duration
}

// Ceiling is the un-jittered backoff for the nth consecutive failure (n >= 1).
func (p Policy) Ceiling(n int) time.Duration {
	d := p.Base
	for i := 1; i < n && d < p.Cap; i++ {
		d *= 2
	}
	return min(d, p.Cap)
}

// Next classifies the outcome and updates st. A Backoff decision carries the
// jittered delay (never shorter than Retry-After); attempts reset on progress.
func (p Policy) Next(st *State, o Outcome) Decision {
	if st.Started.IsZero() {
		st.Started = p.Now()
	}
	if o.OK() {
		return Decision{Action: Now}
	}
	act := Classify(o)
	if o.Progress {
		st.Attempts = 0
	}
	switch act {
	case Restart:
		if st.Restarted {
			return Decision{Action: Permanent}
		}
		st.Restarted = true
		return Decision{Action: Restart}
	case Backoff:
		st.Attempts++
		if st.Attempts > p.MaxAttempts || p.Now().Sub(st.Started) > p.MaxElapsed {
			return Decision{Action: GiveUp}
		}
		return Decision{Action: Backoff, Delay: max(p.Jitter(p.Ceiling(st.Attempts)), o.RetryAfter)}
	case Now:
		st.Attempts++
		if st.Attempts > p.MaxAttempts {
			return Decision{Action: GiveUp}
		}
		return Decision{Action: Now}
	}
	return Decision{Action: act}
}

// ErrGaveUp wraps the last failure when Do stops retrying.
var ErrGaveUp = errors.New("giving up")

// Do runs fn until it succeeds or the policy says stop. fn reports each
// attempt's Outcome. sleep may be nil (uses a timer honouring ctx).
func Do(ctx context.Context, p Policy, fn func(attempt int) Outcome, sleep func(context.Context, time.Duration) error) error {
	if sleep == nil {
		sleep = Sleep
	}
	var st State
	for attempt := 1; ; attempt++ {
		o := fn(attempt)
		if o.OK() {
			return nil
		}
		d := p.Next(&st, o)
		switch d.Action {
		case Backoff:
			if err := sleep(ctx, d.Delay); err != nil {
				return err
			}
		case Now, Restart:
		default:
			if o.Err != nil {
				return errors.Join(d.error(), o.Err)
			}
			return errors.Join(d.error(), errors.New("http status "+strconv.Itoa(o.Status)))
		}
	}
}

func (d Decision) error() error {
	if d.Action == GiveUp {
		return ErrGaveUp
	}
	return errors.New("not retryable (" + d.Action.String() + ")")
}

// Sleep waits d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Transient reports whether err looks like a network hiccup rather than a bug.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.As(err, &ne)
}
