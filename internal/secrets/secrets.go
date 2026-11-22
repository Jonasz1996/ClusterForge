// Package secrets versleutelt geheimen die in de database staan, zoals het
// API-token van Proxmox, met AES-256-GCM. De sleutel komt uit de omgeving en
// staat nooit in de database.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrWrongKey betekent dat een geheim met een andere masterkey versleuteld is.
var ErrWrongKey = errors.New("geheim is met een andere masterkey versleuteld")

// PurposeFile is het doel van de sleutel voor vingerafdrukken van
// gerenderde bestanden, in het logboek en later bij drift.
const PurposeFile = "file fingerprint"

// Box versleutelt en ontsleutelt met één masterkey.
type Box struct {
	aead cipher.AEAD
	key  []byte
	// KeyID herkent de sleutel zonder hem prijs te geven; hij gaat mee in de
	// database zodat een gewijzigde sleutel een duidelijke fout geeft.
	KeyID string
}

// ParseKey leest een masterkey van 32 bytes in base64 of hex.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("masterkey moet 32 bytes zijn, in base64 of hex (maak er een met: openssl rand -hex 32)")
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("masterkey moet 32 bytes zijn")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("clusterforge-key-id:"), key...))
	return &Box{aead: aead, key: slices.Clone(key), KeyID: hex.EncodeToString(sum[:8])}, nil
}

// Derive leidt met HKDF een eigen sleutel af voor één doel. Wat met die
// sleutel gemaakt is, zoals een vingerafdruk in de database, verraadt de
// masterkey niet en is zonder de masterkey niet na te rekenen. Zonder
// masterkey (een nil Box) is er geen sleutel.
func (b *Box) Derive(purpose string) []byte {
	if b == nil {
		return nil
	}
	k, err := hkdf.Key(sha256.New, b.key, nil, "clusterforge "+purpose, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// Fingerprint is de HMAC-SHA256 van data met een afgeleide sleutel. Een
// gewone hash van een gerenderd bestand is offline te raden, want op het
// geheim na is de inhoud bekend; zonder de sleutel lukt dat niet.
func Fingerprint(key, data []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}

// Seal versleutelt plaintext. aad bindt het geheim aan zijn plaats, zoals
// "proxmox:<id>", zodat een gekopieerde waarde elders niet ontsleutelt.
func (b *Box) Seal(plaintext, aad []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad)
}

// Open ontsleutelt wat Seal maakte. keyID is de KeyID van toen.
func (b *Box) Open(ciphertext, aad []byte, keyID string) ([]byte, error) {
	if keyID != b.KeyID {
		return nil, ErrWrongKey
	}
	n := b.aead.NonceSize()
	if len(ciphertext) < n {
		return nil, errors.New("geheim is beschadigd")
	}
	out, err := b.aead.Open(nil, ciphertext[:n], ciphertext[n:], aad)
	if err != nil {
		return nil, errors.New("geheim is beschadigd of hoort niet bij deze plaats")
	}
	return out, nil
}
