package agentbus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	secretCert = "nats_tls_cert"
	secretKey  = "nats_tls_key"
)

// loadOrCreateCert haalt het TLS-certificaat van NATS uit de database, en maakt
// het bij de eerste start aan. Agents pinnen de sha256 van dit certificaat, dus
// het blijft hetzelfde zolang de database blijft bestaan.
func loadOrCreateCert(ctx context.Context, q *store.Queries) (tls.Certificate, string, error) {
	_, err := q.GetServerSecret(ctx, secretCert)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return tls.Certificate{}, "", err
	}
	if err != nil {
		certPEM, keyPEM, err := newSelfSigned()
		if err != nil {
			return tls.Certificate{}, "", err
		}
		// Bij twee servers die tegelijk starten wint de eerste; daarom
		// hieronder opnieuw lezen.
		if err := q.InsertServerSecret(ctx, store.InsertServerSecretParams{Name: secretKey, Value: keyPEM}); err != nil {
			return tls.Certificate{}, "", err
		}
		if err := q.InsertServerSecret(ctx, store.InsertServerSecretParams{Name: secretCert, Value: certPEM}); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	certPEM, err := q.GetServerSecret(ctx, secretCert)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyPEM, err := q.GetServerSecret(ctx, secretKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, protocol.Fingerprint(cert.Certificate[0]), nil
}

func newSelfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "clusterforge-nats"},
		NotBefore:    time.Now().Add(-time.Hour),
		// Lang geldig: agents controleren de fingerprint, niet de vervaldatum.
		NotAfter:    time.Now().AddDate(20, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"clusterforge-nats"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
