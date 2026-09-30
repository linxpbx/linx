package helpanswers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/help"
	"linxpbx.com/linx/internal/safehttp"
)

// DefaultModel is the model suggested for Anthropic (docs/HELP.md §4):
// fast and cheap, and plenty for answering from a few sections.
const DefaultModel = "claude-haiku-4-5"

// DefaultTestQuestion is what Test asks when the admin gives nothing.
const DefaultTestQuestion = "How do I add a desk phone?"

// Service is what the API's /help-answers endpoints and Help's
// /help/answer do.
type Service struct {
	Store  Store
	Sealer *dbsecret.Sealer
	// Client is the SSRF-guarded HTTPS client, with room for AnswerTimeout.
	Client *http.Client
	// Policy and Resolver refuse a provider address on a private network
	// that isn't on the outbound allowlist, with a plain reason, on save.
	Policy   safehttp.Policy
	Resolver safehttp.Resolver
	// Help is the guides (nil when they couldn't be read: no answers).
	Help *help.Library
	Now  func() time.Time

	once      sync.Once
	perMinute *auth.Limiters
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

var errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "Someone changed this setting since you loaded it. Reload and try again."}

// Defaults is the setting before an admin has saved one: off, Anthropic,
// the suggested model.
func Defaults(tenant uuid.UUID) Config {
	return Config{TenantID: tenant, Provider: ProviderAnthropic, Model: DefaultModel,
		PersonDailyLimit: DefaultPersonDailyLimit, ServerDailyLimit: DefaultServerDailyLimit}
}

func (s *Service) load(ctx context.Context, tenant uuid.UUID) (Config, error) {
	c, err := s.Store.HelpAnswers(ctx, tenant)
	if errors.Is(err, auth.ErrNotFound) {
		return Defaults(tenant), nil
	}
	return c, err
}

// Get returns the setting and how many questions were asked today.
func (s *Service) Get(ctx context.Context) (Config, int, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Config{}, 0, errNoPrincipal
	}
	c, err := s.load(ctx, p.TenantID)
	if err != nil {
		return Config{}, 0, err
	}
	used, err := s.Store.HelpAnswersUsedToday(ctx, p.TenantID, s.Now().UTC())
	return c, used, err
}

// By is who writes the answers, as the page says under each one.
func By(c Config) string {
	if c.Provider == ProviderOpenAI {
		if u, err := url.Parse(c.BaseURL); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ProviderNames[c.Provider]
}

// Available is who writes answers for a caller with role ("" when not
// fully signed in), or "" when there are none for them.
func (s *Service) Available(ctx context.Context, tenant uuid.UUID, role string) (string, error) {
	if role == "" || s.Help == nil {
		return "", nil
	}
	c, err := s.load(ctx, tenant)
	if err != nil || !c.Enabled {
		return "", err
	}
	return By(c), nil
}

// Patch is a JSON Merge Patch of the setting; nil fields stay as they are.
// An empty APIKey removes the key.
type Patch struct {
	Enabled                            *bool
	Provider, BaseURL, Model, APIKey   *string
	PersonDailyLimit, ServerDailyLimit *int
}

// Update applies patch if ifMatch (an ETag, or "") matches. Every change needs a fresh "confirm it's you", and is
// audited without the key.
func (s *Service) Update(ctx context.Context, patch Patch, ifMatch string) (Config, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Config{}, errNoPrincipal
	}
	if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
		return Config{}, err
	}
	cur, err := s.load(ctx, p.TenantID)
	if err != nil {
		return Config{}, err
	}
	if ifMatch != "" && ifMatch != auth.ETag(cur.Version) {
		return Config{}, errChanged
	}
	next := cur
	changes := map[string]any{}

	if patch.Provider != nil {
		switch *patch.Provider {
		case ProviderAnthropic, ProviderOpenAI, ProviderOllama:
		default:
			return Config{}, invalid("provider_invalid", "provider is anthropic, openai or ollama.")
		}
		next.Provider = *patch.Provider
		changes["provider"] = next.Provider
	}
	if patch.BaseURL != nil {
		next.BaseURL = strings.TrimSpace(*patch.BaseURL)
	}
	if next.Provider == ProviderAnthropic {
		next.BaseURL = ""
	} else if patch.BaseURL != nil || patch.Provider != nil {
		if err := s.checkURL(ctx, next.BaseURL); err != nil {
			return Config{}, err
		}
	}
	if next.BaseURL != cur.BaseURL {
		changes["base_url"] = next.BaseURL
	}
	if patch.Model != nil {
		next.Model = strings.TrimSpace(*patch.Model)
		if n := utf8.RuneCountInString(next.Model); n < 1 || n > 100 || strings.ContainsAny(next.Model, " \t\r\n") {
			return Config{}, invalid("model_invalid", "Give the model's name as the provider writes it, e.g. "+DefaultModel+".")
		}
		changes["model"] = next.Model
	}
	moved := next.Provider != cur.Provider || next.BaseURL != cur.BaseURL
	switch {
	case patch.APIKey != nil && *patch.APIKey == "":
		next.APIKeyEnc = nil
		changes["api_key"] = "removed"
	case patch.APIKey != nil:
		key := strings.TrimSpace(*patch.APIKey)
		if len(key) > 1000 || strings.ContainsAny(key, " \t\r\n") {
			return Config{}, invalid("api_key_invalid", "That doesn't look like an API key: paste it again, without spaces.")
		}
		enc, err := s.Sealer.Seal(SealID(p.TenantID), []byte(key))
		if err != nil {
			return Config{}, err
		}
		next.APIKeyEnc = enc
		changes["api_key"] = "changed"
	case moved && cur.APIKeyEnc != nil:
		// A key is only ever sent where it was given for (docs/HELP.md
		// §6): a new provider or address needs it pasted again.
		return Config{}, invalid("api_key_required",
			"Paste the API key again for the new provider or address (or leave it empty if it needs none).")
	}
	if patch.PersonDailyLimit != nil {
		if *patch.PersonDailyLimit < 1 || *patch.PersonDailyLimit > 10000 {
			return Config{}, invalid("limit_invalid", "Questions a day per person is 1 to 10,000.")
		}
		next.PersonDailyLimit = *patch.PersonDailyLimit
		changes["person_daily_limit"] = next.PersonDailyLimit
	}
	if patch.ServerDailyLimit != nil {
		if *patch.ServerDailyLimit < 1 || *patch.ServerDailyLimit > 100000 {
			return Config{}, invalid("limit_invalid", "Questions a day for the whole server is 1 to 100,000.")
		}
		next.ServerDailyLimit = *patch.ServerDailyLimit
		changes["server_daily_limit"] = next.ServerDailyLimit
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
		changes["enabled"] = next.Enabled
	}
	if next.Enabled && next.Provider == ProviderAnthropic && next.APIKeyEnc == nil {
		return Config{}, invalid("api_key_required", "Anthropic needs an API key: make one at console.anthropic.com → API keys.")
	}

	next.Version = cur.Version + 1
	next.UpdatedAt = s.Now().UTC()
	err = s.Store.SaveHelpAnswers(ctx, next, cur.Version, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: "help_answers.update", Target: "help_answers", Result: auth.ResultOK, Detail: changes,
	})
	if errors.Is(err, auth.ErrVersionChanged) {
		return Config{}, errChanged
	}
	return next, err
}

// checkURL checks an Ollama or OpenAI-compatible address: https, and not
// on a private network unless allowlisted.
func (s *Service) checkURL(ctx context.Context, raw string) error {
	if raw == "" {
		return invalid("base_url_invalid", "Give the provider's address, starting with https://.")
	}
	if len(raw) > 500 {
		return invalid("base_url_invalid", "That address is too long.")
	}
	u, err := safehttp.CheckURL(raw)
	if err != nil {
		return invalid("base_url_invalid", err.Error())
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return invalid("base_url_invalid", "The address has no ? or # part.")
	}
	if err := s.Policy.CheckHost(ctx, s.Resolver, u.Hostname()); err != nil {
		var b *safehttp.BlockedError
		if errors.As(err, &b) {
			return invalid("base_url_blocked", b.Error())
		}
		return err
	}
	return nil
}

// Question is one question from a person.
type Question struct {
	Text string
	// Role is what the asker may read ("" is refused: answers need a
	// session). An admin kept to a person's view reads a person's.
	Role   string
	UserID uuid.UUID
}

// Answer is what follows a written answer: the guides it used, and who
// wrote it.
type Answer struct {
	Guides []GuideRef
	By     string
	// Cut is true when the answer reached its length limit.
	Cut bool
}

// GuideRef is a guide an answer used.
type GuideRef struct {
	Name, Title string
}

var (
	errOff = &apihttp.Error{Status: http.StatusNotFound, Code: "answers_off",
		Detail: "Written answers aren't turned on on this server."}
	errNoHelp = &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "help_unavailable",
		Detail: "Help isn't available on this server right now."}
	errSignIn = &apihttp.Error{Status: http.StatusUnauthorized, Code: "sign_in_required",
		Detail: "Sign in to get written answers."}
)

// Ask answers q from the guides q.Role may read, calling write with the
// answer as it's written. An error before anything was written is an
// *apihttp.Error or a plain one the page shows as it is.
func (s *Service) Ask(ctx context.Context, tenant uuid.UUID, q Question, write func(string) error) (Answer, error) {
	c, err := s.load(ctx, tenant)
	if err != nil {
		return Answer{}, err
	}
	if !c.Enabled {
		return Answer{}, errOff
	}
	return s.ask(ctx, c, q, write)
}

// Test asks one question with the saved setting, on or off, as the admin
// asking, and returns the whole answer.
func (s *Service) Test(ctx context.Context, question string) (string, Answer, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return "", Answer{}, errNoPrincipal
	}
	uid, err := uuid.Parse(p.ID)
	if err != nil || p.Type != auth.TypeUser {
		return "", Answer{}, &apihttp.Error{Status: http.StatusForbidden, Code: "people_only",
			Detail: "Test it signed in as a person, in System → Settings."}
	}
	c, err := s.load(ctx, p.TenantID)
	if err != nil {
		return "", Answer{}, err
	}
	if strings.TrimSpace(question) == "" {
		question = DefaultTestQuestion
	}
	var b strings.Builder
	a, err := s.ask(ctx, c, Question{Text: question, Role: p.Role, UserID: uid}, func(t string) error {
		b.WriteString(t)
		return nil
	})
	return strings.TrimSpace(b.String()), a, err
}

func (s *Service) ask(ctx context.Context, c Config, q Question, write func(string) error) (Answer, error) {
	if s.Help == nil {
		return Answer{}, errNoHelp
	}
	if q.Role == "" {
		return Answer{}, errSignIn
	}
	text := strings.TrimSpace(q.Text)
	switch {
	case text == "":
		return Answer{}, invalid("question_empty", "Type a question first.")
	case len(text) > help.MaxQuestionLen:
		return Answer{}, invalid("question_too_long", "Ask in 500 characters or fewer.")
	}
	if c.Provider == ProviderAnthropic && c.APIKeyEnc == nil {
		return Answer{}, &Failure{"Anthropic needs an API key: add it in System → Settings → Help answers."}
	}
	now := s.Now()
	s.once.Do(func() { s.perMinute = auth.NewLimiters(PersonPerMinute, PersonPerMinute) })
	if !s.perMinute.Allow(q.UserID.String(), now) {
		return Answer{}, &apihttp.Error{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Detail: fmt.Sprintf("That's %d questions in a minute. Wait a moment and ask again.", PersonPerMinute)}
	}
	which, err := s.Store.TakeHelpAnswer(ctx, c.TenantID, q.UserID, now.UTC(), c.PersonDailyLimit, c.ServerDailyLimit)
	if err != nil {
		return Answer{}, err
	}
	switch which {
	case "person":
		return Answer{}, &apihttp.Error{Status: http.StatusTooManyRequests, Code: "daily_limit",
			Detail: fmt.Sprintf("You've asked %d questions today, the most for one day. The search results still work.", c.PersonDailyLimit)}
	case "server":
		return Answer{}, &apihttp.Error{Status: http.StatusTooManyRequests, Code: "server_daily_limit",
			Detail: "This server has used today's written answers. The search results still work, and answers are back tomorrow."}
	}

	var key string
	if c.APIKeyEnc != nil {
		b, err := s.Sealer.Open(SealID(c.TenantID), c.APIKeyEnc)
		if err != nil {
			return Answer{}, fmt.Errorf("opening the help answers API key: %w", err)
		}
		key = string(b)
	}
	excerpts := s.Help.Excerpts(q.Role, text, MaxExcerptWords)
	var allowed []string
	for _, e := range excerpts {
		allowed = append(allowed, e.Guide)
	}
	all := s.Help.Guides(q.Role)
	if len(excerpts) == 0 {
		for _, g := range all {
			allowed = append(allowed, g.Name)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, AnswerTimeout)
	defer cancel()
	f := &guidesFilter{write: write}
	err = stream(ctx, s.Client, c, key, buildPrompt(text, excerpts, all), f.Write)
	a := Answer{By: By(c)}
	if errors.Is(err, errTooLong) {
		a.Cut, err = true, nil
	}
	if err != nil {
		return Answer{}, describe(ctx, err)
	}
	names, err := f.Close(allowed)
	if err != nil {
		return Answer{}, err
	}
	for _, n := range names {
		if g, ok := s.Help.Guide(q.Role, n); ok {
			a.Guides = append(a.Guides, GuideRef{Name: g.Name, Title: g.Title})
		}
	}
	return a, nil
}

// Failure is a provider failure in plain words, for the page.
type Failure struct{ Detail string }

func (e *Failure) Error() string { return e.Detail }

func describe(ctx context.Context, err error) error {
	var pe *providerError
	switch {
	case errors.Is(err, errRefused):
		return &Failure{"The model declined to answer that question."}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &Failure{fmt.Sprintf("No answer within %d seconds.", int(AnswerTimeout.Seconds()))}
	case errors.As(err, &pe) && (pe.status == http.StatusUnauthorized || pe.status == http.StatusForbidden):
		return &Failure{"The provider didn't accept the API key. An admin can check it in System → Settings."}
	case errors.As(err, &pe) && pe.status == http.StatusTooManyRequests:
		return &Failure{"The provider is busy, or its plan has run out. Try again later."}
	case errors.As(err, &pe) && pe.status == http.StatusNotFound:
		return &Failure{"The provider doesn't know that model or address: " + err.Error() + "."}
	case errors.As(err, &pe):
		return &Failure{"The provider couldn't answer: " + err.Error() + "."}
	}
	return &Failure{"Linx couldn't reach the provider: " + safehttp.Describe(err)}
}
