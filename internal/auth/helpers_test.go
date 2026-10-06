package auth

import "time"

func generateSessionToken(user, secret string, expiresAt time.Time) (string, error) {
	return signSessionToken(user, secret, expiresAt, false)
}
