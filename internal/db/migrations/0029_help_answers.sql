-- Help's written answers (docs/HELP.md §4, ADR-060, help step 3): off by
-- default; an admin picks the provider, its address, the model and the API
-- key. One row per tenant, made the first time an admin saves.

CREATE TABLE help_answers (
    tenant_id          uuid PRIMARY KEY REFERENCES tenant (id),
    enabled            boolean NOT NULL DEFAULT false,
    provider           text NOT NULL CHECK (provider IN ('anthropic', 'openai', 'ollama')),
    -- Where the provider is: always https (the SSRF guard, docs/API.md §4).
    -- Anthropic's is fixed, so empty here.
    base_url           text NOT NULL DEFAULT '' CHECK (base_url = '' OR (base_url ~ '^https://' AND length(base_url) <= 500)),
    model              text NOT NULL CHECK (length(model) BETWEEN 1 AND 100),
    -- Sealed with ADR-030's key, row id "help_answers:<tenant_id>"; NULL
    -- when there's none (an Ollama without one).
    api_key_enc        bytea,
    -- Questions a day: per person and for the whole server.
    person_daily_limit integer NOT NULL DEFAULT 200 CHECK (person_daily_limit BETWEEN 1 AND 10000),
    server_daily_limit integer NOT NULL DEFAULT 1000 CHECK (server_daily_limit BETWEEN 1 AND 100000),
    version            integer NOT NULL DEFAULT 1,
    updated_at         timestamptz NOT NULL
);

-- Questions asked, per person per day (UTC), for the daily limits. Only a
-- count: never the questions. Rows older than a week are removed as new
-- days start.
CREATE TABLE help_answer_usage (
    tenant_id uuid NOT NULL REFERENCES tenant (id),
    day       date NOT NULL,
    user_id   uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    count     integer NOT NULL,
    PRIMARY KEY (tenant_id, day, user_id)
);
