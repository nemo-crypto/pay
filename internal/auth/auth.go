package auth

import (
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"strings"
	"time"
)

type Claims struct {
	Role  string `json:"role"`
	Guest bool   `json:"guest"`
	jwt.RegisteredClaims
}
type Auth struct{ Secret, Issuer, Audience string }

func (a Auth) Token(user, role string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: user, Issuer: a.Issuer, Audience: jwt.ClaimStrings{a.Audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now())}}).SignedString([]byte(a.Secret))
}
func (a Auth) Parse(header string) (*Claims, error) {
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, fmt.Errorf("missing bearer token")
	}
	c := &Claims{}
	t, e := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), c, func(t *jwt.Token) (any, error) { return []byte(a.Secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(a.Issuer), jwt.WithAudience(a.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(10*time.Second))
	if e != nil || !t.Valid || c.Subject == "" || len(c.Subject) > 128 {
		return nil, fmt.Errorf("invalid token")
	}
	return c, nil
}
