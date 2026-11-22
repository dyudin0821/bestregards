-- name: UpsertUser :one
-- Registers a Telegram user on first launch and refreshes last_seen_at afterwards.
-- source keeps the first start_param; later launches never overwrite it.
INSERT INTO users (telegram_id, source)
VALUES ($1, $2)
ON CONFLICT (telegram_id) DO UPDATE SET last_seen_at = now()
RETURNING telegram_id, source, banned, created_at, last_seen_at;

-- name: CreateGuest :one
-- An anonymous website visitor. The id is negative so it can never collide with a Telegram id.
INSERT INTO users (telegram_id)
VALUES (-nextval('guest_id_seq'))
RETURNING telegram_id, source, banned, created_at, last_seen_at;

-- name: GetUser :one
SELECT telegram_id, source, banned, created_at, last_seen_at
FROM users
WHERE telegram_id = $1;

-- name: ReserveQuota :one
-- Takes one free translation for the day. Returns no row when the user already used max_used.
-- The check and the increment are one statement, so concurrent requests cannot exceed the limit.
INSERT INTO quota_usage (telegram_id, day, used)
VALUES ($1, $2, 1)
ON CONFLICT (telegram_id, day) DO UPDATE SET used = quota_usage.used + 1
WHERE quota_usage.used < sqlc.arg(max_used)::int
RETURNING used;

-- name: RefundQuota :exec
UPDATE quota_usage SET used = used - 1
WHERE telegram_id = $1 AND day = $2 AND used > 0;

-- name: GetQuotaUsed :one
SELECT COALESCE((SELECT used FROM quota_usage WHERE telegram_id = $1 AND day = $2), 0)::int;

-- name: AddBudgetSpend :exec
-- A guest's spend is also counted in guest_micro, which has its own cap (see ReserveBudget).
INSERT INTO budget_usage (day, spent_micro, guest_micro)
VALUES (sqlc.arg(day), sqlc.arg(micro)::bigint, CASE WHEN sqlc.arg(guest)::bool THEN sqlc.arg(micro)::bigint ELSE 0 END)
ON CONFLICT (day) DO UPDATE SET
    spent_micro = budget_usage.spent_micro + EXCLUDED.spent_micro,
    guest_micro = budget_usage.guest_micro + EXCLUDED.guest_micro;

-- name: ReserveBudget :one
-- Adds to the day's spending only if the total stays within the cap and, for a guest, the guests'
-- total stays within guest_cap; no row comes back otherwise. Both checks and both increments are
-- this one statement, so concurrent reservations cannot pass either cap.
INSERT INTO budget_usage (day, spent_micro, guest_micro)
VALUES (sqlc.arg(day), sqlc.arg(micro)::bigint, CASE WHEN sqlc.arg(guest)::bool THEN sqlc.arg(micro)::bigint ELSE 0 END)
ON CONFLICT (day) DO UPDATE SET
    spent_micro = budget_usage.spent_micro + EXCLUDED.spent_micro,
    guest_micro = budget_usage.guest_micro + EXCLUDED.guest_micro
WHERE budget_usage.spent_micro + EXCLUDED.spent_micro <= sqlc.arg(cap)::bigint
  AND (NOT sqlc.arg(guest)::bool OR budget_usage.guest_micro + EXCLUDED.guest_micro <= sqlc.arg(guest_cap)::bigint)
RETURNING spent_micro;

-- name: ReleaseBudget :exec
-- Gives back part of a reservation. It is an UPDATE, not an upsert: a negative value in an INSERT
-- would break the CHECK before ON CONFLICT is considered.
UPDATE budget_usage SET
    spent_micro = GREATEST(spent_micro - sqlc.arg(micro)::bigint, 0),
    guest_micro = CASE WHEN sqlc.arg(guest)::bool THEN GREATEST(guest_micro - sqlc.arg(micro)::bigint, 0) ELSE guest_micro END
WHERE day = sqlc.arg(day);

-- name: GetBudgetSpent :one
SELECT COALESCE((SELECT spent_micro FROM budget_usage WHERE day = $1), 0)::bigint;

-- name: GetGuestBudgetSpent :one
SELECT COALESCE((SELECT guest_micro FROM budget_usage WHERE day = $1), 0)::bigint;

-- name: InsertEvent :batchexec
-- A second "copied" or "translate_done" for the same result is silently ignored (see
-- events_copied_once_idx and events_done_once_idx).
INSERT INTO events (at, type, kind, mode, level, source, result_id, user_hash, campaign)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING;

-- name: PurgeEventsChunk :execrows
-- One bounded slice of old events, so a big table is purged in short transactions.
DELETE FROM events WHERE id IN (
    SELECT old.id FROM events AS old WHERE old.at < sqlc.arg(before)::timestamptz ORDER BY old.at LIMIT sqlc.arg(chunk)::int
);

-- name: PurgeGuests :execrows
-- Their quota rows go with them (ON DELETE CASCADE).
DELETE FROM users WHERE telegram_id < 0 AND created_at < $1;

-- name: PurgeQuota :execrows
-- Per-day counters of everyone; only the current day is ever read.
DELETE FROM quota_usage WHERE day < $1;

-- Admin panel reads. Everything below is read-only.

-- name: ActivityDone :one
-- Translations shown in the window, how many of them were copied, and how many people made them.
SELECT count(*) FILTER (WHERE t.kind = 'pair')::bigint AS pair_n,
       count(*) FILTER (WHERE t.kind = 'ai')::bigint   AS ai_n,
       count(c.result_id)::bigint                      AS copied_n,
       count(DISTINCT t.user_hash) FILTER (WHERE t.user_hash <> '')::bigint AS users_n
FROM events t
LEFT JOIN events c ON c.result_id = t.result_id AND c.type = 'copied' AND c.result_id <> ''
WHERE t.type = 'translate_done' AND t.at >= $1 AND t.at < $2;

-- name: CountEventsByType :many
SELECT type, count(*)::bigint AS n
FROM events
WHERE at >= $1 AND at < $2 AND type IN ('quota_exhausted', 'budget_exhausted')
GROUP BY type;

-- name: GetSetting :one
SELECT value FROM settings WHERE key = $1;

-- name: PutSetting :exec
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
