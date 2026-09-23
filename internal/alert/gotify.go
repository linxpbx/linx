package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// gotifyPriority maps a severity to Gotify's 0-10 scale.
func gotifyPriority(severity string, resolved bool) int {
	switch {
	case resolved:
		return 2
	case severity == SeverityCritical:
		return 8
	case severity == SeverityWarning:
		return 5
	default:
		return 2
	}
}

// sendGotify posts to a self-hosted Gotify server's message endpoint
// (docs/API.md §5).
func sendGotify(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	title, body, link, severity, resolved := summarize(msg)
	if link != "" {
		body += "\n" + link
	}
	server := strings.TrimRight(cfg.ServerURL, "/")
	if server == "" {
		return nil, &configError{"This Gotify channel has no server address."}
	}
	payload, err := json.Marshal(struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Priority int    `json:"priority"`
	}{title, body, gotifyPriority(severity, resolved)})
	if err != nil {
		return nil, err
	}
	target := server + "/message?token=" + url.QueryEscape(cfg.AppToken)
	return postJSON(ctx, client, target, nil, payload)
}
