-- name: NavUsage :one
-- Welke onderdelen in gebruik zijn, zodat het menu de rest kan verbergen
-- (fase 47). Alleen bestaan telt, geen inhoud.
SELECT
  EXISTS (SELECT 1 FROM kube_clusters) AS kubernetes,
  EXISTS (SELECT 1 FROM git_repos) AS gitops,
  EXISTS (SELECT 1 FROM service_dependencies) AS dependencies,
  EXISTS (SELECT 1 FROM proxmox_connections) AS proxmox,
  EXISTS (SELECT 1 FROM docker_hosts) AS docker,
  EXISTS (SELECT 1 FROM zabbix_agent_nodes) AS zabbix_agents;
