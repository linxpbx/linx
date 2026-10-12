// Package livekit is Linx's side of its video server, LiveKit (ADR-002,
// ADR-088): the server's configuration, the access tokens the control plane
// alone mints, and the one question Linx asks of a room — who is in it.
//
// LiveKit's signalling reaches the outside world only through the control
// plane (`/livekit/`, services/control-plane/livekit.go) and its media only
// through coturn on linx-media: it publishes no port of its own.
package livekit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Key is the API key's name. There is one key, and only the control plane
// and LiveKit hold its secret (Docker secret linx_livekit_secret).
const Key = "linx"

// Ports inside linx-livekit. Neither is published.
const (
	// SignalPort is LiveKit's HTTP and websocket port, on linx-private.
	SignalPort = 7880
	// MediaPort is the one UDP port all of LiveKit's media uses, on
	// linx-media, where coturn relays to it.
	MediaPort = 7882
)

// MaxPeople is the most people one group call holds (ADR-088): plenty for
// a small office, and a small server's limit for pictures it forwards.
const MaxPeople = 8

// DefaultMediaHost is LiveKit's alias on linx-media (compose.yaml), which is
// also how coturn finds it.
const DefaultMediaHost = "linx-livekit-media"

// DefaultURL is how the control plane reaches LiveKit on linx-private.
const DefaultURL = "http://linx-livekit:7880"

// Config renders LiveKit's configuration for the address it is reached on.
// Everything LiveKit offers clients is that one address and port; the
// clients' own relay (coturn) is how they get there, so LiveKit's TURN and
// its STUN lookups of an outside address stay off.
func Config(mediaIP string) string {
	return fmt.Sprintf(`# Rendered by linx-livekit-entrypoint at start (internal/livekit).
port: %d
bind_addresses: ["0.0.0.0"]
rtc:
  udp_port: %d
  tcp_port: 0
  use_external_ip: false
  node_ip: %s
  ips:
    includes: ["%s/32"]
  enable_loopback_candidate: false
turn:
  enabled: false
room:
  auto_create: true
  empty_timeout: 60
  departure_timeout: 20
  max_participants: %d
logging:
  level: warn
  json: true
`, SignalPort, MediaPort, mediaIP, mediaIP, MaxPeople)
}

// Grant is what a token lets its holder do (LiveKit's VideoGrant).
type Grant struct {
	Room         string `json:"room,omitempty"`
	RoomJoin     bool   `json:"roomJoin,omitempty"`
	RoomAdmin    bool   `json:"roomAdmin,omitempty"`
	CanPublish   *bool  `json:"canPublish,omitempty"`
	CanSubscribe *bool  `json:"canSubscribe,omitempty"`
	// CanPublishData is always false: a group call carries pictures and
	// sound, nothing else.
	CanPublishData *bool `json:"canPublishData,omitempty"`
}

// Token signs a LiveKit access token (HS256, as LiveKit requires) for
// identity, shown as name, with grant, good for ttl.
func Token(secret []byte, identity, name string, grant Grant, ttl time.Duration, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("no LiveKit secret")
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   Key,
		"sub":   identity,
		"name":  name,
		"nbf":   now.Add(-10 * time.Second).Unix(),
		"exp":   now.Add(ttl).Unix(),
		"video": grant,
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// JoinGrant is a person's grant for one room: in, sending their camera and
// microphone, and hearing and seeing everyone else.
func JoinGrant(room string) Grant {
	yes, no := true, false
	return Grant{Room: room, RoomJoin: true, CanPublish: &yes, CanSubscribe: &yes, CanPublishData: &no}
}

// Rooms asks LiveKit about rooms over its own API (Twirp, JSON), on
// linx-private.
type Rooms struct {
	URL    string
	Secret []byte
	HTTP   *http.Client
	Now    func() time.Time
}

// Participants returns the identities of everyone in room now.
func (r *Rooms) Participants(ctx context.Context, room string) ([]string, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	token, err := Token(r.Secret, "linx-control-plane", "", Grant{Room: room, RoomAdmin: true}, time.Minute, now())
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"room": room})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(r.URL, "/")+"/twirp/livekit.RoomService/ListParticipants", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LiveKit answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Participants []struct {
			Identity string `json:"identity"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Participants))
	for _, p := range out.Participants {
		ids = append(ids, p.Identity)
	}
	return ids, nil
}
