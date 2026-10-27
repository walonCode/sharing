package retry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name string
		o    Outcome
		want Action
	}{
		{"io error", Outcome{Err: errors.New("connection reset")}, Backoff},
		{"canceled", Outcome{Err: context.Canceled}, Drop},
		{"408", Outcome{Status: 408}, Backoff},
		{"429", Outcome{Status: 429}, Backoff},
		{"502", Outcome{Status: 502}, Backoff},
		{"503", Outcome{Status: 503}, Backoff},
		{"504", Outcome{Status: 504}, Backoff},
		{"423 locked", Outcome{Status: 423}, Backoff},
		{"409 patch", Outcome{Status: 409, Patch: true}, Now},
		{"409 other", Outcome{Status: 409}, Permanent},
		{"412 range", Outcome{Status: 412, Ranged: true}, Restart},
		{"416 range", Outcome{Status: 416, Ranged: true}, Restart},
		{"412 no range", Outcome{Status: 412}, Permanent},
		{"401", Outcome{Status: 401}, Auth},
		{"403", Outcome{Status: 403}, Drop},
		{"404", Outcome{Status: 404}, Drop},
		{"410", Outcome{Status: 410}, Drop},
		{"413", Outcome{Status: 413}, Permanent},
		{"422", Outcome{Status: 422}, Permanent},
		{"507", Outcome{Status: 507}, Permanent},
		{"500", Outcome{Status: 500}, Backoff},
	}
	for _, c := range cases {
		if got := Classify(c.o); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func fixed(p Policy) Policy {
	p.Jitter = func(max time.Duration) time.Duration { return max } // worst case
	now := time.Unix(0, 0)
	p.Now = func() time.Time { return now }
	return p
}

func TestBackoffBounds(t *testing.T) {
	p := fixed(Default())
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := p.Ceiling(i + 1); got != w*time.Second {
			t.Errorf("Ceiling(%d) = %v, want %v", i+1, got, w*time.Second)
		}
	}
	// Full jitter stays inside [0, ceiling].
	d := Default()
	for n := 1; n <= 10; n++ {
		for range 200 {
			if got := d.Jitter(d.Ceiling(n)); got < 0 || got > d.Ceiling(n) {
				t.Fatalf("jitter %v outside [0,%v]", got, d.Ceiling(n))
			}
		}
	}
}

func TestAttemptsResetOnProgress(t *testing.T) {
	p := fixed(Default())
	var st State
	for i := 0; i < 7; i++ {
		if d := p.Next(&st, Outcome{Err: errors.New("x")}); d.Action != Backoff {
			t.Fatalf("attempt %d = %v", i, d.Action)
		}
	}
	// Progress resets the counter, so another long run of failures is allowed.
	if d := p.Next(&st, Outcome{Err: errors.New("x"), Progress: true}); d.Action != Backoff || d.Delay != time.Second {
		t.Fatalf("after progress: %+v", d)
	}
	for i := 0; i < 7; i++ {
		p.Next(&st, Outcome{Err: errors.New("x")})
	}
	if d := p.Next(&st, Outcome{Err: errors.New("x")}); d.Action != GiveUp {
		t.Fatalf("8 attempts without progress should give up, got %v", d.Action)
	}
}

func TestGiveUpAfterElapsed(t *testing.T) {
	p := Default()
	now := time.Unix(0, 0)
	p.Now = func() time.Time { return now }
	p.Jitter = func(time.Duration) time.Duration { return 0 }
	var st State
	p.Next(&st, Outcome{Err: errors.New("x")})
	now = now.Add(25 * time.Hour)
	if d := p.Next(&st, Outcome{Err: errors.New("x")}); d.Action != GiveUp {
		t.Fatalf("got %v", d.Action)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	p := fixed(Default())
	var st State
	d := p.Next(&st, Outcome{Status: 503, RetryAfter: 30 * time.Second})
	if d.Action != Backoff || d.Delay != 30*time.Second {
		t.Fatalf("%+v", d)
	}
	if got := ParseRetryAfter("7"); got != 7*time.Second {
		t.Fatal(got)
	}
	if ParseRetryAfter("soon") != 0 || ParseRetryAfter("-1") != 0 {
		t.Fatal("bad values must parse as zero")
	}
}

func TestRestartOnlyOnce(t *testing.T) {
	p := fixed(Default())
	var st State
	if d := p.Next(&st, Outcome{Status: 412, Ranged: true}); d.Action != Restart {
		t.Fatal(d)
	}
	if d := p.Next(&st, Outcome{Status: 412, Ranged: true}); d.Action != Permanent {
		t.Fatalf("second 412 = %v", d.Action)
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	p := fixed(Default())
	var slept []time.Duration
	calls := 0
	err := Do(context.Background(), p, func(int) Outcome {
		calls++
		if calls < 3 {
			return Outcome{Status: http.StatusServiceUnavailable}
		}
		return Outcome{Status: http.StatusOK}
	}, func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil })
	if err != nil || calls != 3 || len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("err=%v calls=%d slept=%v", err, calls, slept)
	}
}

func TestDoStopsOnPermanent(t *testing.T) {
	err := Do(context.Background(), fixed(Default()), func(int) Outcome { return Outcome{Status: 413} }, func(context.Context, time.Duration) error { return nil })
	if err == nil {
		t.Fatal("413 must not be retried")
	}
}
