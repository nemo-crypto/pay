package onramp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nemo-crypto/pay/internal/payment"
)

type Adapter struct {
	TokenStore                                                                    *sql.DB
	Name, Key, Secret, WebhookSecret, PublicURL, API, SessionAPI, Widget, RampKey string
	HTTP                                                                          *http.Client
	Sandbox                                                                       bool
	tokenMu                                                                       sync.Mutex
	Token, PreviousToken                                                          string
	TokenExpires                                                                  time.Time
}

func Adapters(mode, public string) (map[string]*Adapter, error) {
	out := map[string]*Adapter{}
	if env := os.Getenv("ONRAMP_ENV"); env != "" && env != "sandbox" && env != "production" {
		return nil, fmt.Errorf("ONRAMP_ENV must be sandbox or production")
	}
	enabled := Names
	if mode != "demo" {
		enabled = nil
		if s := os.Getenv("ONRAMP_PROVIDERS"); s != "" {
			enabled = strings.Split(s, ",")
		}
	}
	for _, name := range enabled {
		name = strings.TrimSpace(name)
		a := &Adapter{Name: name, PublicURL: public, HTTP: payment.Client(), Sandbox: os.Getenv("ONRAMP_ENV") != "production"}
		switch name {
		case "moonpay":
			a.Key = os.Getenv("MOONPAY_PUBLIC_KEY")
			a.Secret = os.Getenv("MOONPAY_SECRET_KEY")
			a.WebhookSecret = os.Getenv("MOONPAY_WEBHOOK_KEY")
			a.API = "https://api.moonpay.com"
			a.Widget = "https://buy.moonpay.com"
			if a.Sandbox {
				a.Widget = "https://buy-sandbox.moonpay.com"
			}
		case "alchemypay":
			a.Key = os.Getenv("ALCHEMYPAY_APP_ID")
			a.Secret = os.Getenv("ALCHEMYPAY_APP_SECRET")
			a.WebhookSecret = a.Secret
			a.API = "https://openapi.alchemypay.org"
			a.Widget = "https://ramp.alchemypay.org"
			if a.Sandbox {
				a.API = "https://openapi-test.alchemypay.org"
				a.Widget = "https://ramptest.alchemypay.org"
			}
		case "transak":
			a.Key = os.Getenv("TRANSAK_API_KEY")
			a.Secret = os.Getenv("TRANSAK_API_SECRET")
			a.API = "https://api.transak.com"
			a.SessionAPI = "https://api-gateway.transak.com"
			a.Widget = "https://global.transak.com"
			if a.Sandbox {
				a.API = "https://api-stg.transak.com"
				a.SessionAPI = "https://api-gateway-stg.transak.com"
				a.Widget = "https://global-stg.transak.com"
			}
		case "ramp":
			a.Key = os.Getenv("RAMP_HOST_API_KEY")
			a.API = "https://api.rampnetwork.com/api"
			a.Widget = "https://app.rampnetwork.com"
			a.RampKey = rampProductionKey
			if a.Sandbox {
				a.API = "https://api.demo.rampnetwork.com/api"
				a.Widget = "https://app.demo.rampnetwork.com"
				a.RampKey = rampSandboxKey
			}
		default:
			return nil, fmt.Errorf("unknown ONRAMP_PROVIDERS entry")
		}
		if mode == "demo" {
			a.Key = "mock-public-" + name
			a.Secret = "mock-secret-" + name
			a.WebhookSecret = "mock-webhook-" + name
			a.Token = "mock-transak-partner-access-token"
			a.TokenExpires = time.Now().Add(7 * 24 * time.Hour)
			a.RampKey = "demo"
		} else {
			if a.Key == "mock-public-"+name || a.Secret == "mock-secret-"+name || a.WebhookSecret == "mock-webhook-"+name {
				return nil, fmt.Errorf("public demo credentials cannot be used for live %s", name)
			}
			if a.Key == "" || (name != "ramp" && a.Secret == "") || (name == "moonpay" && a.WebhookSecret == "") {
				return nil, fmt.Errorf("missing %s onramp credentials", name)
			}
		}
		out[name] = a
	}
	return out, nil
}
func (a *Adapter) call(ctx context.Context, method, endpoint string, headers map[string]string, body any) ([]byte, error) {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := a.HTTP.Do(r)
	if err != nil {
		return nil, fmt.Errorf("provider transport failed")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("provider response invalid")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("provider HTTP %d", res.StatusCode)
	}
	return raw, nil
}
func (a *Adapter) accessToken(ctx context.Context) (string, error) {
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()
	if a.Token != "" && time.Now().Add(time.Minute).Before(a.TokenExpires) {
		return a.Token, nil
	}
	b, e := a.call(ctx, "POST", a.API+"/partners/api/v2/refresh-token", map[string]string{"x-api-key": a.Key, "api-secret": a.Secret}, map[string]string{"apiKey": a.Key})
	if e != nil {
		return "", e
	}
	m, e := jsonMap(b)
	if e != nil {
		return "", e
	}
	d := object(m, "data")
	token := str(d, "accessToken")
	exp, e := strconv.ParseInt(str(d, "expiresAt"), 10, 64)
	if e != nil || token == "" || exp <= time.Now().Unix() {
		return "", fmt.Errorf("invalid partner token")
	}
	if a.TokenStore != nil {
		if _, err := a.TokenStore.ExecContext(ctx, "INSERT INTO onramp_tokens VALUES(?,?,?,?) ON CONFLICT(provider) DO UPDATE SET token=excluded.token,previous=excluded.previous,expires=excluded.expires", a.Name, token, a.Token, exp); err != nil {
			return "", fmt.Errorf("token persistence failed")
		}
	}
	a.PreviousToken = a.Token
	a.Token = token
	a.TokenExpires = time.Unix(exp, 0)
	return token, nil
}
func (a *Adapter) callback(o Order) string {
	return a.PublicURL + "/onramp/webhooks/" + a.Name + "?order_id=" + url.QueryEscape(o.ID) + "&binding=" + url.QueryEscape(o.CallbackToken)
}
func (a *Adapter) Create(ctx context.Context, o Order) (string, error) {
	finish := a.PublicURL + "/onramp?order_id=" + url.QueryEscape(o.ID)
	q := url.Values{}
	switch a.Name {
	case "moonpay":
		q = url.Values{"apiKey": {a.Key}, "currencyCode": {"usdc"}, "baseCurrencyCode": {"usd"}, "baseCurrencyAmount": {dollars(o.FiatMinor)}, "walletAddress": {o.Wallet}, "externalTransactionId": {o.ID}, "redirectURL": {finish}, "lockAmount": {"true"}}
		raw := "?" + q.Encode()
		return a.Widget + raw + "&signature=" + url.QueryEscape(b64mac(a.Secret, raw)), nil
	case "alchemypay":
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		q = url.Values{"appId": {a.Key}, "timestamp": {ts}, "merchantOrderNo": {o.ID}, "crypto": {"USDC"}, "network": {"ETH"}, "fiat": {"USD"}, "fiatAmount": {dollars(o.FiatMinor)}, "address": {o.Wallet}, "showTable": {"buy"}, "callbackUrl": {a.callback(o)}, "redirectUrl": {finish}}
		q.Set("sign", b64mac(a.Secret, ts+"GET/index/rampPageBuy?"+alchemyQuery(q)))
		return a.Widget + "?" + q.Encode(), nil
	case "transak":
		token, e := a.accessToken(ctx)
		if e != nil {
			return "", e
		}
		params := map[string]any{"apiKey": a.Key, "referrerDomain": a.PublicURL, "fiatCurrency": "USD", "fiatAmount": json.Number(dollars(o.FiatMinor)), "cryptoCurrencyCode": "USDC", "network": "ethereum", "walletAddress": o.Wallet, "disableWalletAddressForm": true, "partnerOrderId": o.ID, "redirectURL": finish, "productsAvailed": "BUY"}
		b, e := a.call(ctx, "POST", a.SessionAPI+"/api/v2/auth/session", map[string]string{"access-token": token, "x-api-key": a.Key, "x-user-ip": o.ClientIP}, map[string]any{"widgetParams": params})
		if e != nil {
			return "", e
		}
		m, e := jsonMap(b)
		if e != nil {
			return "", e
		}
		raw := str(object(m, "data"), "widgetUrl")
		u, e := url.Parse(raw)
		expected, _ := url.Parse(a.Widget)
		if e != nil || u.Scheme != "https" || u.Host != expected.Host || u.User != nil {
			return "", fmt.Errorf("invalid Transak widget URL")
		}
		return raw, nil
	case "ramp":
		q = url.Values{"hostApiKey": {a.Key}, "hostAppName": {"Pay Lab"}, "swapAsset": {"ETH_USDC"}, "fiatCurrency": {"USD"}, "fiatValue": {dollars(o.FiatMinor)}, "userAddress": {o.Wallet}, "webhookStatusUrl": {a.callback(o)}, "finalUrl": {finish}, "enabledFlows": {"ONRAMP"}}
		return a.Widget + "?" + q.Encode(), nil
	}
	return "", fmt.Errorf("unknown provider")
}

// Alchemy 的签名示例使用排序后的原始参数值；只对最终 URL 编码。
func alchemyQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if k != "sign" && q.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+q.Get(k))
	}
	return strings.Join(parts, "&")
}
func (a *Adapter) Fetch(ctx context.Context, o Order) (Snapshot, error) {
	var b []byte
	var e error
	switch a.Name {
	case "moonpay":
		b, e = a.call(ctx, "GET", a.API+"/v1/transactions/ext/"+url.PathEscape(o.ID)+"?apiKey="+url.QueryEscape(a.Key), nil, nil)
		if e != nil {
			return Snapshot{}, e
		}
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) != nil {
			return Snapshot{}, fmt.Errorf("invalid transaction list")
		}
		if len(arr) == 0 {
			return Snapshot{}, ErrPending
		}
		if len(arr) != 1 {
			return Snapshot{}, fmt.Errorf("ambiguous provider transactions")
		}
		b = arr[0]
	case "alchemypay":
		path := "/open/api/v4/merchant/query/trade"
		q := url.Values{"merchantOrderNo": {o.ID}, "side": {"BUY"}}
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		b, e = a.call(ctx, "GET", a.API+path+"?"+q.Encode(), map[string]string{"appid": a.Key, "timestamp": ts, "sign": b64mac(a.Secret, ts+"GET"+path+"?"+alchemyQuery(q))}, nil)
	case "transak":
		if o.Ref == "" {
			return Snapshot{}, ErrPending
		}
		token, err := a.accessToken(ctx)
		if err != nil {
			return Snapshot{}, err
		}
		b, e = a.call(ctx, "GET", a.API+"/partners/api/v2/order/"+url.PathEscape(o.Ref), map[string]string{"x-api-key": a.Key, "access-token": token}, nil)
	case "ramp":
		if o.Ref == "" || o.ViewToken == "" {
			return Snapshot{}, ErrPending
		}
		b, e = a.call(ctx, "GET", a.API+"/host-api/purchase/"+url.PathEscape(o.Ref)+"?secret="+url.QueryEscape(o.ViewToken), nil, nil)
	}
	if e != nil {
		return Snapshot{}, e
	}
	m, e := jsonMap(b)
	if e != nil {
		return Snapshot{}, e
	}
	if d := object(m, "data"); d != nil {
		m = d
	}
	return normalize(a.Name, m)
}
