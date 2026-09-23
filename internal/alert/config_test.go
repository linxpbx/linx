package alert

import "testing"

func TestValidateFor(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		cfg     Config
		wantErr bool
	}{
		{"ntfy minimal", KindNtfy, Config{Topic: "linx-alerts"}, false},
		{"ntfy with server and token", KindNtfy, Config{ServerURL: "https://ntfy.example", Topic: "x", AccessToken: "tok"}, false},
		{"ntfy missing topic", KindNtfy, Config{}, true},
		{"ntfy bad topic", KindNtfy, Config{Topic: "not a valid topic!"}, true},
		{"ntfy with slack url", KindNtfy, Config{Topic: "x", URL: "https://hooks.slack.com/x"}, true},

		{"gotify needs both", KindGotify, Config{ServerURL: "https://gotify.example", AppToken: "tok"}, false},
		{"gotify missing token", KindGotify, Config{ServerURL: "https://gotify.example"}, true},
		{"gotify missing server", KindGotify, Config{AppToken: "tok"}, true},

		{"slack ok", KindSlack, Config{URL: "https://hooks.slack.com/services/x"}, false},
		{"slack missing url", KindSlack, Config{}, true},
		{"slack with bot token", KindSlack, Config{URL: "https://hooks.slack.com/x", BotToken: "t"}, true},

		{"teams ok", KindTeams, Config{URL: "https://example.webhook.office.com/x"}, false},
		{"teams missing url", KindTeams, Config{}, true},

		{"telegram ok", KindTelegram, Config{BotToken: "123:abc", ChatID: "-100"}, false},
		{"telegram missing chat id", KindTelegram, Config{BotToken: "123:abc"}, true},
		{"telegram missing bot token", KindTelegram, Config{ChatID: "-100"}, true},

		{"webhook ok (before secret is generated)", KindWebhook, Config{URL: "https://example.com/hook"}, false},
		{"webhook missing url", KindWebhook, Config{}, true},

		{"unknown kind", "carrier-pigeon", Config{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ValidateFor(tt.kind, tt.cfg)
			if (msg != "") != tt.wantErr {
				t.Errorf("ValidateFor(%q, %+v) = %q, wantErr %v", tt.kind, tt.cfg, msg, tt.wantErr)
			}
		})
	}
}

func TestConfigMarshalRoundTrip(t *testing.T) {
	cfg := Config{ServerURL: "https://ntfy.example", Topic: "x", Secret: "whsec_abc"}
	b, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Errorf("round trip = %+v, want %+v", got, cfg)
	}
}
