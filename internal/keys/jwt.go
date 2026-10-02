package keys

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Claims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Role     string `json:"role"`
	Subject  string `json:"sub,omitempty"`
	AAL      string `json:"aal,omitempty"`
	Issued   int64  `json:"iat"`
	Expires  int64  `json:"exp"`
	JSON     []byte `json:"-"`
}

func JWT(secret []byte, role string) (string, error) {
	return SignJWT(secret, map[string]any{
		"iss": "pibase", "role": role, "iat": time.Now().Unix(),
		"exp": time.Now().Add(10 * 365 * 24 * time.Hour).Unix(),
	})
}

func SignJWT(secret []byte, claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

func Hash(value string) []byte {
	h := sha256.Sum256([]byte(value))
	return h[:]
}

func EqualHash(value string, expected []byte) bool {
	return hmac.Equal(Hash(value), expected)
}

func VerifyJWT(token string, secret []byte, now time.Time) (Claims, error) {
	var claims Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("JWT must have three parts")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, fmt.Errorf("decode JWT header: %w", err)
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	decoder := json.NewDecoder(bytes.NewReader(headerBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return claims, fmt.Errorf("JWT header must specify HS256 and JWT")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, fmt.Errorf("decode JWT signature: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, fmt.Errorf("invalid JWT signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("decode JWT claims: %w", err)
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("decode JWT claims: %w", err)
	}
	claims.JSON = append([]byte(nil), payload...)
	if claims.Issuer != "pibase" {
		return claims, fmt.Errorf("invalid JWT issuer")
	}
	if claims.Expires <= now.Unix() {
		return claims, fmt.Errorf("JWT has expired")
	}
	if claims.Issued > now.Add(time.Minute).Unix() {
		return claims, fmt.Errorf("JWT issued in the future")
	}
	return claims, nil
}
