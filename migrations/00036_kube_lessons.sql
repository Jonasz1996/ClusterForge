-- +goose Up
-- De voortgang in de lessen Kubernetes, per gebruiker en cluster. Een rij
-- ontstaat zodra een opdracht aan de beurt is; data is wat ClusterForge
-- toen zag, zoals de pods die er waren voordat je er een weghaalde. Een
-- opdracht is af als done_at gezet is; bij een vraag staat het antwoord
-- erbij.
CREATE TABLE kube_lesson_steps (
    cluster_id uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    lesson     text NOT NULL,
    step       text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    done_at    timestamptz,
    data       jsonb NOT NULL DEFAULT '{}',
    answer     text NOT NULL DEFAULT '',
    PRIMARY KEY (cluster_id, user_id, lesson, step)
);

-- +goose Down
DROP TABLE kube_lesson_steps;
