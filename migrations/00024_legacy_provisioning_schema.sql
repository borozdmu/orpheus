-- +goose Up
ALTER TABLE operations
    ADD COLUMN IF NOT EXISTS deadline_at timestamptz,
    ADD COLUMN IF NOT EXISTS next_check_at timestamptz,
    ADD COLUMN IF NOT EXISTS check_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS first_error jsonb,
    ADD COLUMN IF NOT EXISTS abandoned_at timestamptz;

-- An installation already at upstream v24 has completed the data backfill.
-- Only installations crossing this migration need to run it in v25.
CREATE TABLE pending_v24_provisioning_backfill ();

-- +goose Down
DROP TABLE IF EXISTS pending_v24_provisioning_backfill;
