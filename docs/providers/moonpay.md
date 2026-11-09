# MoonPay：从零完成 Go On-ramp 接入

目标：让用户用法币购买 USDC，交付到 Ethereum 钱包；后端跟踪订单并验证 MoonPay 通知。这里使用 URL Widget，不是另一个 OAuth Platform 产品。

## 第1步：不用账号，先跑完本地闭环

在 `pay` 根目录启动服务：

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/moonpay-demo.db go run ./cmd/server
```

打开 `http://127.0.0.1:8098/onramp`，点 MoonPay → 创建订单 → 模拟 KYC 通过 → 模拟支付完成 → 模拟资产交付。或另开终端：

```sh
python3 scripts/onramp-demo.py --provider moonpay
python3 scripts/onramp-demo.py --provider moonpay --drop-webhook
```

预期最终 `status=completed`，`crypto_amount=29`，交易哈希有值；原点数余额不变。再次使用相同幂等键创建不会增加新订单。勾选“丢弃通知”完成交付后，订单仍未完成，点击“向网关重新查询”或等待约30秒恢复。

内置 mock 使用 `mock-public-moonpay`、`mock-secret-moonpay`、`mock-webhook-moonpay`。这些只是公开的本地测试值，不能放到真实 MoonPay 服务。

## 第2步：认识三把 key

| 凭据 | 来源/用途 | 放哪里 |
| --- | --- | --- |
| Publishable key：`pk_test_...` / `pk_live_...` | Widget 参数与部分交易查询 | Go环境变量；可出现在Widget URL |
| Secret key：`sk_test_...` / `sk_live_...` | URL签名，部分服务端API认证 | 仅Go后端 |
| Webhook API key | 验证 `Moonpay-Signature-V2` | 仅Go后端，不能用普通secret替代 |

到 MoonPay Dashboard 的 Developers → API keys 获取测试凭据，配置应用域名；到 Webhooks 添加通知地址。真实模式使用的密钥环境必须一致。Sandbox Widget为 `https://buy-sandbox.moonpay.com`，生产为 `https://buy.moonpay.com`；API 都是 `https://api.moonpay.com`，由key决定数据环境。

## 第3步：配置 Go 服务

下面命令中的值需要替换为自己的 Sandbox 配置；先由反向代理提供 HTTPS。

```sh
export PAY_MODE=live
export ONRAMP_ENV=sandbox
export ONRAMP_PROVIDERS=moonpay
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export MOONPAY_PUBLIC_KEY='pk_test_replace_me'
export MOONPAY_SECRET_KEY='sk_test_replace_me'
export MOONPAY_WEBHOOK_KEY='replace_with_dashboard_webhook_key'
go run ./cmd/server
```

在受信任终端 `go run ./cmd/token -user alice` 生成联调token，粘贴到演示页“联调 Bearer Token”；真实应用接自己的登录系统。实际购买要填写自己的有效钱包地址，不用mock占位地址。

## 第4步：创建你的本地购买意图

前端调用统一接口，而不是自己拼签名：

```http
POST /onramp/orders
Authorization: Bearer <your token>
Idempotency-Key: moonpay-purchase-001
Content-Type: application/json

{"provider":"moonpay","wallet_address":"0x1111111111111111111111111111111111111111","fiat_amount_minor":3000}
```

Go先保存订单 `on_...`，然后生成：

```text
https://buy-sandbox.moonpay.com?
apiKey=pk_test_...&
baseCurrencyAmount=30.00&baseCurrencyCode=usd&currencyCode=usdc&
externalTransactionId=on_...&walletAddress=0x...&
redirectURL=https%3A%2F%2Fpay.example.com%2Fonramp%3Forder_id%3Don_...&
lockAmount=true&signature=...
```

上面换行仅为阅读，实际是一条URL。`usdc` 对应此示例的Ethereum资产；上线前通过 MoonPay Currencies API 确认商户可用代码。`baseCurrencyAmount`与手续费是否包含的关系需按最终收银台核对；本地意图不是供应商最终扣款承诺。

实现：`internal/onramp/provider.go` 的 `Adapter.Create`。签名精确步骤：先对所有参数URL编码并形成带开头 `?` 的query；用secret做HMAC-SHA256；Base64编码；对签名URL编码；**最后追加**`&signature=`，不要把signature参与原签名。

```go
query := "?" + params.Encode()
signature := base64.StdEncoding.EncodeToString(hmacSHA256(secret, query))
widgetURL := widgetBase + query + "&signature=" + url.QueryEscape(signature)
```

项目实现使用 `b64mac`，固定测试向量由独立Python计算验证，不依赖客户端提供金额或签名。

## 第5步：用户在托管收银台完成 KYC 和支付

前端打开返回的 `checkout_url`。用户在MoonPay完成身份验证、支付方式选择、3DS或银行验证。不要采集真实卡号/证件到本示例服务。用户关闭页面不代表支付取消，回跳也不代表成功。

监听回跳后仅使用 `order_id` 查询自己的订单；服务端只认验签通知或经过认证的供应商查询。

## 第6步：配置并验证 Webhook

Dashboard配置完整地址：

```text
https://pay.example.com/onramp/webhooks/moonpay
```

订阅 `transaction_created`、`transaction_updated`、`transaction_failed`。通知的业务对象在 `data` 中；用 `data.externalTransactionId` 找本地订单。

请求头示例：

```text
Moonpay-Signature-V2: t=1700000000,s=<hex hmac>
```

验签消息为 `timestamp + "." + 原始JSON请求体`，密钥是Webhook API key，算法HMAC-SHA256，结果按hex比较。本项目增加±5分钟时间窗口；服务器需要同步时间。不要先反序列化再重新序列化后验签MoonPay，因为空格/顺序变化会影响原始字节。

状态：`waitingPayment`/`waitingAuthorization`保持pending，`pending`映射processing，`failed`映射failed，只有`completed`且数量/交易哈希完整才完成。回调写入收件箱后返回200，Worker事务处理；顺序不保证，因此完成后不接受旧pending覆盖。

## 第7步：回调丢失时查询

```sh
curl -fsS 'https://api.moonpay.com/v1/transactions/ext/LOCAL_ORDER_ID?apiKey=YOUR_PUBLISHABLE_KEY'
```

这是按 `externalTransactionId` 查询，**返回数组，不保证外部ID唯一**。本项目只接受恰好一条；零条继续等待，多条保持未决并要求人工核实，不任取第一条。会核对金额、USDC、钱包和外部订单ID，再更新状态。

部分其他API（如列出交易）要求 `Authorization: Api-Key <secret>`；本例的external-ID查询用publishable key。不要把两套认证混用。

## 第8步：故障排查与验收

- Widget报签名错：确认签的是编码后的query、含`?`、signature最后追加，secret与public key同环境。
- 回调400：检查Webhook key、时间同步、代理是否修改body，以及通知type是否已实现。
- 本地review：钱包/金额/币种不一致。先核对Dashboard及接口，不手工加点。
- 交易长期processing：KYC/3DS/银行或链上交付还未完成；保留订单，等待查询。
- 回调丢失：用本地 `/onramp/orders/{id}/sync` 验证恢复；不得把回跳参数当成功。

上线验收：真实Sandbox跑成功/失败/重复回调/丢回调/跨用户访问；确认供应商资产、费用、地区限制；再替换live keys和`ONRAMP_ENV=production`。本项目没有执行真实MoonPay交易，也没有实现自动退款发起。

## 官方资料与代码入口

- https://dev.moonpay.com/widget/on-ramp/customization/url-signing
- https://dev.moonpay.com/api-reference/widget/using-the-api
- https://dev.moonpay.com/api-reference/widget/getbuytransactionbyexternalid
- https://dev.moonpay.com/api-reference/widget/webhooks/signature
- https://dev.moonpay.com/widget/sandbox-testing
- `internal/onramp/provider.go`：创建URL、查询；`protocol.go`：验签/映射；`mock.go`：模拟供应商；`engine.go`：持久化和恢复。
