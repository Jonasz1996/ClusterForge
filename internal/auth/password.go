package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id-parameters volgens de tweede aanbeveling van RFC 9106:
// 64 MiB geheugen, 3 iteraties.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MinPasswordLength is de minimale wachtwoordlengte.
const MinPasswordLength = 12

var errBadHash = errors.New("ongeldige wachtwoordhash")

// argonSlots begrenst hoeveel hashes tegelijk lopen: elke hash neemt 64 MiB,
// en honderd aanmeldingen tegelijk zouden de server anders zonder geheugen
// zetten (en daarmee NATS en alle agents). De rest wacht even, maar nooit
// langer dan de request: een volle wachtrij geeft een fout in plaats van een
// login die eindeloos blijft hangen.
var argonSlots = make(chan struct{}, 4)

func argonKey(ctx context.Context, password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) ([]byte, error) {
	select {
	case argonSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wachten op wachtwoordcontrole: %w", ctx.Err())
	}
	defer func() { <-argonSlots }()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen), nil
}

// HashPassword geeft een argon2id-hash in PHC-formaat terug.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argonKey(ctx, []byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword controleert een wachtwoord tegen een hash uit HashPassword.
// De parameters komen uit de hash zelf, zodat oude hashes geldig blijven als
// de standaardparameters later strenger worden.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}
	got, err := argonKey(ctx, []byte(password), salt, time, memory, threads, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
