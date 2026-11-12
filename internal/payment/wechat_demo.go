package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func DemoWechatConfig() WechatConfig {
	return WechatConfig{MchID: "demo-wechat-merchant", Serial: "DEMO_MERCHANT_SERIAL", APIv3Key: "0123456789abcdef0123456789abcdef", PlatformKeyID: "PUB_KEY_ID_DEMO", AppID: "wx_demo_public", MiniAppID: "wx_demo_mini", MobileAppID: "wx_demo_app", TWDPerCNY: "4.50"}
}

// NewDemoWechat 使用临时 RSA 密钥走真实签名/解密协议，永不请求微信服务器。
func NewDemoWechat(db *sql.DB, publicURL string) (*Demo, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	c := DemoWechatConfig()
	w := &Wechat{Config: c, private: key, platform: map[string]*rsa.PublicKey{c.PlatformKeyID: &key.PublicKey}}
	d := &Demo{Gateway: "wechat", PublicURL: publicURL, DB: db, Verifier: w}
	return d, d.Init()
}

func (d *Demo) WechatNotification(o Order, s Snapshot) (*http.Request, []byte, error) {
	w, ok := d.Verifier.(*Wechat)
	if !ok || d.Gateway != "wechat" || s.Status != "succeeded" {
		return nil, nil, fmt.Errorf("invalid demo WeChat notification")
	}
	v := wechatTransaction{AppID: o.WechatAppID, MchID: w.Config.MchID, OrderID: o.ID, TransactionID: s.PaymentIntent, TradeState: "SUCCESS"}
	v.Amount.Total, v.Amount.Currency, v.Payer.OpenID = s.Amount, s.Currency, o.WechatOpenID
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher([]byte(w.Config.APIv3Key))
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce, aad := ID("")[:12], "transaction"
	data := base64.StdEncoding.EncodeToString(gcm.Seal(nil, []byte(nonce), plain, []byte(aad)))
	b, err := json.Marshal(map[string]any{"id": ID("evt_wx_"), "event_type": "TRANSACTION.SUCCESS", "resource_type": "encrypt-resource", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "original_type": "transaction", "ciphertext": data, "nonce": nonce, "associated_data": aad}})
	if err != nil {
		return nil, nil, err
	}
	ts, sigNonce := strconv.FormatInt(time.Now().Unix(), 10), ID("")
	sig, err := w.sign(ts + "\n" + sigNonce + "\n" + string(b) + "\n")
	if err != nil {
		return nil, nil, err
	}
	r, err := http.NewRequest("POST", d.PublicURL+"/webhooks/wechat", strings.NewReader(string(b)))
	if err != nil {
		return nil, nil, err
	}
	r.Header.Set("Wechatpay-Timestamp", ts)
	r.Header.Set("Wechatpay-Nonce", sigNonce)
	r.Header.Set("Wechatpay-Serial", w.Config.PlatformKeyID)
	r.Header.Set("Wechatpay-Signature", sig)
	return r, b, nil
}
