package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// newToken geeft 32 willekeurige bytes terug als URL-veilige string.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is de sleutel waaronder een sessie in de database staat. Een
// gelekte sessietabel geeft zo geen bruikbare cookies prijs.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
