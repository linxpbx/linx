package helpanswers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// A prompt is what's sent: the fixed instructions and one user message
// (the guide sections and the question).
type prompt struct {
	system, user string
}

// errRefused is a model that declined to answer.
var errRefused = errors.New("the model declined to answer")

// errTooLong is a model that stopped at the length limit; what it wrote is
// kept.
var errTooLong = errors.New("the answer reached its length limit")

// stream sends p to c's provider with key and calls write with each piece
// of the answer as it comes. client is the SSRF-guarded one.
func stream(ctx context.Context, client *http.Client, c Config, key string, p prompt, write func(string) error) error {
	switch c.Provider {
	case ProviderAnthropic:
		return streamAnthropic(ctx, client, c, key, p, write)
	case ProviderOpenAI:
		return streamOpenAI(ctx, client, c, key, p, write)
	case ProviderOllama:
		return streamOllama(ctx, client, c, key, p, write)
	}
	return fmt.Errorf("unknown provider %q", c.Provider)
}

// anthropicURL is AnthropicURL, changed only by tests.
var anthropicURL = AnthropicURL

// anthropicVersion is the Messages API version Linx speaks.
const anthropicVersion = "2023-06-01"

// streamAnthropic speaks Anthropic's Messages API with server-sent events,
// directly: the official library would add 6.7 MB to the control plane
// for one call (owner decision, help step 3; docs/RESOURCES.md).
func streamAnthropic(ctx context.Context, client *http.Client, c Config, key string, p prompt, write func(string) error) error {
	resp, err := post(ctx, client, anthropicURL+"v1/messages", map[string]string{
		"X-Api-Key": key, "Anthropic-Version": anthropicVersion,
	}, map[string]any{
		"model": c.Model, "max_tokens": maxAnswerTokens, "stream": true, "system": p.system,
		"messages": []chatMessage{{Role: "user", Content: p.user}},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	stop := ""
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &ev); err != nil {
			return fmt.Errorf("the provider sent something that isn't an answer: %w", err)
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				if err := write(ev.Delta.Text); err != nil {
					return err
				}
			}
		case "message_delta":
			stop = ev.Delta.StopReason
		case "error":
			return &providerError{detail: ev.Error.Message}
		case "message_stop":
			switch stop {
			case "refusal":
				return errRefused
			case "max_tokens":
				return errTooLong
			}
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the answer stopped part way")
}

// providerError is an error answer from the provider: an HTTP status, or
// status 0 for one given in a stream.
type providerError struct {
	status int
	detail string
}

func (e *providerError) Error() string {
	if e.status == 0 {
		return "the provider said: " + e.detail
	}
	msg := fmt.Sprintf("the provider answered %d %s", e.status, http.StatusText(e.status))
	if e.detail != "" {
		msg += ": " + e.detail
	}
	return msg
}

// endpoint joins the admin's base address and path.
func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// bearer is the Authorization header for key ("" for none).
func bearer(key string) map[string]string {
	if key == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + key}
}

func post(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &providerError{status: resp.StatusCode, detail: errorMessage(detail)}
	}
	return resp, nil
}

// errorMessage picks the message out of an OpenAI- or Ollama-style error
// body ({"error": {"message": …}} or {"error": "…"}).
func errorMessage(body []byte) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &nested) == nil && nested.Error.Message != "" {
		return nested.Error.Message
	}
	var flat struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &flat) == nil {
		return flat.Error
	}
	return ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func messages(p prompt) []chatMessage {
	return []chatMessage{{Role: "system", Content: p.system}, {Role: "user", Content: p.user}}
}

// maxLine bounds one streamed line from a provider.
const maxLine = 1 << 20

// streamOpenAI speaks the chat completions API with server-sent events.
func streamOpenAI(ctx context.Context, client *http.Client, c Config, key string, p prompt, write func(string) error) error {
	resp, err := post(ctx, client, endpoint(c.BaseURL, "/chat/completions"), bearer(key), map[string]any{
		"model": c.Model, "messages": messages(p), "stream": true, "max_tokens": maxAnswerTokens,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	finish := ""
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("the provider sent something that isn't an answer: %w", err)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				if err := write(ch.Delta.Content); err != nil {
					return err
				}
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	switch finish {
	case "length":
		return errTooLong
	case "content_filter":
		return errRefused
	}
	return nil
}

// streamOllama speaks Ollama's own chat API: one JSON object per line.
func streamOllama(ctx context.Context, client *http.Client, c Config, key string, p prompt, write func(string) error) error {
	resp, err := post(ctx, client, endpoint(c.BaseURL, "/api/chat"), bearer(key), map[string]any{
		"model": c.Model, "messages": messages(p), "stream": true,
		"options": map[string]any{"num_predict": maxAnswerTokens},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
			Error      string `json:"error"`
		}
		if err := json.Unmarshal(line, &chunk); err != nil {
			return fmt.Errorf("the provider sent something that isn't an answer: %w", err)
		}
		if chunk.Error != "" {
			return &providerError{detail: chunk.Error}
		}
		if chunk.Message.Content != "" {
			if err := write(chunk.Message.Content); err != nil {
				return err
			}
		}
		if chunk.Done {
			if chunk.DoneReason == "length" {
				return errTooLong
			}
			return nil
		}
	}
	return sc.Err()
}
