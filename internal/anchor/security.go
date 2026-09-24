package anchor

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"golang.org/x/crypto/argon2"
)

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
const compactAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"

var reservedCodes = map[string]bool{
	"admin": true, "api": true, "auth": true, "assets": true,
	"static": true, "setup": true, "favicon": true, "robots": true,
}

func validCode(code string, maxLength int) bool {
	if len(code) < 1 || len(code) > maxLength || reservedCodes[strings.ToLower(code)] {
		return false
	}
	for _, char := range code {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func randomCode(length int, excludeSimilar bool) (string, error) {
	characters := alphabet
	if excludeSimilar {
		characters = compactAlphabet
	}
	var result strings.Builder
	result.Grow(length)
	for i := 0; i < length; i++ {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(characters))))
		if err != nil {
			return "", err
		}
		result.WriteByte(characters[index.Int64()])
	}
	return result.String(), nil
}

func validDestination(raw string) bool {
	if len(raw) == 0 || len(raw) > 4096 || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return false
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 64*1024, 1, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=2,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" || parts[2] != "m=65536,t=2,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 2, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func newSession() (string, session, error) {
	token, err := randomBytes(32)
	if err != nil {
		return "", session{}, err
	}
	csrf, err := randomBytes(24)
	if err != nil {
		return "", session{}, err
	}
	return token, session{CSRF: csrf}, nil
}

func randomBytes(count int) (string, error) {
	bytes := make([]byte, count)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
