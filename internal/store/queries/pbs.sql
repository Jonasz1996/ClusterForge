-- name: ListPBSAgents :many
-- Fase 32: de nodes waarvan de agent op een Proxmox Backup Server draait
-- (facts pbs), met hun agent en heartbeat.
SELECT n.id, n.hostname, coalesce(host(n.primary_ip), '')::text AS address, (f.facts -> 'pbs')::jsonb AS pbs,
       a.protocol_version AS agent_protocol, a.version AS agent_version, s.heartbeat_at
FROM nodes n
JOIN node_facts f ON f.node_id = n.id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_status s ON s.node_id = n.id
WHERE jsonb_typeof(f.facts -> 'pbs') = 'object'
ORDER BY n.hostname;

-- name: ListPBSCoverageNodes :many
-- Fase 32 M5: de nodes die een Proxmox-gast zijn (VMID), met hun cluster,
-- om ze te vinden tussen de back-ups op PBS.
SELECT n.id, n.hostname, n.pve_vmid::integer AS vmid, coalesce(r.type, '')::text AS guest_type,
       n.cluster_id, coalesce(c.name, '')::text AS cluster_name, coalesce(c.environment::text, '')::text AS environment
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN proxmox_resources r ON r.connection_id = n.proxmox_id AND r.vmid = n.pve_vmid AND r.type IN ('qemu', 'lxc')
WHERE n.pve_vmid IS NOT NULL
ORDER BY c.name NULLS LAST, n.hostname;
