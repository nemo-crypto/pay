package auth

import (
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

func TestTokenValidation(t *testing.T) {
	a := Auth{Secret: "test-secret", Issuer: "issuer", Audience: "audience"}
	for _, name := range []string{"valid", "expired", "issuer", "audience", "algorithm", "missing_expiry"} {
		t.Run(name, func(t *testing.T) {
			c := Claims{Guest: true, RegisteredClaims: jwt.RegisteredClaims{Subject: "alice", Issuer: a.Issuer, Audience: jwt.ClaimStrings{a.Audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
			method := jwt.SigningMethodHS256
			switch name {
			case "expired":
				c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
			case "issuer":
				c.Issuer = "wrong"
			case "audience":
				c.Audience = jwt.ClaimStrings{"wrong"}
			case "algorithm":
				method = jwt.SigningMethodHS384
			case "missing_expiry":
				c.ExpiresAt = nil
			}
			raw, err := jwt.NewWithClaims(method, c).SignedString([]byte(a.Secret))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := a.Parse("Bearer " + raw)
			if name == "valid" {
				if err != nil || !parsed.Guest {
					t.Fatal(parsed, err)
				}
			} else if err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}
