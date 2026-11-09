package config

import "testing"

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
