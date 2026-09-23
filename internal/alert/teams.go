package alert

import (
	"context"
	"encoding/json"
	"net/http"
)

func teamsColor(severity string, resolved bool) string {
	switch {
	case resolved:
		return "good"
	case severity == SeverityCritical:
		return "attention"
	case severity == SeverityWarning:
		return "warning"
	default:
		return "default"
	}
}

// sendTeams posts an Adaptive Card to a Microsoft Teams Workflows webhook
// URL (docs/API.md §5).
func sendTeams(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	if cfg.URL == "" {
		return nil, &configError{"This Teams channel has no webhook URL."}
	}
	title, body, link, severity, resolved := summarize(msg)

	cardBody := []map[string]any{
		{"type": "TextBlock", "text": title, "weight": "bolder", "size": "medium", "wrap": true,
			"color": teamsColor(severity, resolved)},
		{"type": "TextBlock", "text": body, "wrap": true},
	}
	var actions []map[string]any
	if link != "" {
		actions = append(actions, map[string]any{"type": "Action.OpenUrl", "title": "Open", "url": link})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    cardBody,
	}
	if len(actions) > 0 {
		card["actions"] = actions
	}
	payload, err := json.Marshal(struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     any    `json:"content"`
		} `json:"attachments"`
	}{
		Type: "message",
		Attachments: []struct {
			ContentType string `json:"contentType"`
			Content     any    `json:"content"`
		}{{ContentType: "application/vnd.microsoft.card.adaptive", Content: card}},
	})
	if err != nil {
		return nil, err
	}
	return postJSON(ctx, client, cfg.URL, nil, payload)
}
