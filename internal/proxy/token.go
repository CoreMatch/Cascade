package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

type tokenSigner struct {
	issuer string
	keyID  string
	key    ed25519.PrivateKey
}

type tokenClaims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	SessionID string `json:"sid"`
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	TokenID   string `json:"jti"`
}

func loadSigner(issuer, keyID, path string) (*tokenSigner, error) {
	if issuer == "" || keyID == "" || path == "" {
		return nil, errors.New("proxy issuer, key ID, and private key path are required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proxy signing key: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat proxy signing key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("proxy signing key permissions must not allow group or other access")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("proxy signing key is not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse proxy signing key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("proxy signing key must be Ed25519")
	}
	return &tokenSigner{issuer: issuer, keyID: keyID, key: key}, nil
}

func (s *tokenSigner) Sign(backendID, sessionID, gameUUID string, now time.Time) (string, error) {
	var jti [16]byte
	if _, err := rand.Read(jti[:]); err != nil {
		return "", fmt.Errorf("generate token ID: %w", err)
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": s.keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(tokenClaims{
		Issuer: s.issuer, Audience: backendID, SessionID: sessionID,
		Subject: gameUUID, IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * time.Second).Unix(),
		TokenID: base64.RawURLEncoding.EncodeToString(jti[:]),
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature := ed25519.Sign(s.key, []byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
