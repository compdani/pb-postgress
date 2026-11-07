package bucket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalidToken is returned when a signed URL token does not match the object.
	ErrInvalidToken = errors.New("invalid token")

	// ErrExpiredToken is returned when a signed URL token is past its expiry.
	ErrExpiredToken = errors.New("expired token")
)

// SignToken builds an HMAC token for key that expires at exp.
//
// The token is base64url(key) + "." + unixSeconds + "." + base64url(HMAC-SHA256).
// The main instance can mint the same token from the bucket bearer secret
// stored in _fileHosts.secret. Clients pass it as the "token" query parameter.
func SignToken(secret, key string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(key)) + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyToken accepts token only when it was signed for key and is still valid at now.
// A token is expired at the expiry instant (now >= exp).
func VerifyToken(secret, key, token string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || secret == "" || key == "" {
		return ErrInvalidToken
	}
	rawKey, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || string(rawKey) != key {
		return ErrInvalidToken
	}
	expUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ErrInvalidToken
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return ErrInvalidToken
	}
	if !now.Before(time.Unix(expUnix, 0)) {
		return ErrExpiredToken
	}
	return nil
}
