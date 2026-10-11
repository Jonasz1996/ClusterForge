-- name: ListHostNetworkReads :many
SELECT pve_node, read_at FROM proxmox_host_network_reads WHERE connection_id = $1;

-- name: DeleteHostNetwork :exec
DELETE FROM proxmox_host_networks WHERE connection_id = $1 AND pve_node = $2;

-- name: InsertHostNetworkIface :exec
INSERT INTO proxmox_host_networks (connection_id, pve_node, iface, type, active, ports, vlan_id, vlan_raw_device,
                                   cidr, cidr6, gateway, gateway6, vlan_aware, mtu, comments)
VALUES (@connection_id, @pve_node, @iface, @type, @active, @ports, @vlan_id, @vlan_raw_device,
        @cidr, @cidr6, @gateway, @gateway6, @vlan_aware, @mtu, @comments);

-- name: SetHostNetworkRead :exec
INSERT INTO proxmox_host_network_reads (connection_id, pve_node, read_at, error)
VALUES ($1, $2, $3, $4)
ON CONFLICT (connection_id, pve_node) DO UPDATE SET read_at = EXCLUDED.read_at, error = EXCLUDED.error;

-- name: DeleteStaleHostNetworks :exec
-- Het netwerk van hosts die Proxmox niet meer kent.
WITH gone AS (
    DELETE FROM proxmox_host_network_reads h
    WHERE h.connection_id = $1
      AND NOT EXISTS (SELECT 1 FROM proxmox_resources r
                      WHERE r.connection_id = h.connection_id AND r.type = 'node' AND r.pve_node = h.pve_node)
    RETURNING h.connection_id, h.pve_node
)
DELETE FROM proxmox_host_networks n USING gone g
WHERE n.connection_id = g.connection_id AND n.pve_node = g.pve_node;

-- name: ListHostNetworks :many
SELECT * FROM proxmox_host_networks ORDER BY connection_id, pve_node, iface;

-- name: ListHostNetworksByConnection :many
SELECT * FROM proxmox_host_networks WHERE connection_id = $1 ORDER BY pve_node, iface;

-- name: ListHostNetworkStates :many
SELECT pve_node, read_at, error FROM proxmox_host_network_reads WHERE connection_id = $1 ORDER BY pve_node;

-- name: ListLinkMatches :many
-- VM's en containers die geen node zijn, met een MAC-adres dat een node
-- met een agent en zonder VM ook heeft.
WITH node_macs AS (
    SELECT f.node_id, lower(i->>'mac') AS mac
    FROM node_facts f
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE WHEN jsonb_typeof(f.facts->'interfaces') = 'array' THEN f.facts->'interfaces' ELSE '[]'::jsonb END) i
    WHERE coalesce(i->>'mac', '') NOT IN ('', '00:00:00:00:00:00')
), guest_macs AS (
    SELECT c.connection_id, c.vmid, lower(nic->>'mac') AS mac
    FROM proxmox_guest_config c
    CROSS JOIN LATERAL jsonb_array_elements(c.nets) nic
    WHERE coalesce(nic->>'mac', '') <> ''
)
SELECT DISTINCT ON (g.connection_id, g.vmid, m.node_id) g.connection_id, g.vmid, m.node_id, g.mac::text AS mac
FROM guest_macs g
JOIN node_macs m ON m.mac = g.mac
JOIN nodes n ON n.id = m.node_id AND n.proxmox_id IS NULL
JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
JOIN proxmox_resources r ON r.connection_id = g.connection_id AND r.vmid = g.vmid AND r.type IN ('qemu', 'lxc')
WHERE NOT r.template
  AND coalesce(r.data->>'pool', '') <> @sandbox_pool::text
  AND NOT EXISTS (SELECT 1 FROM nodes o WHERE o.proxmox_id = g.connection_id AND o.pve_vmid = g.vmid)
ORDER BY g.connection_id, g.vmid, m.node_id, g.mac;

-- name: InsertLinkProposal :one
-- Een voorstel dat al bestaat, ook een genegeerd, blijft zoals het is.
INSERT INTO node_link_proposals (connection_id, vmid, node_id, mac)
VALUES ($1, $2, $3, $4)
ON CONFLICT (connection_id, vmid, node_id) DO NOTHING
RETURNING id;

-- name: DeleteInvalidLinkProposals :execrows
-- Voorstellen die niet meer kunnen: de node of de VM is intussen
-- gekoppeld, of de VM bestaat niet meer.
DELETE FROM node_link_proposals p
WHERE p.state = 'suggested'
  AND (EXISTS (SELECT 1 FROM nodes n WHERE n.id = p.node_id AND n.proxmox_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM nodes o WHERE o.proxmox_id = p.connection_id AND o.pve_vmid = p.vmid)
       OR NOT EXISTS (SELECT 1 FROM proxmox_resources r
                      WHERE r.connection_id = p.connection_id AND r.vmid = p.vmid AND r.type IN ('qemu', 'lxc')));

-- name: ListLinkProposals :many
SELECT p.id, p.connection_id, c.name AS connection, p.vmid, r.name AS guest, r.type AS guest_type, r.pve_node AS host,
       p.node_id, n.hostname, n.cluster_id, coalesce(cl.name, '')::text AS cluster, p.mac, p.created_at
FROM node_link_proposals p
JOIN proxmox_connections c ON c.id = p.connection_id
JOIN proxmox_resources r ON r.connection_id = p.connection_id AND r.vmid = p.vmid AND r.type IN ('qemu', 'lxc')
JOIN nodes n ON n.id = p.node_id
LEFT JOIN clusters cl ON cl.id = n.cluster_id
WHERE p.state = 'suggested'
ORDER BY lower(n.hostname), p.vmid;

-- name: LockLinkProposal :one
SELECT * FROM node_link_proposals WHERE id = $1 FOR UPDATE;

-- name: DecideLinkProposal :exec
UPDATE node_link_proposals SET state = $2, decided_by = $3, decided_at = now() WHERE id = $1;

-- name: ListMapNodes :many
-- De nodes met wat de kaart uit hun facts nodig heeft.
SELECT n.id, n.hostname, n.cluster_id, coalesce(c.name, '')::text AS cluster,
       coalesce(c.environment::text, '')::text AS environment, n.status, n.status_reason, n.lifecycle,
       n.primary_ip, n.proxmox_id, n.pve_vmid, (a.id IS NOT NULL)::boolean AS has_agent,
       coalesce(jsonb_build_object(
           'interfaces', f.facts->'interfaces',
           'routes', f.facts->'routes',
           'default_gateway', f.facts->'default_gateway',
           'keepalived', f.facts->'keepalived',
           'primary_address', f.facts->'primary_address'), '{}'::jsonb)::jsonb AS facts
FROM nodes n
LEFT JOIN clusters c ON c.id = n.cluster_id
LEFT JOIN agents a ON a.node_id = n.id AND a.revoked_at IS NULL
LEFT JOIN node_facts f ON f.node_id = n.id
ORDER BY lower(n.hostname), n.id;

-- name: ListMapClusters :many
SELECT id, name, environment::text AS environment, status, status_reason FROM clusters ORDER BY lower(name), id;

-- name: ListMapVIPs :many
SELECT v.id, v.cluster_id, v.address, v.interface, v.vrid, v.owner_node_id FROM vips v ORDER BY v.address;

-- name: ListAllHostNetworkReads :many
SELECT connection_id, pve_node, read_at, error FROM proxmox_host_network_reads;
