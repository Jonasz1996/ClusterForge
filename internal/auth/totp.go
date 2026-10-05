package auth

import (
	"crypto/subtle"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpIssuer = "ClusterForge"
	totpPeriod = 30
	// totpSkew is het aantal periodes ernaast dat nog geldt, om kleine
	// klokverschillen op te vangen.
	totpSkew = 1
)

var totpOpts = totp.ValidateOpts{
	Period:    totpPeriod,
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
}

// newTOTPKey maakt een nieuw TOTP-geheim voor een gebruiker.
func newTOTPKey(username string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: username,
		Period:      totpPeriod,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
}

// matchTOTP zoekt de periode waarbij code hoort. De periode wordt bewaard,
// zodat dezelfde code niet twee keer gebruikt kan worden.
func matchTOTP(code, secret string, now time.Time) (step int64, ok bool) {
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		s := cur + d
		want, err := totp.GenerateCodeCustom(secret, time.Unix(s*totpPeriod, 0), totpOpts)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

func validTOTP(code, secret string, now time.Time) bool {
	_, ok := matchTOTP(code, secret, now)
	return ok
}
