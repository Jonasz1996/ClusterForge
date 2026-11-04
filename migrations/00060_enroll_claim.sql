-- +goose Up
-- Een CT uit een CT-template heeft geen QEMU guest agent, dus ClusterForge
-- kan er geen aanmeldbestand in zetten. De agent vraagt dan zelf om
-- aanmelding (agents/claim): dat lukt alleen met een token voor een node
-- met dezelfde hostname, vanaf het adres dat de uitrol hem gaf.
ALTER TABLE enrollment_tokens ADD COLUMN claim_ip text;

-- +goose Down
ALTER TABLE enrollment_tokens DROP COLUMN claim_ip;
