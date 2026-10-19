package auth

import (
	"testing"
	"time"
)

func TestEnrollTokenTTLIsTenMinutesUnlessADemoServerSaysOtherwise(t *testing.T) {
	if got := EnrollTokenTTL(); got != DefaultEnrollTokenTTL {
		t.Errorf("with nothing set: %s, want %s", got, DefaultEnrollTokenTTL)
	}
	// Only a demo server sets this, so App Review can open the app a day
	// after the build was submitted (docs/ops/STORE_SUBMISSION.md §1).
	for raw, want := range map[string]time.Duration{
		"72h":     72 * time.Hour,
		"30d":     DefaultEnrollTokenTTL, // not a Go duration: ignored
		"rubbish": DefaultEnrollTokenTTL,
		"1m":      DefaultEnrollTokenTTL, // never shorter than the default
		"1000h":   MaxEnrollTokenTTL,     // never longer than a week
		"":        DefaultEnrollTokenTTL,
	} {
		t.Setenv("LINX_ENROLL_TOKEN_TTL", raw)
		if got := EnrollTokenTTL(); got != want {
			t.Errorf("LINX_ENROLL_TOKEN_TTL=%q: %s, want %s", raw, got, want)
		}
	}
}
