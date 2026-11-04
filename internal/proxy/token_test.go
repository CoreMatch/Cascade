package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTokenSignerCreatesBackendBoundEdDSAJWT(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := &tokenSigner{issuer: "proxy-test", keyID: "key-1", key: privateKey}
	now := time.Unix(1_800_000_000, 0)
	token, err := signer.Sign("backend-a", "11111111-2222-4333-8444-555555555555", "game-user", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d segments", len(parts))
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(publicKey, []byte(unsigned), signature) {
		t.Fatal("JWT signature did not verify")
	}
	headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "EdDSA" || header["kid"] != "key-1" {
		t.Fatalf("unexpected JWT header: %v", header)
	}
	claimsBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "proxy-test" || claims["aud"] != "backend-a" ||
		claims["sid"] != "11111111-2222-4333-8444-555555555555" || claims["sub"] != "game-user" {
		t.Fatalf("unexpected JWT claims: %v", claims)
	}
	if claims["exp"].(float64)-claims["iat"].(float64) != 30 {
		t.Fatalf("JWT lifetime is not 30 seconds: %v", claims)
	}
	if _, ok := claims["haUserUUID"]; ok {
		t.Fatal("JWT must not expose HA identity")
	}
}

// TestTokenSignerIssuesUniqueJti guards the proxy side of the one-time jti
// contract: every signed token must carry a fresh, non-empty token ID, so
// the backend's one-time jti ledger can never see a collision from the
// proxy. (The ledger itself — persistence and replay rejection — lives on
// the backend, e.g. data/CascadeJti.bin in the RW-HPS Go Server.)
func TestTokenSignerIssuesUniqueJti(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := &tokenSigner{issuer: "proxy-test", keyID: "key-1", key: privateKey}
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		token, err := signer.Sign("backend-a", "11111111-2222-4333-8444-555555555555", "game-user", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(token, ".")
		claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			TokenID string `json:"jti"`
		}
		if err := json.Unmarshal(claimsBytes, &claims); err != nil {
			t.Fatal(err)
		}
		if claims.TokenID == "" {
			t.Fatal("signed token has an empty jti")
		}
		if _, dup := seen[claims.TokenID]; dup {
			t.Fatalf("jti %q was issued twice", claims.TokenID)
		}
		seen[claims.TokenID] = struct{}{}
	}
}
