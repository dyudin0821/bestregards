-- +goose Up
CREATE TABLE events (
    id        BIGSERIAL   PRIMARY KEY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    type      TEXT        NOT NULL CHECK (type IN ('translate_done', 'copied', 'quota_exhausted', 'budget_exhausted')),
    kind      TEXT        NOT NULL DEFAULT '' CHECK (kind IN ('', 'pair', 'ai')),
    mode      TEXT        NOT NULL DEFAULT '' CHECK (length(mode) <= 32),
    level     SMALLINT    NOT NULL DEFAULT 0 CHECK (level BETWEEN 0 AND 3),
    source    TEXT        NOT NULL CHECK (source IN ('miniapp', 'web', 'inline')),
    result_id TEXT        NOT NULL DEFAULT '' CHECK (length(result_id) <= 32),
    user_hash TEXT        NOT NULL DEFAULT '' CHECK (length(user_hash) <= 64),
    campaign  TEXT        NOT NULL DEFAULT '' CHECK (length(campaign) <= 64)
);

COMMENT ON TABLE events IS 'Anonymous product events. Never holds text; user_hash is HMAC(EVENTS_PEPPER, telegram_id).';

-- Purging by age and the daily metrics both scan by time.
CREATE INDEX events_at_idx ON events (at);

-- One "copied" per result: the metric counts results that were used, not button presses.
CREATE UNIQUE INDEX events_copied_once_idx ON events (result_id) WHERE type = 'copied' AND result_id <> '';

-- One client-reported "translate_done" per result: the endpoint takes events without a session, so a
-- repeated report must not add rows.
CREATE UNIQUE INDEX events_done_once_idx ON events (result_id) WHERE type = 'translate_done' AND result_id <> '';

-- +goose Down
DROP TABLE events;
