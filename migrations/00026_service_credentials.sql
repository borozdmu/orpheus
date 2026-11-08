-- +goose Up
CREATE TABLE IF NOT EXISTS run_service_credentials (
    run_id uuid PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    invocation_id uuid NOT NULL UNIQUE,
    access_context jsonb NOT NULL,
    scopes jsonb NOT NULL,
    ciphertext text NOT NULL
);
CREATE TABLE IF NOT EXISTS run_input_subjects (
    message_id uuid PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    subject jsonb NOT NULL
);

DROP TABLE IF EXISTS pending_v24_provisioning_backfill;

-- +goose Down
DROP TABLE run_input_subjects;
DROP TABLE run_service_credentials;
-- If the data migration is subsequently reversed, it must be replayable.
CREATE TABLE IF NOT EXISTS pending_v24_provisioning_backfill ();
