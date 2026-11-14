package config

import (
	"github.com/nemo-crypto/pay/internal/auth"
	"strings"
	"testing"
)

func TestWechatConfiguration(t *testing.T) {
	for _, k := range []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "ALLSCALE_API_KEY", "ALLSCALE_API_SECRET", "PLANS_FILE", "DAILY_LIMIT_NT", "CHECKOUT_ALLOWED_ORIGINS", "WECHAT_MCH_ID", "WECHAT_MERCHANT_SERIAL", "WECHAT_PRIVATE_KEY_FILE", "WECHAT_API_V3_KEY", "WECHAT_PLATFORM_KEY_ID", "WECHAT_PLATFORM_PUBLIC_KEY_FILE", "WECHAT_APP_ID", "WECHAT_MINI_APP_ID", "WECHAT_MOBILE_APP_ID", "WECHAT_TWD_PER_CNY", "WECHAT_TRUSTED_PROXY_CIDRS"} {
		t.Setenv(k, "")
	}
	t.Setenv("PAY_MODE", "live")
	t.Setenv("PUBLIC_URL", "https://pay.example.com")
	t.Setenv("JWT_SECRET", "a-long-enough-secret-for-testing-only")
	if c, err := Load(); err != nil || len(c.Wechat.Scenes()) != 0 {
		t.Fatal("disabled WeChat configuration", c.Wechat.Scenes(), err)
	}
	t.Setenv("WECHAT_MINI_APP_ID", "wx_mini")
	if _, err := Load(); err == nil {
		t.Fatal("accepted partial merchant configuration")
	}
	for k, v := range map[string]string{"WECHAT_MCH_ID": "merchant", "WECHAT_MERCHANT_SERIAL": "serial", "WECHAT_PRIVATE_KEY_FILE": "/run/secrets/key.pem", "WECHAT_API_V3_KEY": "0123456789abcdef0123456789abcdef", "WECHAT_PLATFORM_KEY_ID": "PUB_KEY_ID_TEST", "WECHAT_PLATFORM_PUBLIC_KEY_FILE": "/run/secrets/public.pem", "WECHAT_TWD_PER_CNY": "4.50"} {
		t.Setenv(k, v)
	}
	if c, err := Load(); err != nil || len(c.Wechat.Scenes()) != 1 || c.Wechat.Scenes()[0] != "mini" {
		t.Fatal("mini-only configuration", c.Wechat.Scenes(), err)
	}
	t.Setenv("WECHAT_TRUSTED_PROXY_CIDRS", "not-a-cidr")
	if _, err := Load(); err == nil {
		t.Fatal("accepted invalid proxy CIDR")
	}
	t.Setenv("WECHAT_TRUSTED_PROXY_CIDRS", "127.0.0.0/8,10.0.0.0/8")
	t.Setenv("PAY_MODE", "demo")
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:8098")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8098")
	t.Setenv("WECHAT_API_V3_KEY", "invalid-real-key")
	if c, err := Load(); err != nil || len(c.Wechat.Scenes()) != 5 || c.Wechat.APIv3Key == "invalid-real-key" {
		t.Fatal("demo should use only mock credentials", err)
	}
}

func TestLiveConfiguration(t *testing.T) {
	for _, k := range []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "ALLSCALE_API_KEY", "ALLSCALE_API_SECRET", "ALLSCALE_TWD_PER_USD", "PLANS_FILE", "DAILY_LIMIT_NT", "CHECKOUT_ALLOWED_ORIGINS", "STRIPE_LIVE"} {
		t.Setenv(k, "")
	}
	t.Setenv("PAY_MODE", "live")
	t.Setenv("PUBLIC_URL", "https://pay.example.com")
	t.Setenv("JWT_SECRET", "a-long-enough-secret-for-testing-only")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STRIPE_SECRET_KEY", "not-a-stripe-key")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	if _, err := Load(); err == nil {
		t.Fatal("invalid key accepted")
	}
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_example")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUBLIC_URL", "http://pay.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("insecure live URL accepted")
	}
}

func TestDemoCannotMintTokensUsingLiveSecret(t *testing.T) {
	t.Setenv("PAY_MODE", "demo")
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:8098")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8098")
	t.Setenv("JWT_SECRET", "a-dedicated-live-secret-not-for-public-demo")
	t.Setenv("CHECKOUT_ALLOWED_ORIGINS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != DemoJWTSecret {
		t.Fatal("demo adopted configured live signing key")
	}
	token, err := (auth.Auth{Secret: cfg.JWTSecret, Issuer: cfg.Issuer, Audience: cfg.Audience}).Token("demo-user", "user")
	if err != nil {
		t.Fatal(err)
	}
	live := auth.Auth{Secret: "a-dedicated-live-secret-not-for-public-demo", Issuer: cfg.Issuer, Audience: cfg.Audience}
	if _, err = live.Parse("Bearer " + token); err == nil {
		t.Fatal("demo token accepted by live authentication")
	}
}

func TestLiveRejectsPublicDemoAndBlankSigningKeys(t *testing.T) {
	for _, secret := range []string{DemoJWTSecret, "  " + DemoJWTSecret + "  ", strings.Repeat(" ", 64)} {
		t.Run("invalid-key", func(t *testing.T) {
			t.Setenv("PAY_MODE", "live")
			t.Setenv("PUBLIC_URL", "https://pay.example.com")
			t.Setenv("JWT_SECRET", secret)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe live signing key accepted")
			}
		})
	}
}

func TestEnvironmentKeys(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		stripe       bool
		onramp, want string
	}{
		{"demo", false, "sandbox", "demo"},
		{"demo", true, "production", "demo"},
		{"live", false, "sandbox", "live:stripe_test:onramp_sandbox"},
		{"live", true, "sandbox", "live:stripe_live:onramp_sandbox"},
		{"live", false, "production", "live:stripe_test:onramp_production"},
	} {
		cfg := Config{Mode: tc.mode, StripeLive: tc.stripe, OnrampEnvironment: tc.onramp}
		if got := cfg.EnvironmentKey(); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}

func TestLiveRejectsPublicDemoPaymentCredentials(t *testing.T) {
	for _, pair := range [][2]string{
		{"STRIPE_WEBHOOK_SECRET", "whsec_local_demo_only"},
		{"ALLSCALE_API_KEY", "local-demo-key"},
		{"ALLSCALE_API_SECRET", "local-demo-secret"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			for _, key := range []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "ALLSCALE_API_KEY", "ALLSCALE_API_SECRET", "CHECKOUT_ALLOWED_ORIGINS"} {
				t.Setenv(key, "")
			}
			t.Setenv("PAY_MODE", "live")
			t.Setenv("PUBLIC_URL", "https://pay.example.com")
			t.Setenv("JWT_SECRET", "dedicated-random-test-key-with-at-least-32-bytes")
			t.Setenv(pair[0], pair[1])
			if _, err := Load(); err == nil {
				t.Fatal("public demo payment credential accepted")
			}
		})
	}
}
