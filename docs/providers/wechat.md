# 微信支付：小程序、微信网页、H5、Native 和 App

本模块是**普通直连商户 API v3**，增加平台点数，和 Stripe/Allscale 共用储值订单、幂等键、inbox、对账和事务账本。服务商、子商户、合单、付款码和跨境商户是其他产品协议，不通过切换本模块的场景参数启用。

## 1. 先选择场景

| 实际入口 | `wechat_scene` | 下单 API | 用户侧调用 | 前提 |
| --- | --- | --- | --- | --- |
| 微信小程序 | `mini` | `/v3/pay/transactions/jsapi` | `wx.requestPayment` | 小程序 AppID 与商户绑定；小程序登录取得该 AppID 的 OpenID |
| 微信内公众号网页 | `jsapi` | `/v3/pay/transactions/jsapi` | `WeixinJSBridge.invoke` | 对应公众号 AppID、网页授权 OpenID、支付授权目录 |
| 微信外手机浏览器 | `h5` | `/v3/pay/transactions/h5` | 浏览器导航到 `h5_url` | 已开通 H5，配置 H5 支付域名、真实用户 IP、正确 Referer |
| 桌面网站 | `native` | `/v3/pay/transactions/native` | 把 `code_url` 生成为二维码，手机微信扫码 | 开通 Native；二维码按当前订单展示，浏览器轮询状态 |
| 原生 Android/iOS App | `app` | `/v3/pay/transactions/app` | 微信 OpenSDK | 开放平台移动应用 AppID、商户绑定、平台签名/Universal Links 配置 |

浏览器 UA 只用于推荐选项，不用来认证用户。普通浏览器不能执行小程序 API 或原生 SDK；小程序中的网页 WebView 也不能直接当成小程序支付页面，需要返回小程序页面调起。微信内网页不要用 H5 绕过 JSAPI 身份绑定。

小程序 OpenID、公众号 OpenID 按 AppID 隔离，不能互换，也不能用 UnionID 代替。AppID 与商户关联、产品权限、支持的设备环境均须在微信商户平台核对。

小程序虚拟商品/游戏、虚拟币或点数，以及 iOS 内数字内容，还涉及不同的产品准入和客户端规则。若业务应使用 `wx.requestVirtualPayment`、小游戏虚拟支付或 Apple IAP，需要另接相应协议；本模块 `mini` 仅代表普通商户 JSAPI 下单，不表示所有点数充值都能通过该通道上线。项目的点数仅作演示，请按实际售卖内容与终端核对产品适用性。

## 2. 本地运行五场景演示

```sh
HTTP_ADDR=127.0.0.1:8098 \
PUBLIC_URL=http://127.0.0.1:8098 \
DATABASE_PATH=data/pay-demo.db \
go run ./cmd/server
```

打开 `http://127.0.0.1:8098/?gateway=wechat#topup`，依次选择五个场景并模拟成功。每笔 NT$1000 对应2000点；默认演示报价为 `1 CNY = 4.50 TWD`，向上取整后扣款快照为22223分（¥222.23）。这是教学固定报价，不是市场汇率。

demo 不调用微信服务器，不打开真实微信收银台；它返回模拟付款参数并跳到本地收银台。模拟通知使用临时 RSA 密钥签名、AES-256-GCM 加密，然后走正式验签/解密/入账代码。模拟二维码链接及 prepay_id 无法用于真实付款。重启后 RSA 演示密钥更新，已持久化订单及已验签 inbox 继续保留。

## 3. 配置真实普通商户

启动前导出环境变量；程序不会自动加载 `.env`：

```sh
export PAY_MODE=live
export PUBLIC_URL=https://pay.example.com
export JWT_SECRET='<至少32字节随机密钥>'
export WECHAT_MCH_ID='<商户号>'
export WECHAT_MERCHANT_SERIAL='<商户API证书序列号>'
export WECHAT_PRIVATE_KEY_FILE=/run/secrets/apiclient_key.pem
export WECHAT_API_V3_KEY='<32字节APIv3密钥>'
export WECHAT_PLATFORM_KEY_ID='PUB_KEY_ID_...'
export WECHAT_PLATFORM_PUBLIC_KEY_FILE=/run/secrets/wechatpay_public_key.pem
export WECHAT_APP_ID='<公众号/H5/Native绑定的AppID>'
export WECHAT_MINI_APP_ID='<小程序AppID>'
export WECHAT_MOBILE_APP_ID='<移动App的AppID>'
export WECHAT_TWD_PER_CNY=4.50
```

只配置需要的 AppID。例如仅小程序时可不设置 `WECHAT_APP_ID` 和 `WECHAT_MOBILE_APP_ID`，接口只列出 `mini`。AppID 存在不等于已开通产品权限；微信商户平台仍须开通对应支付产品。

本实现采用**微信支付平台公钥模式**：公钥和 ID 从可信商户平台取得，PEM 类型为 `PUBLIC KEY`。商户私钥支持 PKCS#8 或 PKCS#1，RSA 至少2048位。不要把商户证书序列号当成平台公钥 ID，不要从未验签通知里下载或信任新公钥。当前启动配置固定一个平台公钥，未实现自动下载平台证书或无停机多公钥轮换；轮换前需要安排配置更新与重启。

普通商户该模块只收 CNY；现有套餐仍以 TWD 表达业务储值额度。服务端使用 `WECHAT_TWD_PER_CNY` 计算并冻结人民币**分**金额，不能将 `nt_amount` 原样作为人民币发送。币种和人民币分金额出现在创建结果的 `payment_currency`、`payment_amount_minor`；每日额度仍按 TWD 计算。需要原生人民币套餐时，应另行定义业务定价规则，不应偷偷把现有套餐改币种。

## 4. 身份绑定在后端完成

小程序业务登录后端用 `wx.login` 的短期 `code` 调用微信 `jscode2session`；公众号网页使用 OAuth code 在服务端换取 OpenID。AppSecret、session_key、OAuth token 不进入浏览器支付请求，也不写入付款参数。

本项目复用已有 JWT 登录契约，**不提供公共的任意 OpenID 绑定接口**。登录服务确认身份后，在签名 JWT 中加入：

```json
{
  "sub": "business-user-id",
  "wechat_openids": {
    "wx_your_mini_app": "openid_returned_by_jscode2session",
    "wx_your_official_account": "openid_returned_by_oauth"
  }
}
```

同时保留现有 issuer、audience、有效期和签名校验。服务端按当前场景 AppID 取身份；缺少匹配项返回403 `WECHAT_IDENTITY_REQUIRED`。请求 JSON 不接受 `openid`、`app_id` 或自报用户 IP。切换 AppID/身份会改变幂等请求指纹。

受信任终端联调可用：

```sh
go run ./cmd/token -user business-user-id \
  -wechat-app-id wx_your_mini_app \
  -wechat-openid '<仅填写微信登录后端已确认的OpenID>'
```

该命令是持有 JWT 密钥的管理员工具，不是面向用户的登录接口。真实 OAuth 回跳、code2session、用户绑定存储、域名白名单及业务登录会话由原应用接入。

## 5. 统一创建订单

```sh
curl -fsS https://pay.example.com/topups \
  -H "Authorization: Bearer $PAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: wechat-mini-purchase-001' \
  -d '{"gateway":"wechat","wechat_scene":"mini","plan_id":"demo-1000"}'
```

其他场景只替换 `wechat_scene`，同一购买意图重试必须保留原键。服务端生成不超过32字符的商户单号，在发出请求前保存；不同场景/身份不可复用同一幂等键。`ui_mode=embedded` 不适用于微信。

回调地址固定为 `PUBLIC_URL + /webhooks/wechat`，由服务端发送，不接受前端指定。JSAPI/小程序/App 返回 `checkout.wechat`，Native 返回 `checkout.code_url`，H5 返回 `checkout.url`。下单成功不等于付款成功。

### 小程序

```js
const order = await createTopup({ gateway: 'wechat', wechat_scene: 'mini', plan_id: 'demo-1000' });
const p = order.checkout.wechat;
wx.requestPayment({
  timeStamp: p.timeStamp,
  nonceStr: p.nonceStr,
  package: p.package,
  signType: p.signType,
  paySign: p.paySign,
  complete() { pollOrderFromBackend(order.order_id); }
});
```

`createTopup`、`pollOrderFromBackend` 代表业务前端封装；调用后端必须带原应用登录令牌。小程序 AppID 必须与返回 `appId` 一致，配置服务器 request 合法域名。小程序 SDK 的客户端成功回调也只触发查单。

### 微信内网页 JSAPI

```js
const p = order.checkout.wechat;
// 用户点击付款时调用；若 bridge 尚未加载，等待 WeixinJSBridgeReady。
WeixinJSBridge.invoke('getBrandWCPayRequest', p, () => {
  pollOrderFromBackend(order.order_id);
});
```

本演示页提供调起按钮；微信桥尚未就绪时可在就绪后再次点击。已有公众号页面可以用已注册的 JS-SDK `chooseWXPay`，但其 `timestamp` 参数名与本接口的 `timeStamp` 不同，需要映射，不能直接盲传。

### H5 外部手机浏览器

真实用户 IP 从连接地址取得；经过反向代理时，显式设置 `WECHAT_TRUSTED_PROXY_CIDRS`，例如 `127.0.0.0/8,10.20.0.0/16`。只有连接来自这些网段时读取代理覆盖的 `X-Real-IP`，不直接信任任意 `X-Forwarded-For`。配置可信代理后缺少有效 `X-Real-IP` 会拒绝下单。

H5 需要正常浏览器顶层导航和商户白名单域名 Referer。演示页用带 `referrerPolicy=origin` 的链接打开微信返回的 HTTPS `wx.tenpay.com` 地址；不会使用 fetch/iframe 代替收银台。服务端把已校验的 `return_url` 作为 `redirect_url` 编码进链接。回跳后查询订单，不能据回跳认定付款成功。

`h5_url` 有效期短，接口 `checkout.expires_at` 最多保留5分钟，订单本身30分钟。过期付款链接不会在创建重放、详情或恢复接口重新返回。订单未完成时先补查/核对，不因链接过期自动新建另一个扣款意图；当前没有自动重建 H5 链接或关单 API。

### PC Native 扫码

`code_url` 是 `weixin://wxpay/...` 内容，不是可直接打开的 HTTPS 收银台。业务前端应使用本地二维码组件编码这个完整字符串，例如已安装 `qrcode` 的前端中：

```js
import QRCode from 'qrcode';
await QRCode.toCanvas(canvas, order.checkout.code_url, { width: 256 });
pollOrderFromBackend(order.order_id);
```

不要将付款链接提交到外部在线二维码图片服务。本仓库演示页使用内置 node-qrcode 浏览器模块在本地 canvas 渲染二维码，并提供扫码内容复制；demo 跳本地模拟收银台。二维码过期应停止展示，页面刷新/关闭不代表微信关单。

### 原生 App

`checkout.wechat` 映射到 OpenSDK 的 PayReq，而不是当成小程序参数：

| 本接口 | OpenSDK PayReq |
| --- | --- |
| `appId` | Android `appId`；iOS 由注册应用确定 |
| `partnerId` | `partnerId` |
| `prepayId` | `prepayId` |
| `package` | Android `packageValue` / iOS `package`，值 `Sign=WXPay` |
| `nonceStr` | `nonceStr` |
| `timeStamp` | Android `timeStamp` / iOS `timeStamp`（按 SDK 转整数） |
| `paySign` | `sign` |

App 支付签名原文的第四行是 **prepay_id 本身**；JSAPI/小程序第四行是 **prepay_id=xxx**。App 不需要 payer OpenID。SDK 返回后从后端查询订单；不要把 SDK errCode=0 直接写入账本。原生工程、SDK 安装与移动应用平台配置在调用方完成。

## 6. 签名、通知和对账

下单请求使用商户私钥 RSA-SHA256：

```text
HTTP方法\n
包含查询串的请求路径\n
时间戳（秒）\n
随机串\n
原始请求body\n
```

Authorization 使用 `WECHATPAY2-SHA256-RSA2048`，携带商户号、商户证书序列号、时间戳、随机串和 Base64 签名。成功下单与查单响应必须先按 `Wechatpay-Serial` 选择固定可信平台公钥，并核验原始响应 body 签名；不能只验证 JSON 内容。

通知先验证平台签名和5分钟时间窗，再用32字节 APIv3 密钥对 `resource.ciphertext` 做 AES-256-GCM 解密。GCM 的 nonce、associated_data 必须原样使用，不能和 HTTP 签名 nonce 混淆。只接受有效的 `TRANSACTION.SUCCESS` 交易成功通知。验签解密后还校验商户号、AppID、商户单号、CNY原始订单总额及需要时的 OpenID；优惠后的 payer_total 不代替 amount.total。

通知持久化成功返回HTTP204；重复 event ID 持久化去重，事务保证只增加一次点数。入账在后续 worker 中执行，不能因为先应答通知就假定业务已经到账。验签或入库失败返回非2xx，让微信重试。

漏通知时用商户单号主动查询：

```text
GET /v3/pay/transactions/out-trade-no/{商户单号}?mchid={商户号}
```

同样验证响应签名并核对身份/金额。下单超时后保留原单号，详情 `provider_create_unknown`；自动对账只查询，不重新创建扣款订单。不存在/异常查询会继续保留待确认，不会凭网络错误释放额度或认定未付款。管理员应核对原商户单号，确认微信侧最终结果后再处理。

| 微信状态 | 本地行为 |
| --- | --- |
| SUCCESS | 身份和金额核对后，事务入账一次 |
| NOTPAY / USERPAYING | 保留pending，继续查询 |
| CLOSED / REVOKED | expired，后到的合法成功仍按既有规则核对 |
| PAYERROR | failed |
| REFUND | 人工复核；不将“进入退款”误判成全额退款完成 |
| 未知状态 / 金额、身份不符 | 人工复核，不自动增加点数 |

退款申请、退款成功通知、部分退款账本、主动关单和服务商模式未在本模块实现。不要将 `TRANSACTION.SUCCESS` 的金额与退款金额混用。已成功订单不会被晚到的 pending/closed 通知覆盖。

## 7. 联调与故障定位

先确认已开通所选产品和商户/AppID绑定；再检查 OpenID 来自正确 AppID、HTTPS notify_url 无登录拦截、H5域名及Referer、小程序请求域名、公众号支付授权目录、移动SDK配置。签名错误需分别检查商户私钥/证书序列号、平台公钥/ID、时间同步、原始body以及每行末尾的换行。

```sh
GOCACHE=/tmp/chovy-pay-go-cache go test -race ./internal/payment ./internal/httpapi -run Wechat
```

本地测试覆盖五种下单协议、客户端签名、响应验签、通知解密/篡改、金额/币种/AppID/OpenID不符、重复事件、丢通知恢复、下单超时和通知先于下单返回。不等同于真实微信支付验收；本环境无法访问官方站点和商户API，部署前需用商户真实配置核对最新协议，并按五类终端分别完成小额支付及查单验收。

官方参考：[微信支付商户文档](https://pay.weixin.qq.com/doc/v3/merchant/)、[小程序 wx.requestPayment](https://developers.weixin.qq.com/miniprogram/dev/api/payment/wx.requestPayment.html)、[微信开放平台](https://open.weixin.qq.com/)。

[返回支付教程总览](README.md) · [接入与运维](../integration.md)
