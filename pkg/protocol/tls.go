package protocol

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
)

var errFingerprint = errors.New("certificaat van de server komt niet overeen met de gepinde fingerprint")

// Fingerprint is de hex-sha256 van een DER-certificaat.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// PinnedTLSConfig is de TLS-configuratie van de agent: alleen een server met
// precies dit certificaat wordt vertrouwd.
func PinnedTLSConfig(fingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// De standaardcontrole kan niet: het certificaat is self-signed en de
		// hostnaam verschilt per installatie. VerifyConnection doet de echte check.
		InsecureSkipVerify: true, //nolint:gosec // vervangen door certificaat-pinning hieronder
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || Fingerprint(cs.PeerCertificates[0].Raw) != fingerprint {
				return errFingerprint
			}
			return nil
		},
	}
}
