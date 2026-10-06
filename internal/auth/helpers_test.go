package auth

import "time"

func generateSessionToken(user, secret string, expiresAt time.Time) (string, error) {
	return signSessionToken(user, secret, expiresAt, false)
}

func parseStashToken(token, secret string) (string, error) {
	user, _, err := parseStashTokenClaims(token, secret)
	return user, err
}
