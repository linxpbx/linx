package helpanswers

// SetAnthropicURL points Anthropic at a test server until the test ends.
func SetAnthropicURL(u string) func() {
	old := anthropicURL
	anthropicURL = u
	return func() { anthropicURL = old }
}
