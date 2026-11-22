-- +goose Up
CREATE TABLE users (
    telegram_id  BIGINT PRIMARY KEY,
    source       TEXT        NOT NULL DEFAULT '' CHECK (length(source) <= 64),
    banned       BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN users.source IS 'start_param of the first launch, used for campaign attribution';

-- +goose Down
DROP TABLE users;
