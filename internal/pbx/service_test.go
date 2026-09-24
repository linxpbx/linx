package pbx

import "testing"

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
		{`  "3"  `, 3, true},
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

func TestCheckNumber(t *testing.T) {
	for _, ok := range []string{"10", "101", "999999"} {
		if err := checkNumber(ok); err != nil {
			t.Errorf("checkNumber(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"1", "1234567", "10a", "", "-1", "1.5"} {
		if err := checkNumber(bad); err == nil {
			t.Errorf("checkNumber(%q) succeeded, want an error", bad)
		}
	}
}

func TestCheckDeviceKind(t *testing.T) {
	if err := checkDeviceKind(KindSoftphone); err != nil {
		t.Errorf("checkDeviceKind(softphone) = %v, want nil", err)
	}
	for _, kind := range []string{KindWeb, KindIOS, KindDesk, "bogus"} {
		if err := checkDeviceKind(kind); err == nil {
			t.Errorf("checkDeviceKind(%q) succeeded, want an error (not supported yet)", kind)
		}
	}
}

func TestSIPServer(t *testing.T) {
	s := &Service{Domain: "lab.linxpbx.com"}
	if got := s.SIPServer(); got != "sip.lab.linxpbx.com" {
		t.Errorf("SIPServer() = %q", got)
	}
}
