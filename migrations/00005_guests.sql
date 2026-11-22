-- +goose Up
-- Anonymous visitors of the website get a negative id from this sequence, so they share every table
-- with Telegram users (whose ids are positive) without a clash. The id is -nextval('guest_id_seq').
CREATE SEQUENCE guest_id_seq AS BIGINT;
-- The purge job removes old guests by age; the partial index keeps that scan off the real users.
CREATE INDEX users_guest_created_idx ON users (created_at) WHERE telegram_id < 0;
COMMENT ON COLUMN users.telegram_id IS 'Telegram user id; a negative value is an anonymous guest of the website';

-- +goose Down
COMMENT ON COLUMN users.telegram_id IS NULL;
DROP INDEX users_guest_created_idx;
DROP SEQUENCE guest_id_seq;
