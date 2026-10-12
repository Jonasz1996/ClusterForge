-- +goose Up
-- De apps van elk Kubernetes-cluster: Deployments, StatefulSets en
-- DaemonSets, zoals de sync ze elke 30 seconden ziet. De evaluator geeft
-- elke app een status, zoals een dienst: een app die al twee minuten niet
-- doet wat hij moet, is verminderd of down, en maakt het cluster verminderd.
CREATE TABLE kube_workloads (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id    uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    namespace     text NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('Deployment', 'StatefulSet', 'DaemonSet')),
    name          text NOT NULL,
    -- system is true in een namespace van Kubernetes zelf; die telt niet
    -- mee voor de status.
    system        boolean NOT NULL DEFAULT false,
    desired       integer NOT NULL DEFAULT 0,
    ready         integer NOT NULL DEFAULT 0,
    up_to_date    integer NOT NULL DEFAULT 0,
    images        text[] NOT NULL DEFAULT '{}',
    -- trouble zegt wat de pods hebben, zoals ImagePullBackOff; leeg als er
    -- geen pod met een probleem is.
    trouble       text NOT NULL DEFAULT '',
    -- short_since is sinds wanneer de app niet doet wat hij moet; leeg als
    -- hij in orde is.
    short_since   timestamptz,
    status        text NOT NULL DEFAULT 'unknown',
    status_reason text NOT NULL DEFAULT '',
    status_since  timestamptz,
    seen_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, namespace, kind, name)
);

-- Waarschuwingen uit de Kubernetes-events, en containers die door gebrek
-- aan geheugen stopten (OOMKilled). Kubernetes zelf bewaart events maar een
-- uur; hier blijven ze 30 dagen, voor What Broke en de tijdlijn.
CREATE TABLE kube_events (
    id          bigserial PRIMARY KEY,
    cluster_id  uuid NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    -- key is namespace/naam van het event, of pod/container/tijdstip bij
    -- een OOMKilled.
    key         text NOT NULL,
    ts          timestamptz NOT NULL,
    last_ts     timestamptz NOT NULL,
    count       integer NOT NULL DEFAULT 1,
    reason      text NOT NULL,
    namespace   text NOT NULL DEFAULT '',
    object_kind text NOT NULL DEFAULT '',
    object_name text NOT NULL DEFAULT '',
    -- De app waar het object bij hoort, zoals Deployment web; leeg als die
    -- niet te vinden is.
    app_kind    text NOT NULL DEFAULT '',
    app_name    text NOT NULL DEFAULT '',
    -- De Kubernetes-node, en de ClusterForge-node als die gekoppeld is.
    node_name   text NOT NULL DEFAULT '',
    node_id     uuid REFERENCES nodes (id) ON DELETE SET NULL,
    message     text NOT NULL DEFAULT '',
    UNIQUE (cluster_id, key)
);
CREATE INDEX kube_events_cluster_ts ON kube_events (cluster_id, ts);
CREATE INDEX kube_events_node_ts ON kube_events (node_id, ts) WHERE node_id IS NOT NULL;
CREATE INDEX kube_events_last_ts ON kube_events (last_ts);

-- Waarom de API niet antwoordde: api als een agent op een server het
-- vroeg en geen antwoord kreeg, old_agent of no_agent als er niemand was om
-- het te vragen. Alleen api maakt het cluster down.
ALTER TABLE kube_clusters ADD COLUMN error_kind text NOT NULL DEFAULT ''
    CHECK (error_kind IN ('', 'api', 'old_agent', 'no_agent'));

-- Een app kan geraakt zijn in een incident.
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run', 'app'));

-- +goose Down
DELETE FROM incident_subjects WHERE subject_type = 'app';
ALTER TABLE incident_subjects DROP CONSTRAINT incident_subjects_subject_type_check;
ALTER TABLE incident_subjects ADD CONSTRAINT incident_subjects_subject_type_check
    CHECK (subject_type IN ('cluster', 'node', 'service', 'test_run'));
ALTER TABLE kube_clusters DROP COLUMN error_kind;
DROP TABLE kube_events;
DROP TABLE kube_workloads;
