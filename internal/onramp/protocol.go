package onramp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/golang-jwt/jwt/v5"
)

// 官方公开的 SPKI 公钥，不是商户私钥。
const rampProductionKey = "MFYwEAYHKoZIzj0CAQYFK4EEAAoDQgAElvxpYOhgdAmI+7oL4mABRAfM5CwLkCbZm64ERVKAisSulWFC3oRZom/PeyE2iXPX1ekp9UD1r+51c9TiuIHU4w=="
const rampSandboxKey = "MFYwEAYHKoZIzj0CAQYFK4EEAAoDQgAEevN2PMEeIaaMkS4VIfXOqsLebj19kVeuwWl0AnkIA6DJU0r3ixkXVhJTltycJtkDoEAYtPHfARyTofB5ZNw9xA=="

func demoRampKey() *secp.PrivateKey {
	sum := sha256.Sum256([]byte("pay-lab-public-demo-key-not-for-production"))
	return secp.PrivKeyFromBytes(sum[:])
}
func rampPublic(key string) (*secp.PublicKey, error) {
	if key == "demo" {
		return demoRampKey().PubKey(), nil
	}
	b, e := base64.StdEncoding.DecodeString(key)
	if e != nil {
		return nil, e
	}
	var info struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	rest, e := asn1.Unmarshal(b, &info)
	if e != nil || len(rest) != 0 {
		return nil, fmt.Errorf("invalid ramp public key")
	}
	return secp.ParsePubKey(info.PublicKey.Bytes)
}
func canonical(b []byte) ([]byte, error) { return jsoncanonicalizer.Transform(b) }
func alchemyCanonical(m map[string]any) ([]byte, error) {
	clean := map[string]any{}
	for k, v := range m {
		if k != "signature" && k != "newSignature" && v != nil && v != "" {
			clean[k] = v
		}
	}
	b, e := json.Marshal(clean)
	if e != nil {
		return nil, e
	}
	return canonical(b)
}
func checkFresh(ts string, milliseconds bool) bool {
	n, e := strconv.ParseInt(ts, 10, 64)
	if e != nil {
		return false
	}
	if milliseconds {
		n /= 1000
	}
	return n >= time.Now().Unix()-300 && n <= time.Now().Unix()+300
}
func (a *Adapter) Verify(r *http.Request, b []byte) (Event, error) {
	m, e := jsonMap(b)
	if e != nil {
		return Event{}, e
	}
	var data map[string]any
	switch a.Name {
	case "moonpay":
		parts := map[string]string{}
		for _, part := range strings.Split(r.Header.Get("Moonpay-Signature-V2"), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			parts[k] = v
		}
		sig, err := hex.DecodeString(parts["s"])
		if err != nil || a.WebhookSecret == "" || !checkFresh(parts["t"], false) || !hmac.Equal(sig, mac(a.WebhookSecret, parts["t"]+"."+string(b))) {
			return Event{}, fmt.Errorf("invalid MoonPay signature")
		}
		switch str(m, "type") {
		case "transaction_created", "transaction_updated", "transaction_failed":
		default:
			return Event{}, fmt.Errorf("unsupported MoonPay event")
		}
		data = object(m, "data")
	case "alchemypay":
		ts := r.Header.Get("timestamp")
		canon, err := alchemyCanonical(m)
		if err != nil {
			return Event{}, err
		}
		sig, err := base64.StdEncoding.DecodeString(str(m, "newSignature"))
		if err != nil || !checkFresh(ts, true) || a.Secret == "" || str(m, "appId") != a.Key || !hmac.Equal(sig, mac(a.Secret, ts+"POST"+r.URL.Path+string(canon))) {
			return Event{}, fmt.Errorf("invalid Alchemy Pay signature")
		}
		data = m
	case "transak":
		a.tokenMu.Lock()
		keys := []string{a.Token, a.PreviousToken}
		a.tokenMu.Unlock()
		for _, key := range keys {
			if key == "" {
				continue
			}
			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(str(m, "data"), claims, func(*jwt.Token) (any, error) { return []byte(key), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithJSONNumber())
			if err == nil && token.Valid {
				data = object(map[string]any(claims), "webhookData")
				break
			}
		}
		if data == nil {
			return Event{}, fmt.Errorf("invalid Transak JWT")
		}
	case "ramp":
		sig, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Body-Signature"))
		if err != nil {
			return Event{}, err
		}
		signature, err := ecdsa.ParseDERSignature(sig)
		if err != nil {
			return Event{}, err
		}
		key, err := rampPublic(a.RampKey)
		if err != nil {
			return Event{}, err
		}
		canon, err := canonical(b)
		if err != nil {
			return Event{}, err
		}
		sum := sha256.Sum256(canon)
		if !signature.Verify(sum[:], key) {
			return Event{}, fmt.Errorf("invalid Ramp signature")
		}
		switch str(m, "type") {
		case "CREATED", "RELEASED", "RETURNED":
		default:
			return Event{}, fmt.Errorf("unsupported Ramp event")
		}
		data = object(m, "purchase")
	default:
		return Event{}, fmt.Errorf("unknown provider")
	}
	if data == nil {
		return Event{}, fmt.Errorf("missing order object")
	}
	snap, e := normalize(a.Name, data)
	if e != nil {
		return Event{}, e
	}
	// 部分网关没有唯一 delivery ID；用已验证的业务对象规范化哈希去重，JWT 换签也不会重复处理。
	raw, _ := json.Marshal(data)
	canon, e := canonical(raw)
	if e != nil {
		return Event{}, e
	}
	return Event{ID: digest(canon), Snapshot: snap}, nil
}
func normalize(name string, m map[string]any) (Snapshot, error) {
	s := Snapshot{Status: "pending"}
	amount := ""
	status := str(m, "status")
	switch name {
	case "moonpay":
		s.Ref = str(m, "id")
		s.ExternalID = str(m, "externalTransactionId")
		s.Wallet = str(m, "walletAddress")
		s.Fiat = str(object(m, "baseCurrency"), "code")
		s.Asset = str(object(m, "currency"), "code")
		s.Network = "ethereum"
		amount = str(m, "baseCurrencyAmount")
		s.CryptoAmount = str(m, "quoteCurrencyAmount")
		s.TxHash = str(m, "cryptoTransactionId")
		switch status {
		case "completed":
			s.Status = "completed"
		case "failed":
			s.Status = "failed"
		case "pending":
			s.Status = "processing"
		case "waitingPayment", "waitingAuthorization":
			s.Status = "pending"
		default:
			return s, fmt.Errorf("unknown MoonPay status")
		}
	case "alchemypay":
		s.Ref = str(m, "orderNo")
		s.ExternalID = str(m, "merchantOrderNo")
		s.Wallet = str(m, "address")
		s.Fiat = str(m, "fiat")
		s.Asset = str(m, "crypto")
		s.Network = str(m, "network")
		if s.Network == "ETH" {
			s.Network = "ethereum"
		}
		amount = str(m, "amount")
		s.CryptoAmount = str(m, "cryptoQuantity")
		s.TxHash = str(m, "txHash")
		switch status {
		case "FINISHED":
			s.Status = "completed"
		case "PAY_SUCCESS", "TRANSFER":
			s.Status = "processing"
		case "PENDING":
		case "PAY_FAIL":
			s.Status = "failed"
		case "CANCEL":
			s.Status = "cancelled"
		case "REFUNDED":
			s.Status = "refunded"
		case "RISK_CONTROL":
			s.Status = "review"
		default:
			return s, fmt.Errorf("unknown Alchemy Pay status")
		}
	case "transak":
		s.Ref = str(m, "id")
		if s.Ref == "" {
			s.Ref = str(m, "_id")
		}
		s.ExternalID = str(m, "partnerOrderId")
		s.Wallet = str(m, "walletAddress")
		s.Fiat = str(m, "fiatCurrency")
		s.Asset = str(m, "cryptoCurrency")
		s.Network = str(m, "network")
		amount = str(m, "fiatAmount")
		s.CryptoAmount = str(m, "cryptoAmount")
		s.TxHash = str(m, "transactionHash")
		switch status {
		case "COMPLETED":
			s.Status = "completed"
		case "FAILED":
			s.Status = "failed"
		case "CANCELLED", "EXPIRED":
			s.Status = "cancelled"
		case "REFUNDED":
			s.Status = "refunded"
		case "PROCESSING", "PENDING_DELIVERY_FROM_TRANSAK", "PAYMENT_DONE_MARKED_BY_USER":
			s.Status = "processing"
		case "AWAITING_PAYMENT_FROM_USER", "AWAITING_KYC", "PENDING", "CREATED":
		default:
			return s, fmt.Errorf("unknown Transak status")
		}
	case "ramp":
		s.Ref = str(m, "id")
		s.Wallet = str(m, "receiverAddress")
		s.Fiat = str(m, "fiatCurrency")
		s.Asset = str(object(m, "asset"), "symbol")
		s.Network = "ethereum"
		if s.Asset == "ETH_USDC" {
			s.Asset = "USDC"
		} else {
			s.Network = "unknown"
		}
		amount = str(m, "fiatValue")
		s.TxHash = str(m, "finalTxHash")
		s.ViewToken = str(m, "purchaseViewToken")
		units, ok := newBigInt(str(m, "cryptoAmount"))
		dec, e := strconv.Atoi(str(object(m, "asset"), "decimals"))
		if ok && e == nil && dec >= 0 && dec <= 36 {
			s.CryptoAmount = unitsToDecimal(units, dec)
		}
		switch status {
		case "RELEASED":
			s.Status = "completed"
		case "RETURNED":
			s.Status = "refunded"
		case "EXPIRED", "CANCELLED":
			s.Status = "cancelled"
		case "INITIALIZED", "PAYMENT_STARTED":
		case "PAYMENT_EXECUTED", "FIAT_SENT", "FIAT_RECEIVED", "RELEASING":
			s.Status = "processing"
		default:
			return s, fmt.Errorf("unknown Ramp status")
		}
	}
	n, e := decimalMinor(amount)
	if e != nil || s.Ref == "" || s.Wallet == "" || s.Fiat == "" || s.Asset == "" || s.Network == "" {
		return s, fmt.Errorf("provider monetary/binding fields missing")
	}
	s.FiatMinor = n
	s.Fiat = strings.ToUpper(s.Fiat)
	s.Asset = strings.ToUpper(s.Asset)
	if s.Status == "completed" {
		v, ok := positiveDecimal(s.CryptoAmount)
		if !ok || !v || s.TxHash == "" {
			return s, fmt.Errorf("delivery proof missing")
		}
	}
	return s, nil
}

func newBigInt(s string) (*big.Int, bool) {
	v, ok := new(big.Int).SetString(s, 10)
	return v, ok && v.Sign() >= 0
}
func unitsToDecimal(v *big.Int, n int) string {
	return new(big.Rat).SetFrac(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)).FloatString(n)
}
func positiveDecimal(s string) (bool, bool) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return false, false
	}
	return r.Sign() > 0, true
}
