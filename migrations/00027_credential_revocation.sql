-- +goose Up
ALTER TABLE run_service_credentials ADD COLUMN revoked_at timestamptz;

-- +goose Down
ALTER TABLE run_service_credentials DROP COLUMN revoked_at;
