package payment

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// WechatConfig 支持普通直连商户；不复用服务商/子商户协议。
type WechatConfig struct {
	MchID, Serial, PrivateKeyFile, APIv3Key string
	PlatformKeyID, PlatformKeyFile          string
	AppID, MiniAppID, MobileAppID           string
	TWDPerCNY                               string
}

func (c WechatConfig) AppFor(scene string) string {
	switch scene {
	case "mini":
		return c.MiniAppID
	case "app":
		return c.MobileAppID
	case "jsapi", "h5", "native":
		return c.AppID
	default:
		return ""
	}
}

func (c WechatConfig) Scenes() []string {
	out := []string{}
	for _, scene := range []string{"mini", "jsapi", "h5", "native", "app"} {
		if c.AppFor(scene) != "" {
			out = append(out, scene)
		}
	}
	return out
}

// WechatPayParams 由服务端签名；App SDK 的字段映射见微信教程。
type WechatPayParams struct {
	AppID     string `json:"appId"`
	Timestamp string `json:"timeStamp"`
	Nonce     string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
	PartnerID string `json:"partnerId,omitempty"`
	PrepayID  string `json:"prepayId,omitempty"`
}

type Wechat struct {
	Config             WechatConfig
	NotifyURL, BaseURL string
	HTTP               *http.Client
	private            *rsa.PrivateKey
	platform           map[string]*rsa.PublicKey
}

func NewWechat(c WechatConfig, publicURL string) (*Wechat, error) {
	if c.MchID == "" || c.Serial == "" || len(c.APIv3Key) != 32 || c.PlatformKeyID == "" || len(c.Scenes()) == 0 {
		return nil, fmt.Errorf("incomplete WeChat Pay configuration")
	}
	b, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("invalid WeChat merchant PEM")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("WeChat merchant key must be RSA >=2048 bits")
	}
	if err = key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid WeChat merchant RSA key")
	}
	b, err = os.ReadFile(c.PlatformKeyFile)
	if err != nil {
		return nil, err
	}
	block, _ = pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("invalid WeChat platform PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("WeChat platform key must be PKIX PUBLIC KEY PEM")
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < 2048 {
		return nil, fmt.Errorf("WeChat platform key must be RSA >=2048 bits")
	}
	return &Wechat{Config: c, NotifyURL: publicURL + "/webhooks/wechat", BaseURL: "https://api.mch.weixin.qq.com", HTTP: Client(), private: key, platform: map[string]*rsa.PublicKey{c.PlatformKeyID: pub}}, nil
}

func (w *Wechat) sign(message string) (string, error) {
	h := sha256.Sum256([]byte(message))
	b, err := rsa.SignPKCS1v15(rand.Reader, w.private, crypto.SHA256, h[:])
	return base64.StdEncoding.EncodeToString(b), err
}

func (w *Wechat) verifySignature(h http.Header, body []byte) error {
	ts, nonce := h.Get("Wechatpay-Timestamp"), h.Get("Wechatpay-Nonce")
	key := w.platform[h.Get("Wechatpay-Serial")]
	if key == nil || nonce == "" || !fresh(ts, time.Now()) {
		return ErrSignature
	}
	sig, err := base64.StdEncoding.DecodeString(h.Get("Wechatpay-Signature"))
	if err != nil {
		return ErrSignature
	}
	digest := sha256.Sum256([]byte(ts + "\n" + nonce + "\n" + string(body) + "\n"))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return ErrSignature
	}
	return nil
}

func (w *Wechat) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), ID("")
	sig, err := w.sign(method + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + string(body) + "\n")
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, method, w.BaseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, w.Config.MchID, nonce, ts, w.Config.Serial, sig))
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Wechatpay-Serial", w.Config.PlatformKeyID)
	resp, err := w.HTTP.Do(r)
	if err != nil {
		return nil, &Uncertain{err}
	}
	b, err := readResponse(resp)
	if err != nil {
		return nil, &Uncertain{err}
	}
	if err = w.verifySignature(resp.Header, b); err != nil {
		return nil, &Uncertain{err}
	}
	return b, nil
}

func (w *Wechat) payParams(o Order, prepay string) (*WechatPayParams, error) {
	ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), ID("")
	pkg, signed := "prepay_id="+prepay, "prepay_id="+prepay
	if o.WechatScene == "app" {
		pkg, signed = "Sign=WXPay", prepay
	}
	sig, err := w.sign(o.WechatAppID + "\n" + ts + "\n" + nonce + "\n" + signed + "\n")
	if err != nil {
		return nil, err
	}
	p := &WechatPayParams{AppID: o.WechatAppID, Timestamp: ts, Nonce: nonce, Package: pkg, SignType: "RSA", PaySign: sig}
	if o.WechatScene == "app" {
		p.PartnerID, p.PrepayID = w.Config.MchID, prepay
	}
	return p, nil
}

func (w *Wechat) Create(ctx context.Context, o Order) (Session, error) {
	if o.WechatAppID == "" || o.WechatAppID != w.Config.AppFor(o.WechatScene) || len(o.ID) > 32 || o.ID == "" || o.ProviderCurrency != "CNY" || o.ProviderAmount < 1 || o.ProviderAmount > 99999999 {
		return Session{}, fmt.Errorf("invalid WeChat order")
	}
	product := o.WechatScene
	if product == "mini" {
		product = "jsapi"
	}
	body := map[string]any{"appid": o.WechatAppID, "mchid": w.Config.MchID, "description": fmt.Sprintf("点数储值 %d 点", o.Coins), "out_trade_no": o.ID, "notify_url": w.NotifyURL, "time_expire": o.ExpiresAt.Format(time.RFC3339), "amount": map[string]any{"total": o.ProviderAmount, "currency": "CNY"}}
	if product == "jsapi" {
		if o.WechatOpenID == "" {
			return Session{}, fmt.Errorf("missing bound WeChat OpenID")
		}
		body["payer"] = map[string]string{"openid": o.WechatOpenID}
	}
	if product == "h5" {
		if net.ParseIP(o.ClientIP) == nil {
			return Session{}, fmt.Errorf("missing H5 client IP")
		}
		body["scene_info"] = map[string]any{"payer_client_ip": o.ClientIP, "h5_info": map[string]string{"type": "Wap"}}
	}
	b, _ := json.Marshal(body)
	b, err := w.request(ctx, "POST", "/v3/pay/transactions/"+product, b)
	if err != nil {
		return Session{}, err
	}
	var v struct {
		PrepayID string `json:"prepay_id"`
		H5URL    string `json:"h5_url"`
		CodeURL  string `json:"code_url"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Session{}, &Uncertain{err}
	}
	checkout := Checkout{Mode: o.WechatScene}
	expires := o.ExpiresAt
	if product == "h5" && time.Now().Add(5*time.Minute).Before(expires) {
		expires = time.Now().Add(5 * time.Minute)
	}
	checkout.ExpiresAt = &expires
	switch product {
	case "jsapi", "app":
		if v.PrepayID == "" {
			return Session{}, &Uncertain{fmt.Errorf("missing WeChat prepay ID")}
		}
		checkout.Wechat, err = w.payParams(o, v.PrepayID)
	case "h5":
		u, parseErr := url.Parse(v.H5URL)
		if parseErr != nil || u.Scheme != "https" || u.Host != "wx.tenpay.com" || u.User != nil || u.Fragment != "" {
			return Session{}, &Uncertain{fmt.Errorf("invalid WeChat H5 URL")}
		}
		q := u.Query()
		q.Set("redirect_url", o.ReturnURL)
		u.RawQuery = q.Encode()
		checkout.URL = u.String()
	case "native":
		u, parseErr := url.Parse(v.CodeURL)
		if parseErr != nil || u.Scheme != "weixin" || u.Host != "wxpay" || u.User != nil {
			return Session{}, &Uncertain{fmt.Errorf("invalid WeChat Native code URL")}
		}
		checkout.CodeURL = v.CodeURL
	default:
		return Session{}, fmt.Errorf("unsupported WeChat scene")
	}
	return Session{Ref: o.ID, Checkout: checkout}, err
}

type wechatTransaction struct {
	AppID         string `json:"appid"`
	MchID         string `json:"mchid"`
	OrderID       string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id"`
	TradeType     string `json:"trade_type"`
	TradeState    string `json:"trade_state"`
	Amount        struct {
		Total    int64  `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Payer struct {
		OpenID string `json:"openid"`
	} `json:"payer"`
}

func (w *Wechat) snapshot(b []byte) (Snapshot, error) {
	var v wechatTransaction
	if json.Unmarshal(b, &v) != nil || v.MchID != w.Config.MchID || v.OrderID == "" || len(v.OrderID) > 32 || v.Amount.Total < 1 || v.Amount.Currency != "CNY" {
		return Snapshot{}, fmt.Errorf("invalid WeChat transaction")
	}
	allowed := false
	for _, scene := range w.Config.Scenes() {
		allowed = allowed || w.Config.AppFor(scene) == v.AppID
	}
	if !allowed {
		return Snapshot{}, fmt.Errorf("unexpected WeChat app ID")
	}
	status, detail := "pending", strings.ToLower(v.TradeState)
	switch v.TradeState {
	case "SUCCESS":
		if v.TransactionID == "" {
			return Snapshot{}, fmt.Errorf("missing WeChat transaction ID")
		}
		status = "succeeded"
	case "NOTPAY", "USERPAYING":
	case "CLOSED", "REVOKED":
		status = "expired"
	case "PAYERROR":
		status = "failed"
	case "REFUND":
		status, detail = "flagged", "wechat_refund_requires_review"
	default:
		status, detail = "flagged", "unknown_wechat_state"
	}
	return Snapshot{Ref: v.OrderID, OrderID: v.OrderID, PaymentIntent: v.TransactionID, Status: status, Detail: detail, Currency: v.Amount.Currency, Amount: v.Amount.Total, WechatAppID: v.AppID, WechatOpenID: v.Payer.OpenID}, nil
}

func (w *Wechat) Fetch(ctx context.Context, o Order) (Snapshot, error) {
	b, err := w.request(ctx, "GET", "/v3/pay/transactions/out-trade-no/"+url.PathEscape(o.ID)+"?mchid="+url.QueryEscape(w.Config.MchID), nil)
	if err != nil {
		return Snapshot{}, err
	}
	return w.snapshot(b)
}

func (w *Wechat) Verify(r *http.Request, b []byte) (Event, error) {
	if err := w.verifySignature(r.Header, b); err != nil {
		return Event{}, err
	}
	var ev struct {
		ID           string `json:"id"`
		EventType    string `json:"event_type"`
		ResourceType string `json:"resource_type"`
		Resource     struct {
			Algorithm      string `json:"algorithm"`
			OriginalType   string `json:"original_type"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if json.Unmarshal(b, &ev) != nil || ev.ID == "" || ev.ResourceType != "encrypt-resource" || ev.Resource.Algorithm != "AEAD_AES_256_GCM" || ev.Resource.OriginalType != "transaction" {
		return Event{}, fmt.Errorf("invalid WeChat notification")
	}
	if ev.EventType != "TRANSACTION.SUCCESS" {
		return Event{}, fmt.Errorf("unsupported WeChat notification")
	}
	block, err := aes.NewCipher([]byte(w.Config.APIv3Key))
	if err != nil {
		return Event{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Event{}, err
	}
	if len(ev.Resource.Nonce) != gcm.NonceSize() {
		return Event{}, fmt.Errorf("invalid WeChat resource nonce")
	}
	data, err := base64.StdEncoding.DecodeString(ev.Resource.Ciphertext)
	if err != nil {
		return Event{}, err
	}
	plain, err := gcm.Open(nil, []byte(ev.Resource.Nonce), data, []byte(ev.Resource.AssociatedData))
	if err != nil {
		return Event{}, ErrSignature
	}
	snap, err := w.snapshot(plain)
	if err != nil || snap.Status != "succeeded" {
		return Event{}, fmt.Errorf("invalid WeChat success notification")
	}
	return Event{ID: ev.ID, Snapshot: snap}, nil
}
