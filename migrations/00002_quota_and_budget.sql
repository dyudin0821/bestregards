-- +goose Up
CREATE TABLE quota_usage (
    telegram_id BIGINT      NOT NULL REFERENCES users (telegram_id) ON DELETE CASCADE,
    day         DATE        NOT NULL,
    used        INTEGER     NOT NULL CHECK (used >= 0),
    PRIMARY KEY (telegram_id, day)
);

-- The primary key leads with telegram_id; the purge job deletes by day.
CREATE INDEX quota_usage_day_idx ON quota_usage (day);

COMMENT ON TABLE quota_usage IS 'Free AI translations used per user per calendar day (day is the date in QUOTA_TZ)';

CREATE TABLE budget_usage (
    day         DATE   PRIMARY KEY,
    spent_micro BIGINT NOT NULL CHECK (spent_micro >= 0),
    guest_micro BIGINT NOT NULL DEFAULT 0 CHECK (guest_micro >= 0)
);

COMMENT ON TABLE budget_usage IS 'Money spent on free AI translations per calendar day, in millionths of a US dollar';
COMMENT ON COLUMN budget_usage.guest_micro IS 'The part of spent_micro spent by anonymous guests; capped lower than the whole so guests cannot starve Telegram users';

-- +goose Down
DROP TABLE budget_usage;
DROP TABLE quota_usage;
