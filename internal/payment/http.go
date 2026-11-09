package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func Client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func BodyHash(body []byte) string { s := sha256.Sum256(body); return hex.EncodeToString(s[:]) }
func MAC(secret, canonical string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(canonical))
	return "v1=" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}
func fresh(ts string, now time.Time) bool {
	n, e := strconv.ParseInt(ts, 10, 64)
	return e == nil && n >= now.Unix()-300 && n <= now.Unix()+300
}
func readResponse(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("provider response too large")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return nil, fmt.Errorf("provider HTTP %d", r.StatusCode)
	}
	return b, nil
}
func ValidateCheckout(raw string, gateway string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return fmt.Errorf("invalid checkout URL")
	}
	host := strings.ToLower(u.Hostname())
	want := "checkout.stripe.com"
	if gateway == "allscale" {
		want = "checkout.allscale.io"
	}
	if host != want {
		return fmt.Errorf("unexpected checkout host")
	}
	return nil
}
