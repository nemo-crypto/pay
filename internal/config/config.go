package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Plan struct {
	ID       string `json:"id"`
	NTAmount int64  `json:"nt_amount"`
	Coins    int64  `json:"coin_amount"`
}
type Config struct {
	Mode, Addr, Database, PublicURL, JWTSecret, Issuer, Audience        string
	StripeKey, StripeWebhookSecret, AllscaleKey, AllscaleSecret, FXRate string
	StripeLive, AllscaleSigned                                          bool
	Origins                                                             []string
	Plans                                                               []Plan
	DailyLimit                                                          int64
	Custom                                                              bool
	Interval                                                            time.Duration
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func Load() (Config, error) {
	c := Config{Mode: env("PAY_MODE", "demo"), Addr: env("HTTP_ADDR", "127.0.0.1:8080"), Database: env("DATABASE_PATH", "data/pay.db"), PublicURL: strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:8080"), "/"), JWTSecret: os.Getenv("JWT_SECRET"), Issuer: env("JWT_ISSUER", "pay-demo"), Audience: env("JWT_AUDIENCE", "pay-api"), StripeKey: os.Getenv("STRIPE_SECRET_KEY"), StripeWebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"), AllscaleKey: os.Getenv("ALLSCALE_API_KEY"), AllscaleSecret: os.Getenv("ALLSCALE_API_SECRET"), FXRate: os.Getenv("ALLSCALE_TWD_PER_USD"), StripeLive: os.Getenv("STRIPE_LIVE") == "true", AllscaleSigned: os.Getenv("ALLSCALE_SIGNED_RESPONSES") == "true", Custom: os.Getenv("CUSTOM_TOPUP_ENABLED") == "true", DailyLimit: 100000, Interval: 30 * time.Second}
	if raw := os.Getenv("DAILY_LIMIT_NT"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 || n > 10000000 {
			return c, fmt.Errorf("invalid DAILY_LIMIT_NT")
		}
		c.DailyLimit = n
	}
	c.Origins = strings.Split(env("CHECKOUT_ALLOWED_ORIGINS", c.PublicURL), ",")
	c.Plans = []Plan{{"demo-1000", 1000, 2000}, {"demo-2000", 2000, 4120}, {"demo-5000", 5000, 10800}}
	if f := os.Getenv("PLANS_FILE"); f != "" {
		b, e := os.ReadFile(f)
		if e != nil {
			return c, e
		}
		if e = json.Unmarshal(b, &c.Plans); e != nil {
			return c, e
		}
	}
	if c.Mode != "demo" && c.Mode != "live" {
		return c, fmt.Errorf("PAY_MODE must be demo or live")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return c, fmt.Errorf("PUBLIC_URL must be an origin")
	}
	if c.Mode == "live" {
		if u.Scheme != "https" || len(c.JWTSecret) < 32 {
			return c, fmt.Errorf("live mode requires HTTPS PUBLIC_URL and JWT_SECRET >=32 bytes")
		}
		if c.StripeKey != "" && (c.StripeWebhookSecret == "" || (!strings.HasPrefix(c.StripeKey, "sk_test_") && !strings.HasPrefix(c.StripeKey, "sk_live_")) || strings.HasPrefix(c.StripeKey, "sk_live_") != c.StripeLive) {
			return c, fmt.Errorf("Stripe key/mode/webhook configuration mismatch")
		}
		if (c.AllscaleKey != "" || c.AllscaleSecret != "") && (c.AllscaleKey == "" || c.AllscaleSecret == "" || c.FXRate == "") {
			return c, fmt.Errorf("Allscale requires key, secret and ALLSCALE_TWD_PER_USD quote")
		}
	}
	if c.Mode == "demo" {
		if !strings.HasPrefix(c.Addr, "127.0.0.1:") && !strings.HasPrefix(c.Addr, "localhost:") {
			return c, fmt.Errorf("demo must bind loopback")
		}
		if c.JWTSecret == "" {
			c.JWTSecret = "local-demo-only-do-not-use-in-production-2026"
		}
		if c.FXRate == "" {
			c.FXRate = "32.00"
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Plans {
		if p.ID == "" || seen[p.ID] || p.NTAmount < 1 || p.NTAmount > 500000 || p.Coins < 1 || p.Coins > 100000000 {
			return c, fmt.Errorf("invalid plan")
		}
		seen[p.ID] = true
	}
	if len(c.Plans) == 0 {
		return c, fmt.Errorf("plans cannot be empty")
	}
	for i, o := range c.Origins {
		c.Origins[i] = strings.TrimSpace(o)
		v, e := url.Parse(c.Origins[i])
		if e != nil || v.Host == "" || v.User != nil || v.Path != "" || v.RawQuery != "" || v.Fragment != "" || (v.Scheme != "https" && !(c.Mode == "demo" && v.Scheme == "http")) {
			return c, fmt.Errorf("invalid allowed origin")
		}
	}
	return c, nil
}
