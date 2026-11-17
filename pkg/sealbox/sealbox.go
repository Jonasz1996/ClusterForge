// Package sealbox versleutelt een klein geheim naar een publieke X25519-
// sleutel, zodat alleen de houder van de geheime sleutel het kan lezen
// (fase 39, inlog-poort). De browser versleutelt met WebCrypto in exact
// hetzelfde formaat (web/src/lib/sealbox.ts):
//
//	sealed = base64( epk[32] || nonce[12] || AES-256-GCM(key, nonce, plain, aad) )
//	key    = HKDF-SHA256(ikm = X25519(eph, pk), salt = epk || pk, info = Info, 32)
//
// aad bindt het geheim aan waar het voor dient (bij de inlog-poort het
// domein), zodat een versleutelde inlog niet naar een andere host kan.
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Info is de HKDF-info van versie 1.
const Info = "clusterforge-inlogpoort-v1"

// GenerateKey maakt een nieuw sleutelpaar.
func GenerateKey() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// PrivateKey leest een geheime sleutel (32 bytes).
func PrivateKey(raw []byte) (*ecdh.PrivateKey, error) {
	return ecdh.X25519().NewPrivateKey(raw)
}

// PublicKey leest een publieke sleutel (base64, 32 bytes).
func PublicKey(b64 string) (*ecdh.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPublicKey(raw)
}

func aead(shared, epk, pk []byte) (cipher.AEAD, error) {
	salt := append(append([]byte{}, epk...), pk...)
	key, err := hkdf.Key(sha256.New, shared, salt, Info, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Seal versleutelt plain naar pk.
func Seal(pk *ecdh.PublicKey, plain, aad []byte) (string, error) {
	eph, err := GenerateKey()
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pk)
	if err != nil {
		return "", err
	}
	epk := eph.PublicKey().Bytes()
	g, err := aead(shared, epk, pk.Bytes())
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append(append([]byte{}, epk...), nonce...), g.Seal(nil, nonce, plain, aad)...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// ErrOpen is elke fout bij het openen: verkeerde sleutel, ander domein,
// beschadigd.
var ErrOpen = errors.New("kan de versleutelde inlog niet openen (andere sleutel of ander domein)")

// Open ontsleutelt met de geheime sleutel.
func Open(sk *ecdh.PrivateKey, sealed string, aad []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 32+12+16 {
		return nil, ErrOpen
	}
	epkRaw, nonce, ct := raw[:32], raw[32:44], raw[44:]
	epk, err := ecdh.X25519().NewPublicKey(epkRaw)
	if err != nil {
		return nil, ErrOpen
	}
	shared, err := sk.ECDH(epk)
	if err != nil {
		return nil, ErrOpen
	}
	g, err := aead(shared, epkRaw, sk.PublicKey().Bytes())
	if err != nil {
		return nil, ErrOpen
	}
	plain, err := g.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
