package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func telegramEmoji(severity string, resolved bool) string {
	switch {
	case resolved:
		return "✅"
	case severity == SeverityCritical:
		return "🚨"
	case severity == SeverityWarning:
		return "⚠️"
	default:
		return "ℹ️"
	}
}

// sendTelegram posts to a Telegram bot's sendMessage API (docs/API.md
// §5). The bot token is part of the URL path, so it's never logged: only
// the fixed api.telegram.org host reaches the delivery log or an error.
func sendTelegram(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error) {
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return nil, &configError{"This Telegram channel needs a bot token and a chat id."}
	}
	title, body, link, severity, resolved := summarize(msg)
	text := fmt.Sprintf("%s %s\n%s", telegramEmoji(severity, resolved), title, body)
	if link != "" {
		text += "\n" + link
	}
	payload, err := json.Marshal(struct {
		ChatID                string `json:"chat_id"`
		Text                  string `json:"text"`
		DisableWebPagePreview bool   `json:"disable_web_page_preview"`
	}{cfg.ChatID, text, true})
	if err != nil {
		return nil, err
	}
	target := "https://api.telegram.org/bot" + url.PathEscape(cfg.BotToken) + "/sendMessage"
	return postJSON(ctx, client, target, nil, payload)
}
