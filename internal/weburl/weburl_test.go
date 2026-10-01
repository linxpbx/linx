package weburl

import (
	"strings"
	"testing"
)

func TestOrigin(t *testing.T) {
	for _, tc := range []struct {
		port int
		want string
	}{
		{0, "https://example.com"},
		{443, "https://example.com"},
		{8443, "https://example.com:8443"},
	} {
		if got := Origin("example.com", tc.port); got != tc.want {
			t.Errorf("Origin(%d) = %q, want %q", tc.port, got, tc.want)
		}
	}
}

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, ""},
		{map[string]string{"LINX_DOMAIN": "example.com"}, "https://example.com"},
		{map[string]string{"LINX_DOMAIN": "example.com", Env: "443"}, "https://example.com"},
		{map[string]string{"LINX_DOMAIN": "example.com", Env: "8443"}, "https://example.com:8443"},
		{map[string]string{"LINX_DOMAIN": "example.com", Env: "nope"}, "https://example.com"},
		{map[string]string{"LINX_DOMAIN": "example.com", Env: "70000"}, "https://example.com"},
	} {
		if got := FromEnv(env(tc.env)); got != tc.want {
			t.Errorf("FromEnv(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestProblem(t *testing.T) {
	for _, p := range []int{443, 1024, 4443, 8443, 9443, 65535} {
		if msg := Problem(p); msg != "" {
			t.Errorf("Problem(%d) = %q, want none", p, msg)
		}
	}
	for p, why := range map[int]string{
		80: "1024 to 65535", 1023: "1024 to 65535", 65536: "1024 to 65535",
		5060: "Linx itself", 5061: "Linx itself", 5064: "Linx itself", 5349: "Linx itself", 6464: "Linx itself",
		6000: "browsers", 6666: "browsers", 10080: "browsers",
	} {
		if msg := Problem(p); !strings.Contains(msg, why) {
			t.Errorf("Problem(%d) = %q, want it to say %q", p, msg, why)
		}
	}
}
