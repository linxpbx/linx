package trunk

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMatchETag(t *testing.T) {
	tests := []struct {
		ifMatch string
		version int
		want    bool
	}{
		{`"1"`, 1, true},
		{`"2"`, 1, false},
		{"*", 5, true},
		{`"1", "2"`, 2, true},
	}
	for _, tt := range tests {
		if got := matchETag(tt.ifMatch, tt.version); got != tt.want {
			t.Errorf("matchETag(%q, %d) = %v, want %v", tt.ifMatch, tt.version, got, tt.want)
		}
	}
}

func TestETagRoundTrip(t *testing.T) {
	if got := ETag(7); got != `"7"` {
		t.Errorf("ETag(7) = %q", got)
	}
}

func TestCheckHost(t *testing.T) {
	for _, ok := range []string{"sip.example.com", "10.0.0.1"} {
		if err := checkHost(ok); err != nil {
			t.Errorf("checkHost(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "has space", string(make([]byte, 256))} {
		if err := checkHost(bad); err == nil {
			t.Errorf("checkHost(%q) succeeded, want an error", bad)
		}
	}
}

func TestCheckPort(t *testing.T) {
	for _, ok := range []int{1, 5061, 65535} {
		if err := checkPort(ok); err != nil {
			t.Errorf("checkPort(%d) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []int{0, -1, 65536} {
		if err := checkPort(bad); err == nil {
			t.Errorf("checkPort(%d) succeeded, want an error", bad)
		}
	}
}

func TestCheckCodecs(t *testing.T) {
	got, err := checkCodecs(nil)
	if err != nil || len(got) != 2 || got[0] != "alaw" || got[1] != "ulaw" {
		t.Errorf("checkCodecs(nil) = %v, %v, want [alaw ulaw], nil", got, err)
	}
	got, err = checkCodecs([]string{"opus", "opus", "g722"})
	if err != nil || len(got) != 2 {
		t.Errorf("checkCodecs dedupe = %v, %v", got, err)
	}
	if _, err := checkCodecs([]string{"gsm"}); err == nil {
		t.Error("checkCodecs([gsm]) succeeded, want an error")
	}
}

func TestCheckMaxCalls(t *testing.T) {
	for _, ok := range []int{1, 4, 500} {
		if err := checkMaxCalls(ok); err != nil {
			t.Errorf("checkMaxCalls(%d) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []int{0, -1, 501} {
		if err := checkMaxCalls(bad); err == nil {
			t.Errorf("checkMaxCalls(%d) succeeded, want an error", bad)
		}
	}
}

func TestTrunkUnencrypted(t *testing.T) {
	wg := uuid.New()
	tests := []struct {
		name  string
		trunk Trunk
		want  bool
	}{
		{"tls+srtp", Trunk{Transport: TransportTLS, MediaEncryption: MediaSRTP}, false},
		{"tcp", Trunk{Transport: TransportTCP, MediaEncryption: MediaSRTP}, true},
		{"no media encryption", Trunk{Transport: TransportTLS, MediaEncryption: MediaNone}, true},
		{"unencrypted but through wireguard", Trunk{Transport: TransportUDP, MediaEncryption: MediaNone, WireGuardProfileID: &wg}, false},
	}
	for _, tt := range tests {
		if got := tt.trunk.Unencrypted(); got != tt.want {
			t.Errorf("%s: Unencrypted() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestApplyUnencrypted(t *testing.T) {
	now := time.Now()

	t.Run("encrypted trunk needs no confirmation", func(t *testing.T) {
		tr := Trunk{Transport: TransportTLS, MediaEncryption: MediaSRTP}
		if err := applyUnencrypted(&tr, false, "admin", now); err != nil {
			t.Fatalf("applyUnencrypted = %v, want nil", err)
		}
		if tr.UnencryptedConfirmedAt != nil {
			t.Error("encrypted trunk shouldn't carry a confirmation")
		}
	})

	t.Run("unencrypted trunk needs confirmation", func(t *testing.T) {
		tr := Trunk{Transport: TransportTCP, MediaEncryption: MediaNone}
		if err := applyUnencrypted(&tr, false, "admin", now); err == nil {
			t.Fatal("applyUnencrypted without confirmation succeeded, want an error")
		}
		if err := applyUnencrypted(&tr, true, "admin", now); err != nil {
			t.Fatalf("applyUnencrypted with confirmation = %v, want nil", err)
		}
		if tr.UnencryptedConfirmedBy != "admin" || tr.UnencryptedConfirmedAt == nil {
			t.Error("confirmation wasn't stamped")
		}
	})

	t.Run("already confirmed and still unencrypted needs no reconfirmation", func(t *testing.T) {
		confirmedAt := now.Add(-time.Hour)
		tr := Trunk{Transport: TransportTCP, MediaEncryption: MediaNone, UnencryptedConfirmedBy: "admin", UnencryptedConfirmedAt: &confirmedAt}
		if err := applyUnencrypted(&tr, false, "someone-else", now); err != nil {
			t.Fatalf("applyUnencrypted = %v, want nil (already confirmed)", err)
		}
		if tr.UnencryptedConfirmedBy != "admin" {
			t.Error("stale confirmation shouldn't be overwritten by a no-op call")
		}
	})

	t.Run("becoming encrypted clears a stale confirmation", func(t *testing.T) {
		confirmedAt := now.Add(-time.Hour)
		tr := Trunk{Transport: TransportTLS, MediaEncryption: MediaSRTP, UnencryptedConfirmedBy: "admin", UnencryptedConfirmedAt: &confirmedAt}
		if err := applyUnencrypted(&tr, false, "admin", now); err != nil {
			t.Fatalf("applyUnencrypted = %v, want nil", err)
		}
		if tr.UnencryptedConfirmedAt != nil || tr.UnencryptedConfirmedBy != "" {
			t.Error("confirmation should be cleared once the trunk is encrypted again")
		}
	})
}

func TestCheckDIDNumber(t *testing.T) {
	for _, ok := range []string{"0501234567", "+971501234567", "112"} {
		if err := checkDIDNumber(ok); err != nil {
			t.Errorf("checkDIDNumber(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "1", "abc", "+", "12345678901234567890123"} {
		if err := checkDIDNumber(bad); err == nil {
			t.Errorf("checkDIDNumber(%q) succeeded, want an error", bad)
		}
	}
}

func TestCheckAllowedCategories(t *testing.T) {
	got, err := checkAllowedCategories([]string{"mobile", "mobile", "national"})
	if err != nil || len(got) != 2 {
		t.Errorf("checkAllowedCategories dedupe = %v, %v", got, err)
	}
	if _, err := checkAllowedCategories([]string{"emergency"}); err == nil {
		t.Error(`checkAllowedCategories(["emergency"]) succeeded, want an error (emergency is never gated)`)
	}
	if _, err := checkAllowedCategories([]string{"bogus"}); err == nil {
		t.Error("checkAllowedCategories with an unknown category succeeded, want an error")
	}
}
