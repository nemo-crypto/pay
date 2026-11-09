# Transak：Go Token → Session → Widget → JWT Webhook 全流程

本教程使用Partner API和托管Widget，不是Headless卡支付API。新版Create Widget URL必须由后端调用，不能只拼一个带apiKey的旧链接当作完整集成。

## 第1步：无需账号的可运行闭环

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/transak-demo.db go run ./cmd/server
```

打开 <http://127.0.0.1:8098/onramp> 选择Transak，依次创建、模拟KYC、模拟付款、模拟交付；或另开终端运行：

```sh
python3 scripts/onramp-demo.py --provider transak
python3 scripts/onramp-demo.py --provider transak --drop-webhook
```

mock自动提供API key、secret、Partner Access Token；模拟Create Widget URL API，再生成用HS256签名的JWT通知，服务端验证后完成订单。最终29 USDC只是假报价，不代表Transak费率。

## 第2步：申请Partner Dashboard并区分三种凭据

| 凭据 | 用途 | 注意 |
| --- | --- | --- |
| API key | 标识商户，session的x-api-key / widgetParams.apiKey | 按环境配置 |
| API secret | 获取/刷新Partner Access Token | 只保存在后端 |
| Partner Access Token | 查询/Session API；也是Webhook JWT验签密钥 | 不能误用API secret验证JWT |

在Partner Dashboard开通Staging，配置服务器IP白名单、referrerDomain、Webhook地址和订单事件订阅。使用相同环境的所有端点：

| 用途 | Staging | Production |
| --- | --- | --- |
| Partner API | `https://api-stg.transak.com` | `https://api.transak.com` |
| Session API | `https://api-gateway-stg.transak.com` | `https://api-gateway.transak.com` |
| Widget | `https://global-stg.transak.com` | `https://global.transak.com` |

## 第3步：配置Go与真实用户IP

```sh
export PAY_MODE=live
export ONRAMP_ENV=sandbox
export ONRAMP_PROVIDERS=transak
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export TRANSAK_API_KEY='your_staging_api_key'
export TRANSAK_API_SECRET='your_staging_api_secret'
go run ./cmd/server
```

Session API要求 `x-user-ip`。Go默认使用连接的RemoteAddr，不信任浏览器自报IP。若经过反向代理，需要设置 `ONRAMP_TRUSTED_PROXY_CIDRS` 为代理网段，并确保代理**覆盖** `X-Real-IP`。不要设置全网可信，也不要透传用户伪造的header。生产网络不能把127.0.0.1当作真实用户IP提交。

## 第4步：获取并缓存Partner Access Token

适配器首次需要时自动调用：

```sh
curl -fsS -X POST https://api-stg.transak.com/partners/api/v2/refresh-token \
  -H "x-api-key: $TRANSAK_API_KEY" \
  -H "api-secret: $TRANSAK_API_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"apiKey":"YOUR_STAGING_API_KEY"}'
```

返回：

```json
{"data":{"accessToken":"<sensitive JWT>","expiresAt":1729775825}}
```

expiresAt为Unix秒；官方说明有效期约7天。**不要每次下单刷新token**。代码使用互斥缓存，并持久化当前token、上一代token与到期时间到本地SQLite。重启读取缓存，临近到期刷新，旧token保留用于轮换期间的通知验签。数据库包含凭据，按敏感数据保护和备份；生产可使用KMS加密。

## 第5步：先保存本地订单，再创建一次性Widget URL

前端请求：

```http
POST /onramp/orders
Authorization: Bearer <app token>
Idempotency-Key: transak-purchase-001
Content-Type: application/json

{"provider":"transak","wallet_address":"0x1111111111111111111111111111111111111111","fiat_amount_minor":3000}
```

Go保存`on_...`订单，再调用：

```http
POST https://api-gateway-stg.transak.com/api/v2/auth/session
x-api-key: <partner API key>
access-token: <cached Partner Access Token>
x-user-ip: <validated original user IP>
Content-Type: application/json

{"widgetParams":{"apiKey":"<partner API key>","referrerDomain":"https://pay.example.com","fiatCurrency":"USD","fiatAmount":30,"cryptoCurrencyCode":"USDC","network":"ethereum","walletAddress":"0x...","disableWalletAddressForm":true,"partnerOrderId":"on_...","redirectURL":"https://pay.example.com/onramp?order_id=on_...","productsAvailed":"BUY"}}
```

返回 `{"data":{"widgetUrl":"https://global-stg.transak.com/?sessionId=..."}}`。代码校验URL必须属于当前环境的允许域名，防止异常响应诱导跳转。

**widgetUrl在5分钟后失效，每个sessionId只能使用一次，不能刷新/重新打开当作恢复支付。** 本项目本地同幂等键不会重新创建session；打开失败时先查询已有订单，确认没有付款或待处理订单后，再新建购买意图。请求创建超时且结果未知转review，避免重复发起。

## 第6步：用户KYC与SDK订单引用

前端打开widgetUrl，由Transak执行KYC、支付方式选择与付款。网页只保存自己的order_id，不保存Partner Access Token。

订单通知通过 `partnerOrderId` 回到本地订单。如果所用商户版本的通知没有回传这个字段，需要从Transak SDK订单事件取得供应商订单ID，再调用：

```http
POST /onramp/orders/{localOrderID}/bind
Authorization: Bearer <app token>
Content-Type: application/json

{"provider_order_id":"<Transak order ID>"}
```

后端将调用Get Order API验证 `partnerOrderId`、金额、钱包、币种和网络才绑定；SDK事件本身不是可信支付凭证。若商户查询响应也不提供partnerOrderId，不能弱化核对，应先与Transak确认可用关联字段并调整适配器。

## 第7步：验证Webhook JWT

Dashboard配置：

```text
https://pay.example.com/onramp/webhooks/transak
```

常见事件包括ORDER_CREATED、ORDER_PROCESSING、ORDER_COMPLETED、ORDER_FAILED、ORDER_REFUNDED、ORDER_EXPIRED。HTTP body通常是：

```json
{"data":"eyJhbGciOiJIUzI1NiIs..."}
```

用**Partner Access Token作为HS256密钥**验证data中的JWT，限制算法，不接受none或任意算法。验证后读取claims里的 `webhookData` 和 `eventID`。本项目检查JWT自带exp（若有），并按业务对象规范化哈希去重；不会凭外层明文状态更新订单。

```json
{"webhookData":{"id":"provider-order","partnerOrderId":"on_...","status":"COMPLETED","fiatCurrency":"USD","fiatAmount":30,"walletAddress":"0x...","cryptoCurrency":"USDC","network":"ethereum","cryptoAmount":29,"transactionHash":"0x..."},"eventID":"ORDER_COMPLETED"}
```

`COMPLETED`还要核对所有资产/金额/钱包字段、数量及哈希。`PENDING_DELIVERY_FROM_TRANSAK`只是processing；`AWAITING_KYC`/`AWAITING_PAYMENT_FROM_USER`继续等待。KYC事件不是订单完成事件，本实现仅处理订单对象。

## 第8步：GET Order补查与失败恢复

```sh
curl -fsS "https://api-stg.transak.com/partners/api/v2/order/PROVIDER_ORDER_ID" \
  -H "x-api-key: $TRANSAK_API_KEY" \
  -H "access-token: $TRANSAK_ACCESS_TOKEN"
```

这里的 `$TRANSAK_ACCESS_TOKEN` 是上一步接口取得的值，不是Go启动必填环境变量。代码自动管理token。需要供应商订单ID才能查询；如果第一次订单通知也丢失且SDK未绑定，不能凭本地ID冒充Transak订单ID。先用Dashboard/SDK找回，再由服务端验证绑定。

后台每30秒补查已绑定的pending/processing。mock预置确定的供应商引用，所以可完整演示“所有回调都丢失”恢复；真实模式仍需先取得该引用。

常见问题：401检查环境与token；Session失败检查IP白名单、x-user-ip、referrerDomain；回调JWT失败检查是否误用API secret、token轮换与缓存；未完成检查是否仍KYC/付款/链上交付中。禁止把页面显示“成功”当最终证据。

## 第9步：验收与上线

逐项验证token缓存、单次session、KYC、真实Staging付款、回调验签、SDK引用绑定、丢回调恢复、重复通知和越权404。将`ONRAMP_ENV`切production前，申请正式权限并更换Production凭据与白名单。本项目未执行真实Transak交易，不实现Headless支付或自动退款发起。

官方资料：

- https://docs.transak.com/api/public/refresh-access-token
- https://docs.transak.com/api/public/create-widget-url
- https://docs.transak.com/api/public/get-order-by-order-id
- https://docs.transak.com/guides/how-to-decrypt-webhook-payload
- https://docs.transak.com/features/webhooks
- https://docs.transak.com/customization/query-parameters
