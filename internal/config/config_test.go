package config

import "testing"

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
