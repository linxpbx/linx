package alert

import (
	"context"
	"net/http"
	"strings"

	"linxpbx.com/linx/internal/version"
)

// defaultNtfyServer is used when a channel doesn't name its own.
const defaultNtfyServer = "https://ntfy.sh"

// ntfyPriority maps a severity to ntfy's 1-5 priority scale.
func ntfyPriority(severity string, resolved bool) string {
	switch {
	case resolved:
		return "3"
	case severity == SeverityCritical:
		return "5"
	case severity == SeverityWarning:
		return "4"
	default:
		return "3"
	}
}

func ntfyTag(severity string, resolved bool) string {
	switch {
	case resolved:
		return "white_check_mark"
	case severity == SeverityCritical:
		return "rotating_light"
	case severity == SeverityWarning:
		return "warning"
	default:
		return "information_source"
	}
}

// sendNtfy posts a plain-text push to a ntfy topic (https://ntfy.sh or a
// self-hosted server), docs/API.md §5.
func sendNtfy(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	title, body, link, severity, resolved := summarize(msg)
	server := strings.TrimRight(cfg.ServerURL, "/")
	if server == "" {
		server = defaultNtfyServer
	}
	url := server + "/" + cfg.Topic

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, &configError{"That isn't a valid ntfy server or topic."}
	}
	req.Header.Set("User-Agent", "Linx-Alerts/"+version.Version)
	req.Header.Set("Title", title)
	req.Header.Set("Priority", ntfyPriority(severity, resolved))
	req.Header.Set("Tags", ntfyTag(severity, resolved))
	if link != "" {
		req.Header.Set("Click", link)
	}
	if cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}
	return client.Do(req)
}
