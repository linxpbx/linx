package livekit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTokenIsLiveKitsHS256(t *testing.T) {
	secret := []byte("a-long-shared-secret-for-the-test")
	now := time.Unix(1_800_000_000, 0)
	tok, err := Token(secret, "device-1", "Sara", JoinGrant("call-x"), 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("bad signature")
	}
	var header map[string]string
	b, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(b, &header)
	if header["alg"] != "HS256" {
		t.Errorf("header %v", header)
	}
	var claims struct {
		Iss, Sub, Name string
		Nbf, Exp       int64
		Video          map[string]any
	}
	b, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(b, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != Key || claims.Sub != "device-1" || claims.Name != "Sara" || claims.Exp != now.Add(5*time.Minute).Unix() {
		t.Errorf("claims %+v", claims)
	}
	v := claims.Video
	if v["room"] != "call-x" || v["roomJoin"] != true || v["canPublish"] != true || v["canSubscribe"] != true || v["canPublishData"] != false {
		t.Errorf("grant %v", v)
	}
	if _, ok := v["roomAdmin"]; ok {
		t.Error("a person's token is an admin's")
	}
	if _, err := Token(nil, "x", "", Grant{}, time.Minute, now); err == nil {
		t.Error("signed with no secret")
	}
}

func TestConfigOffersOnlyTheMediaAddress(t *testing.T) {
	c := Config("172.30.0.5")
	for _, want := range []string{"node_ip: 172.30.0.5", `includes: ["172.30.0.5/32"]`, "use_external_ip: false",
		"udp_port: 7882", "tcp_port: 0", "turn:\n  enabled: false", "max_participants: 8"} {
		if !strings.Contains(c, want) {
			t.Errorf("config missing %q:\n%s", want, c)
		}
	}
}

func TestParticipants(t *testing.T) {
	secret := []byte("s3cret-s3cret-s3cret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.RoomService/ListParticipants" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["room"] != "call-y" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"participants":[{"identity":"a"},{"identity":"b"}]}`))
	}))
	defer srv.Close()
	r := &Rooms{URL: srv.URL, Secret: secret}
	got, err := r.Participants(t.Context(), "call-y")
	if err != nil || strings.Join(got, ",") != "a,b" {
		t.Fatalf("participants %v %v", got, err)
	}
	if got, err := r.Participants(t.Context(), "call-none"); err != nil || len(got) != 0 {
		t.Fatalf("missing room %v %v", got, err)
	}
}
