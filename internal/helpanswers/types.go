// Package helpanswers is Help's written answers (docs/HELP.md §4, ADR-060):
// off by default; an admin picks a provider (Anthropic, an
// OpenAI-compatible service, or Ollama), and a question is answered by that
// provider's model from the guide sections Help's search finds, nothing
// else. Only the question and those sections leave the server, over the
// SSRF-guarded HTTPS client, with limits per person and per server. Caller
// mistakes come back as *apihttp.Error.
package helpanswers

import (
	"context"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderOllama    = "ollama"
)

// AnthropicURL is where Anthropic answers: fixed, so its key only ever goes
// there.
const AnthropicURL = "https://api.anthropic.com/"

// ProviderNames are how the page names each provider under an answer
// ("Answers are written by <name> from Linx's guides").
var ProviderNames = map[string]string{
	ProviderAnthropic: "Anthropic (Claude)",
	ProviderOpenAI:    "an OpenAI-compatible service",
	ProviderOllama:    "Ollama",
}

// Limits (docs/HELP.md §4). The daily ones are the defaults an admin may
// change.
const (
	PersonPerMinute         = 10
	DefaultPersonDailyLimit = 200
	DefaultServerDailyLimit = 1000
	// AnswerTimeout is how long a whole answer may take.
	AnswerTimeout = 30 * time.Second
	// MaxExcerptWords is how much of the guides goes with a question.
	MaxExcerptWords = 3000
	// maxAnswerTokens bounds an answer's length (and so its cost).
	maxAnswerTokens = 4000
)

// Config is the tenant's written-answers setting. APIKeyEnc is sealed with
// ADR-030's key, row id SealID(TenantID); nil when there's no key.
type Config struct {
	TenantID         uuid.UUID
	Enabled          bool
	Provider         string
	BaseURL          string
	Model            string
	APIKeyEnc        []byte
	PersonDailyLimit int
	ServerDailyLimit int
	Version          int
	UpdatedAt        time.Time
}

// SealID is the API key's row id for dbsecret.
func SealID(tenant uuid.UUID) string { return "help_answers:" + tenant.String() }

// Store is the database access written answers need (internal/store
// implements it).
type Store interface {
	// HelpAnswers returns the tenant's setting, auth.ErrNotFound when no
	// admin has saved one yet.
	HelpAnswers(ctx context.Context, tenant uuid.UUID) (Config, error)
	// SaveHelpAnswers writes c; version is the one it replaces (0 for none
	// yet), auth.ErrVersionChanged when someone saved in between.
	SaveHelpAnswers(ctx context.Context, c Config, version int, audit auth.AuditEntry) error
	// TakeHelpAnswer counts one question by user on day (UTC) unless that
	// would pass personMax for them or serverMax for the tenant; which is
	// "person" or "server" when it's refused, "" when counted.
	TakeHelpAnswer(ctx context.Context, tenant, user uuid.UUID, day time.Time, personMax, serverMax int) (which string, err error)
	// HelpAnswersUsedToday is the tenant's count on day.
	HelpAnswersUsedToday(ctx context.Context, tenant uuid.UUID, day time.Time) (int, error)
}
