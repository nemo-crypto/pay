package onramp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/golang-jwt/jwt/v5"
)

// MockTransport 本地模拟远端 API，禁止网络透传；使用与真实网关相同的适配器。
type MockTransport struct {
	DB      *sql.DB
	Adapter *Adapter
}

func (m *MockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	a := m.Adapter
	reply := func(v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	}
	if a.Name == "transak" && strings.HasSuffix(r.URL.Path, "/refresh-token") {
		if r.Header.Get("api-secret") != a.Secret || r.Header.Get("x-api-key") != a.Key {
			return nil, fmt.Errorf("mock auth failed")
		}
		return reply(map[string]any{"data": map[string]any{"accessToken": "mock-transak-partner-access-token", "expiresAt": time.Now().Add(7 * 24 * time.Hour).Unix()}})
	}
	if a.Name == "transak" && strings.HasSuffix(r.URL.Path, "/auth/session") {
		if r.Header.Get("access-token") != "mock-transak-partner-access-token" || r.Header.Get("x-api-key") != a.Key || r.Header.Get("x-user-ip") == "" {
			return nil, fmt.Errorf("mock session headers missing")
		}
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			return nil, fmt.Errorf("invalid session request")
		}
		id := str(object(v, "widgetParams"), "partnerOrderId")
		if id == "" {
			return nil, fmt.Errorf("missing partner order")
		}
		return reply(map[string]any{"data": map[string]any{"widgetUrl": a.Widget + "/?sessionId=mock_" + id}})
	}
	var id string
	switch a.Name {
	case "moonpay":
		if r.URL.Query().Get("apiKey") != a.Key {
			return nil, fmt.Errorf("mock API key mismatch")
		}
		id = strings.TrimPrefix(r.URL.Path, "/v1/transactions/ext/")
	case "alchemypay":
		ts := r.Header.Get("timestamp")
		if r.Header.Get("appid") != a.Key || r.Header.Get("sign") != b64mac(a.Secret, ts+"GET"+r.URL.Path+"?"+alchemyQuery(r.URL.Query())) {
			return nil, fmt.Errorf("mock Alchemy API signature mismatch")
		}
		id = r.URL.Query().Get("merchantOrderNo")
	case "transak":
		if r.Header.Get("access-token") != "mock-transak-partner-access-token" {
			return nil, fmt.Errorf("mock token mismatch")
		}
		id = strings.TrimPrefix(r.URL.Path, "/partners/api/v2/order/mock_transak_")
	case "ramp":
		if r.URL.Query().Get("secret") != "mock-view-token" {
			return nil, fmt.Errorf("mock view token mismatch")
		}
		id = strings.TrimPrefix(r.URL.Path, "/api/host-api/purchase/mock_ramp_")
	}
	var raw string
	if e := m.DB.QueryRowContext(r.Context(), "SELECT payload FROM onramp_mock WHERE id=? AND provider=?", id, a.Name).Scan(&raw); e != nil {
		return nil, e
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil, fmt.Errorf("invalid mock state")
	}
	if a.Name == "moonpay" {
		return reply([]any{v})
	}
	if a.Name == "alchemypay" || a.Name == "transak" {
		return reply(map[string]any{"data": v})
	}
	return reply(v)
}
func mockPayload(o Order, status string) map[string]any {
	// 仅为教学：模拟扣除固定 1 USD 后交付 USDC，不代表任何供应商真实费率。
	crypto := dollars(o.FiatMinor - 100)
	tx := ""
	if status == "completed" {
		tx = "0x" + strings.Repeat("a", 64)
	}
	ref := "mock_" + o.Provider + "_" + o.ID
	switch o.Provider {
	case "moonpay":
		st := map[string]string{"pending": "waitingPayment", "processing": "pending", "completed": "completed", "failed": "failed", "cancelled": "failed", "refunded": "failed"}[status]
		return map[string]any{"id": ref, "externalTransactionId": o.ID, "walletAddress": o.Wallet, "baseCurrency": map[string]string{"code": "usd"}, "currency": map[string]string{"code": "usdc"}, "baseCurrencyAmount": json.Number(dollars(o.FiatMinor)), "quoteCurrencyAmount": json.Number(crypto), "status": st, "cryptoTransactionId": tx}
	case "alchemypay":
		st := map[string]string{"pending": "PENDING", "processing": "TRANSFER", "completed": "FINISHED", "failed": "PAY_FAIL", "cancelled": "CANCEL", "refunded": "REFUNDED"}[status]
		return map[string]any{"appId": "mock-public-alchemypay", "orderNo": ref, "merchantOrderNo": o.ID, "address": o.Wallet, "fiat": "USD", "crypto": "USDC", "network": "ETH", "amount": dollars(o.FiatMinor), "cryptoQuantity": crypto, "txHash": tx, "status": st}
	case "transak":
		st := map[string]string{"pending": "AWAITING_PAYMENT_FROM_USER", "processing": "PENDING_DELIVERY_FROM_TRANSAK", "completed": "COMPLETED", "failed": "FAILED", "cancelled": "CANCELLED", "refunded": "REFUNDED"}[status]
		return map[string]any{"id": ref, "partnerOrderId": o.ID, "walletAddress": o.Wallet, "fiatCurrency": "USD", "cryptoCurrency": "USDC", "network": "ethereum", "fiatAmount": json.Number(dollars(o.FiatMinor)), "cryptoAmount": json.Number(crypto), "transactionHash": tx, "status": st}
	case "ramp":
		st := map[string]string{"pending": "INITIALIZED", "processing": "RELEASING", "completed": "RELEASED", "failed": "EXPIRED", "cancelled": "CANCELLED", "refunded": "RETURNED"}[status]
		return map[string]any{"id": ref, "receiverAddress": o.Wallet, "fiatCurrency": "USD", "fiatValue": json.Number(dollars(o.FiatMinor)), "asset": map[string]any{"symbol": "ETH_USDC", "decimals": 6}, "cryptoAmount": strconv.FormatInt((o.FiatMinor-100)*10000, 10), "finalTxHash": tx, "purchaseViewToken": "mock-view-token", "status": st}
	}
	return nil
}
func (a *Adapter) mockEvent(o Order, payload map[string]any) (*http.Request, []byte, error) {
	var body any
	h := make(http.Header)
	switch a.Name {
	case "moonpay":
		body = map[string]any{"type": "transaction_updated", "data": payload}
	case "alchemypay":
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		h.Set("timestamp", ts)
		canon, e := alchemyCanonical(payload)
		if e != nil {
			return nil, nil, e
		}
		payload["newSignature"] = b64mac(a.Secret, ts+"POST/onramp/webhooks/alchemypay"+string(canon))
		body = payload
	case "transak":
		token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"webhookData": payload, "eventID": "ORDER_UPDATE", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("mock-transak-partner-access-token"))
		if e != nil {
			return nil, nil, e
		}
		body = map[string]string{"data": token}
	case "ramp":
		typ := "CREATED"
		if str(payload, "status") == "RELEASED" {
			typ = "RELEASED"
		}
		if str(payload, "status") == "RETURNED" {
			typ = "RETURNED"
		}
		body = map[string]any{"type": typ, "purchase": payload}
	}
	b, e := json.Marshal(body)
	if e != nil {
		return nil, nil, e
	}
	if a.Name == "moonpay" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		h.Set("Moonpay-Signature-V2", "t="+ts+",s="+hex.EncodeToString(mac(a.WebhookSecret, ts+"."+string(b))))
	}
	if a.Name == "ramp" {
		canon, e := canonical(b)
		if e != nil {
			return nil, nil, e
		}
		sum := sha256.Sum256(canon)
		h.Set("X-Body-Signature", base64.StdEncoding.EncodeToString(ecdsa.Sign(demoRampKey(), sum[:]).Serialize()))
	}
	r, e := http.NewRequestWithContext(context.Background(), "POST", a.callback(o), strings.NewReader(string(b)))
	if e != nil {
		return nil, nil, e
	}
	r.Header = h
	return r, b, nil
}
func mockRef(o Order) Order {
	o.Ref = "mock_" + o.Provider + "_" + o.ID
	if o.Provider == "ramp" {
		o.ViewToken = "mock-view-token"
	}
	return o
}
func localCheckout(public, id string) string {
	return public + "/onramp/checkout?order_id=" + url.QueryEscape(id)
}
