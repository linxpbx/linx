package webhook

import (
	"testing"
	"time"
)

func TestRetrySchedule(t *testing.T) {
	if MaxAttempts != len(retryDelays)+1 {
		t.Fatalf("MaxAttempts = %d, want %d", MaxAttempts, len(retryDelays)+1)
	}
	var total time.Duration
	for _, d := range retryDelays {
		total += d
	}
	if total < 27*time.Hour || total > 28*time.Hour {
		t.Errorf("retries span %v, want about 27 h (docs/API.md §4)", total)
	}
	now := time.Now()
	for n := 1; n < MaxAttempts; n++ {
		next, ok := NextRetry(n, MaxAttempts, now)
		d, want := next.Sub(now), retryDelays[n-1]
		if !ok || d < want*9/10 || d > want*11/10 {
			t.Errorf("after attempt %d: retry in %v, want %v ±10%%", n, d, want)
		}
	}
	if _, ok := NextRetry(MaxAttempts, MaxAttempts, now); ok {
		t.Error("a retry after the last attempt")
	}
	if _, ok := NextRetry(1, 1, now); ok {
		t.Error("a one-attempt delivery (test message) was retried")
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText([]byte("ok\x00\xffend")); got != "ok��end" {
		t.Errorf("cleanText() = %q", got)
	}
}
