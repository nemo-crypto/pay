# Alchemy Pay：Go Ramp Page + 查询 + Webhook 全流程

本教程选择“Page Integration托管买币页面”，不是Native卡支付、虚拟卡或Crypto Payment产品。不同产品的签名、端点和订单字段不可混用。本示例USD买Ethereum USDC，资金交付到用户钱包。

## 第1步：先跑不用密钥的本地版本

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/alchemypay-demo.db go run ./cmd/server
```

打开 <http://127.0.0.1:8098/onramp>，选Alchemy Pay → 创建订单 → 模拟KYC通过 → 模拟支付完成 → 模拟资产交付。也可新终端执行：

```sh
python3 scripts/onramp-demo.py --provider alchemypay
python3 scripts/onramp-demo.py --provider alchemypay --drop-webhook
```

完成时显示29 USDC、模拟tx_hash和completed。手续费1 USD只是假数据。drop-webhook版本通过API补查完成，验证系统不依赖浏览器回跳。本地mock凭据是 `mock-public-alchemypay` / `mock-secret-alchemypay`；请求签名、回调签名和业务状态仍按真实协议处理。

## 第2步：准备商户与测试环境

向Alchemy Pay申请Ramp商户准入，取得appId/appSecret，开通所需法币、USDC与ETH网络，配置商户的出站IP白名单及允许域名。官方文档要求上线前提供服务器出口IP；机器换IP时也要同步更新。

| 环境 | 托管页 | API |
| --- | --- | --- |
| 测试 | `https://ramptest.alchemypay.org` | `https://openapi-test.alchemypay.org` |
| 生产 | `https://ramp.alchemypay.org` | `https://openapi.alchemypay.org` |

商户appSecret只保存在Go后端，不能发到浏览器。本项目不接收真实卡号或KYC材料，身份验证由托管页处理。商户是否开放测试网络、支付方式及模拟支付，由Alchemy Pay确认，不从mock示例推断。

## 第3步：配置服务端

```sh
export PAY_MODE=live
export ONRAMP_ENV=sandbox
export ONRAMP_PROVIDERS=alchemypay
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export ALCHEMYPAY_APP_ID='your_sandbox_app_id'
export ALCHEMYPAY_APP_SECRET='your_sandbox_app_secret'
go run ./cmd/server
```

通过受信任终端 `go run ./cmd/token -user alice` 获得联调token；进入网页连接账户。不要把实际secret发到聊天或提交Git。生产反向代理保留 `/onramp/webhooks/alchemypay` 路径与原始请求内容。

## 第4步：创建本地订单并签收银台URL

```http
POST /onramp/orders
Authorization: Bearer <token>
Idempotency-Key: alchemy-purchase-001
Content-Type: application/json

{"provider":"alchemypay","wallet_address":"0x1111111111111111111111111111111111111111","fiat_amount_minor":3000}
```

Go保存本地订单，再形成以下页面参数：

| 参数 | 本项目传值 |
| --- | --- |
| appId / timestamp | 商户ID / 13位毫秒时间戳 |
| merchantOrderNo | 本地 `on_...` 订单ID |
| fiat / fiatAmount | USD / 30.00 |
| crypto / network | USDC / ETH |
| address | 用户接收钱包 |
| showTable | buy |
| callbackUrl | 带本地关联参数的Go Webhook地址 |
| redirectUrl | `/onramp?order_id=...` |
| sign | 后端计算的Base64 HMAC结果 |

注意：**浏览器URL路径和签名虚拟路径不同**。虽然打开的是 `https://ramptest.alchemypay.org?...`，签名使用 `/index/rampPageBuy`。

```text
timestamp + GET + /index/rampPageBuy? + 排序后的非空参数
```

参数按名称字典序排序，排除sign和空值。官方示例签名使用参数原值（如callbackUrl里的`https://`），然后用appSecret做HMAC-SHA256、Base64，最后对整个URL各参数编码。不要在签名之前重复编码callbackUrl。

实现见 `Adapter.Create` 和 `alchemyQuery`。独立固定签名向量覆盖排序与HMAC；如果商户所用文档版本要求不同编码，先用官方签名样例对齐再切生产。

## 第5步：KYC、付款与交付

前端打开 `checkout_url`。用户在Alchemy Pay页面选择可用支付方式并完成KYC/支付。本地网页回跳只查询订单，不给平台加点。

特别注意：`PAY_SUCCESS` 只表示法币付款成功，尚未完成币交付；`TRANSFER` 表示转账处理中；只有 `FINISHED` 才对应本地completed，还要有cryptoQuantity和txHash并通过字段核对。

| Alchemy状态 | 本地状态 | 操作 |
| --- | --- | --- |
| PENDING | pending | 等待付款 |
| PAY_SUCCESS / TRANSFER | processing | 继续查询，不能当交付完成 |
| FINISHED | completed | 核对钱包/币种/金额/网络/哈希 |
| PAY_FAIL | failed | 展示失败，不重用成功标记 |
| CANCEL | cancelled | 用户重新购买需新意图 |
| RISK_CONTROL | review | 人工复核 |
| REFUNDED | refunded | 记录退回，不再次标记完成 |

## 第6步：回调验签，别用旧signature字段

通知发送到后端传入的callbackUrl，路由：

```text
POST https://pay.example.com/onramp/webhooks/alchemypay?order_id=...&binding=...
timestamp: <13位毫秒时间戳>
```

body含 `appId,merchantOrderNo,orderNo,status,amount,fiat,crypto,network,address` 等字段，以及 `newSignature`。本项目验证newSignature，不用旧的signature代替。

1. 获取Header中的timestamp，要求在±5分钟内。
2. 从body排除空值、`signature`和`newSignature`；其余字段排序为紧凑JSON。
3. 拼接 `timestamp + "POST" + "/onramp/webhooks/alchemypay" + 排序JSON`。这里的请求路径不包括域名和query。
4. 用appSecret做HMAC-SHA256并Base64，常量时间比较。
5. appId必须属于本商户，merchantOrderNo必须属于本地订单。
6. 写入事件收件箱，随后事务更新订单，重复事件不重复生效。

金额如 `"30.00000000"` 使用有理数转换为3000美分，不用float64直接乘100。completed还要求交付数量和哈希完整。金额、钱包或网络不一致转review。

## 第7步：主动查询恢复

真实查询端点：

```text
GET https://openapi-test.alchemypay.org/open/api/v4/merchant/query/trade?merchantOrderNo=on_...&side=BUY
appid: <appId>
timestamp: <毫秒>
sign: <Base64 HMAC>
```

GET签名内容为：

```text
timestamp + GET + /open/api/v4/merchant/query/trade?merchantOrderNo=on_...&side=BUY
```

参数同样排序。本项目支持官方样例中的直接订单对象和`data`封装，使用商户查询结果更新订单。不要误用文档某些简写示例中的 `/merchant/query/trade` 而漏掉 `/open/api/v4`。

在本地调用 `POST /onramp/orders/{id}/sync` 就会执行这条查询；后台每30秒对未完成订单查询。回调丢失演示可以验证此链路。

## 第8步：常见错误与上线验收

- 签名错误：检查毫秒时间戳、方法大写、虚拟页面签名路径、非空排序、原值与URL编码顺序。
- 查询403/连接失败：检查出口IP白名单、商户开通权限及测试/生产域名配对。
- PAY_SUCCESS但页面未完成：这是正常中间态，等待TRANSFER/FINISHED。
- FINISHED但本地不完成：检查txHash、cryptoQuantity，以及USDC/ETH、金额和钱包是否一致。
- review不会自动清除：先核对商户后台和查询结果，做有审计的人工处置，不能直接补余额。

验收清单：签名正反例、PAY_SUCCESS不提前完成、FINISHED完成、失败/取消、重复/乱序通知、丢通知查询、跨用户查询失败。真实商户联调尚未执行；自动退款发起和原生信用卡API不在此示例范围。

## 官方资料

- https://alchemypay.readme.io/docs/page-integration-2
- https://alchemypay.readme.io/docs/on-ramp-custom-parameters
- https://alchemypay.readme.io/docs/ramp-signature-description
- https://alchemypay.readme.io/docs/api-sign
- https://alchemypay.readme.io/docs/query-order-2
- https://alchemypay.readme.io/docs/webhook-signature
- https://alchemypay.readme.io/docs/list-of-order-status
