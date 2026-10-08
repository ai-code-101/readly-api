// Package otp holds the phone-number and one-time-code helpers.
package otp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

var ErrInvalidPhone = errors.New("enter a valid Kenyan mobile number, e.g. 0712 345 678")

// NormalizePhone turns 0712…, 712…, 254712… or +254 712 … into 254712345678.
// Kenyan mobile numbers start with 7 or 1 after the country code.
func NormalizePhone(raw string) (string, error) {
	var d strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			d.WriteRune(r)
		}
	}
	s := d.String()
	switch {
	case len(s) == 12 && strings.HasPrefix(s, "254"):
		s = s[3:]
	case len(s) == 10 && s[0] == '0':
		s = s[1:]
	case len(s) == 9:
	default:
		return "", ErrInvalidPhone
	}
	if s[0] != '7' && s[0] != '1' {
		return "", ErrInvalidPhone
	}
	return "254" + s, nil
}

// NewCode returns a cryptographically random 6-digit code.
func NewCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// HashCode is an HMAC so a leaked table can't be brute-forced without the secret.
func HashCode(secret []byte, phone, purpose, code string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(phone + "|" + purpose + "|" + code))
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compares two hashes in constant time.
func Equal(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// NewSessionToken returns a random session token and the hash to store for it.
func NewSessionToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Mask shows a phone number as 2547••••697 for logs and the UI.
func Mask(phone string) string {
	if len(phone) < 7 {
		return phone
	}
	return phone[:4] + strings.Repeat("•", len(phone)-7) + phone[len(phone)-3:]
}
