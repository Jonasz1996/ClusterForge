-- +goose Up
-- Fase 27 M4: na de nmap-scan spreekt de scanner de gevonden diensten aan
-- (panelen en API's zonder login). default_passwords: per dienst één
-- aanmeldpoging met het bekende standaardwachtwoord; standaard uit.
ALTER TABLE security_scans ADD COLUMN default_passwords boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE security_scans DROP COLUMN default_passwords;
