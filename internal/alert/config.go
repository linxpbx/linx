package alert

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// fieldsFor names the Config fields a kind uses, for validation messages
// and to reject fields that don't apply.
var fieldsFor = map[string][]string{
	KindNtfy:     {"topic"}, // server_url, access_token optional
	KindGotify:   {"server_url", "app_token"},
	KindSlack:    {"url"},
	KindTeams:    {"url"},
	KindTelegram: {"bot_token", "chat_id"},
	KindWebhook:  {"url"},
}

// optionalFields may be set for a kind without being required.
var optionalFields = map[string][]string{
	KindNtfy: {"server_url", "access_token"},
}

// fieldValue reads one named field of cfg (used generically so ValidateFor
// can report on the fields named in fieldsFor without a big switch).
func fieldValue(cfg Config, name string) string {
	switch name {
	case "server_url":
		return cfg.ServerURL
	case "topic":
		return cfg.Topic
	case "access_token":
		return cfg.AccessToken
	case "app_token":
		return cfg.AppToken
	case "url":
		return cfg.URL
	case "bot_token":
		return cfg.BotToken
	case "chat_id":
		return cfg.ChatID
	}
	return ""
}

// allFields is every Config field name, for spotting one that doesn't
// belong to kind.
var allFields = []string{"server_url", "topic", "access_token", "app_token", "url", "bot_token", "chat_id"}

// ValidateFor checks that cfg has exactly the fields kind's channel needs
// (required ones set, no fields from another kind), returning a
// plain-language message naming the first problem, or "" if it's fine.
func ValidateFor(kind string, cfg Config) string {
	required, ok := fieldsFor[kind]
	if !ok {
		return fmt.Sprintf("%q isn't a channel kind.", kind)
	}
	allowed := append(append([]string{}, required...), optionalFields[kind]...)
	for _, f := range allFields {
		v := fieldValue(cfg, f)
		wanted := contains(allowed, f)
		if v == "" && wanted && contains(required, f) {
			return fmt.Sprintf("%q needs %q.", kind, jsonName(f))
		}
		if v != "" && !wanted {
			return fmt.Sprintf("%q isn't used by a %s channel.", jsonName(f), kind)
		}
	}
	if kind == KindNtfy && cfg.Topic != "" && !topicPattern.MatchString(cfg.Topic) {
		return "ntfy topics are letters, digits, underscores and hyphens only."
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// jsonName is f itself: fieldsFor already uses the wire (snake_case) names.
func jsonName(f string) string { return f }

// Marshal and Unmarshal round-trip Config through JSON for sealing
// (dbsecret encrypts and decrypts opaque bytes).
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

func UnmarshalConfig(b []byte) (Config, error) {
	var c Config
	err := json.Unmarshal(b, &c)
	return c, err
}
