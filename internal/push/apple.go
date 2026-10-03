package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Apple's two addresses. Everything goes over TLS 1.2+ to one of these and
// nowhere else; the client itself is the same guarded one webhooks use, so
// a name that resolved to this server's own network would be refused.
const (
	productionHost = "api.push.apple.com"
	sandboxHost    = "api.sandbox.push.apple.com"
)

// Key is the .p8 signing key Apple gave the owner, ready to sign with.
type Key struct {
	teamID, keyID string
	private       *ecdsa.PrivateKey

	mu      sync.Mutex
	token   string
	madeAt  time.Time
	nowFunc func() time.Time
}

// ParseKey reads an Apple .p8 file (a PKCS#8 EC private key in PEM). It is
// the only thing done with the file: it is never written anywhere but the
// sealed column it came from.
func ParseKey(teamID, keyID string, p8 []byte) (*Key, error) {
	block, _ := pem.Decode(bytes.TrimSpace(p8))
	if block == nil {
		return nil, errors.New("that file isn't a .p8 key (no PEM in it)")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("that .p8 key couldn't be read: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("that .p8 key isn't the kind Apple's push keys are")
	}
	return &Key{teamID: teamID, keyID: keyID, private: key}, nil
}

// token returns the provider token (a signed JWT) Apple wants on every
// request, making a new one when the one in hand is old. Apple refuses a
// token over an hour old, and refuses being handed new ones too often, so
// one token serves every push for TokenLife.
func (k *Key) provider(now time.Time) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && now.Sub(k.madeAt) < TokenLife {
		return k.token, nil
	}
	header := map[string]string{"alg": "ES256", "kid": k.keyID, "typ": "JWT"}
	claims := map[string]any{"iss": k.teamID, "iat": now.Unix()}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64url(headerJSON) + "." + base64url(claimsJSON)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", err
	}
	// ES256: r and s, 32 bytes each, nothing around them (RFC 7518 §3.4).
	signature := make([]byte, 64)
	copyInto(signature[:32], r)
	copyInto(signature[32:], s)
	k.token = signing + "." + base64url(signature)
	k.madeAt = now
	return k.token, nil
}

func copyInto(dst []byte, n *big.Int) {
	b := n.Bytes()
	if len(b) > len(dst) {
		b = b[len(b)-len(dst):]
	}
	copy(dst[len(dst)-len(b):], b)
}

func base64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// notice is one push on its way to Apple.
type notice struct {
	Token string
	// Topic is the app (the bundle id), with ".voip" on the end for a wake
	// push: Apple keeps the two apart.
	Topic      string
	PushType   string
	Priority   int
	Expiration int64
	Payload    []byte
}

// wakePayload is what a woken app is told: the call's id, the caller's
// number and the time. No name, no token, nothing else (docs/PHASE2.md §5).
func wakePayload(c Call) ([]byte, error) {
	return json.Marshal(map[string]any{
		"aps":  map[string]any{},
		"linx": map[string]any{"call": c.ID, "from": c.From, "at": c.At.Unix()},
	})
}

// quietPayload is a missed call or a new voicemail: the same bare facts,
// in the words the phone shows.
func quietPayload(kind, title, body string, badge *int) ([]byte, error) {
	aps := map[string]any{
		"alert": map[string]any{"title": title, "body": body},
		"sound": "default",
	}
	if badge != nil {
		aps["badge"] = *badge
	}
	return json.Marshal(map[string]any{"aps": aps, "linx": map[string]any{"kind": kind}})
}

// send delivers one push. A dead token comes back as *Dead, so the caller
// can forget it; everything else is an ordinary error and is retried by
// whatever happens next, never in a loop here.
func (g *Gateway) send(ctx context.Context, key *Key, environment string, n notice) error {
	provider, err := key.provider(g.now())
	if err != nil {
		return err
	}
	host := productionHost
	if environment == Sandbox {
		host = sandboxHost
	}
	if g.Host != "" {
		host = g.Host
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+host+"/3/device/"+n.Token, bytes.NewReader(n.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+provider)
	req.Header.Set("apns-topic", n.Topic)
	req.Header.Set("apns-push-type", n.PushType)
	req.Header.Set("apns-priority", strconv.Itoa(n.Priority))
	req.Header.Set("apns-expiration", strconv.FormatInt(n.Expiration, 10))
	req.Header.Set("content-type", "application/json")

	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var said struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &said)
	switch {
	case resp.StatusCode == http.StatusGone, said.Reason == "BadDeviceToken",
		said.Reason == "Unregistered", said.Reason == "DeviceTokenNotForTopic":
		return &Dead{Reason: said.Reason}
	case said.Reason != "":
		return fmt.Errorf("apple refused the push: %d %s", resp.StatusCode, said.Reason)
	default:
		return fmt.Errorf("apple refused the push: %d", resp.StatusCode)
	}
}
