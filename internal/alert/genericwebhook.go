package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"linxpbx.com/linx/internal/version"
	"linxpbx.com/linx/internal/webhook"
)

// genericWebhookItem is one alert in the payload; a digest carries several.
type genericWebhookItem struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Link     string `json:"link,omitempty"`
	Resolved bool   `json:"resolved"`
}

type genericWebhookBody struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		Alerts []genericWebhookItem `json:"alerts"`
	} `json:"data"`
}

// sendGenericWebhook posts a Standard Webhooks signed message (the same
// format as internal/webhook), docs/API.md §5. Signing uses cfg.Secret, a
// secret Linx generates when the channel is created — never one the admin
// types in, so there's nothing for a typo to get wrong.
func sendGenericWebhook(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	if cfg.URL == "" || cfg.Secret == "" {
		return nil, &configError{"This webhook channel has no signing secret. Recreate it."}
	}
	items := make([]genericWebhookItem, 0, len(msg.Items))
	for _, it := range msg.Items {
		items = append(items, genericWebhookItem{Severity: it.Severity, Title: it.Title, Message: it.Body,
			Link: it.Link, Resolved: it.Resolved})
	}
	eventType := "alert." + msg.Kind
	if msg.Kind == DeliveryDigest {
		eventType = "alert.digest"
	}
	payload := genericWebhookBody{Type: eventType, Timestamp: msg.At.UTC()}
	payload.Data.Alerts = items
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	id := msgID(msg.DeliveryID)
	sig, err := webhook.Sign([]string{cfg.Secret}, id, msg.At, body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, &configError{"That isn't a valid URL."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Linx-Alerts/"+version.Version)
	req.Header.Set("webhook-id", id)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(msg.At.Unix(), 10))
	req.Header.Set("webhook-signature", sig)
	return client.Do(req)
}
