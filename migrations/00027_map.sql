-- +goose Up
-- Het netwerk van elke Proxmox-host: bridges, bonds, VLAN-interfaces en
-- fysieke poorten, zoals GET /nodes/{node}/network ze geeft. De sync leest
-- het elke 10 minuten en vervangt dan alle rijen van die host.
CREATE TABLE proxmox_host_networks (
    connection_id   uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    pve_node        text NOT NULL,
    iface           text NOT NULL,
    -- bridge, bond, eth, vlan, alias, OVSBridge, OVSBond, OVSPort, OVSIntPort
    -- of unknown, zoals Proxmox het noemt.
    type            text NOT NULL,
    active          boolean NOT NULL DEFAULT false,
    -- De poorten van een bridge of de slaves van een bond.
    ports           text[] NOT NULL DEFAULT '{}',
    vlan_id         integer,
    vlan_raw_device text NOT NULL DEFAULT '',
    cidr            text NOT NULL DEFAULT '',
    cidr6           text NOT NULL DEFAULT '',
    gateway         text NOT NULL DEFAULT '',
    gateway6        text NOT NULL DEFAULT '',
    vlan_aware      boolean NOT NULL DEFAULT false,
    mtu             integer,
    comments        text NOT NULL DEFAULT '',
    PRIMARY KEY (connection_id, pve_node, iface)
);

-- Wanneer het netwerk van een host het laatst gelezen is, en de fout als
-- dat mislukte; de vorige rijen blijven dan staan.
CREATE TABLE proxmox_host_network_reads (
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    pve_node       text NOT NULL,
    read_at        timestamptz NOT NULL,
    error          text NOT NULL DEFAULT '',
    PRIMARY KEY (connection_id, pve_node)
);

-- Een VM in Proxmox die geen node is, met hetzelfde MAC-adres als een
-- interface van een node met een agent die nog aan geen VM hangt. Een
-- beheerder bevestigt of negeert het voorstel; een genegeerd voorstel komt
-- niet terug.
CREATE TABLE node_link_proposals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id  uuid NOT NULL REFERENCES proxmox_connections (id) ON DELETE CASCADE,
    vmid           integer NOT NULL,
    node_id        uuid NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    mac            text NOT NULL,
    state          text NOT NULL DEFAULT 'suggested' CHECK (state IN ('suggested', 'confirmed', 'ignored')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    decided_at     timestamptz,
    decided_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    UNIQUE (connection_id, vmid, node_id)
);

-- +goose Down
DROP TABLE node_link_proposals;
DROP TABLE proxmox_host_network_reads;
DROP TABLE proxmox_host_networks;
