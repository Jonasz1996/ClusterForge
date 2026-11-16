-- name: CountOpenIncidents :one
SELECT count(*) FROM incidents WHERE status = 'open';

-- name: CountFailedJobsSince :one
SELECT count(*) FROM jobs WHERE status = 'failed' AND finished_at >= @since::timestamptz;
