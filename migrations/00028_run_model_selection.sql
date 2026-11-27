-- +goose Up
CREATE TABLE run_model_selections (
    run_id uuid PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    selection jsonb NOT NULL
);

-- +goose Down
DROP TABLE run_model_selections;
