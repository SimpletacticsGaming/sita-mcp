package auth

import (
	"testing"
	"time"
)

func TestLimiterWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(2, 10*time.Minute)
	l.now = func() time.Time { return now }

	l.Fail("k")
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("blocked after one failure")
	}
	now = now.Add(time.Minute)
	l.Fail("k")
	blocked, retry := l.Blocked("k")
	if !blocked || retry != 9*time.Minute {
		t.Fatalf("blocked=%v retry=%v, want true 9m", blocked, retry)
	}

	now = now.Add(9 * time.Minute)
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("still blocked after the window")
	}

	l.Fail("k")
	l.Reset("k")
	l.Fail("k")
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("reset did not clear failures")
	}
}

func TestLimiterSweep(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(5, time.Minute)
	l.now = func() time.Time { return now }
	for _, k := range []string{"a", "b", "c"} {
		l.Fail(k)
	}
	now = now.Add(2 * time.Minute)
	l.Fail("d")
	if len(l.entries) != 1 {
		t.Fatalf("entries = %d, want 1 after sweep", len(l.entries))
	}
}
