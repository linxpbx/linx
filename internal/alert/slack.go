package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func slackEmoji(severity string, resolved bool) string {
	switch {
	case resolved:
		return ":white_check_mark:"
	case severity == SeverityCritical:
		return ":rotating_light:"
	case severity == SeverityWarning:
		return ":warning:"
	default:
		return ":information_source:"
	}
}

// sendSlack posts to a Slack incoming webhook URL (docs/API.md §5).
func sendSlack(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	if cfg.URL == "" {
		return nil, &configError{"This Slack channel has no webhook URL."}
	}
	title, body, link, severity, resolved := summarize(msg)
	text := fmt.Sprintf("%s *%s*\n%s", slackEmoji(severity, resolved), title, body)
	if link != "" {
		text += "\n<" + link + ">"
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{text})
	if err != nil {
		return nil, err
	}
	return postJSON(ctx, client, cfg.URL, nil, payload)
}
