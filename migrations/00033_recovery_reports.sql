-- +goose Up
-- Het rapport Herstelbaarheid per maand. Elke eerste van de maand bewaart
-- de planner het rapport van de maand ervoor, zodat het later nog zegt hoe
-- de back-ups en de risico's toen stonden.
CREATE TABLE recovery_reports (
    month        date PRIMARY KEY CHECK (extract(day FROM month) = 1),
    report       jsonb NOT NULL,
    generated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE recovery_reports;
