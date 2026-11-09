# Ramp Network：Go Hosted Widget + ECDSA Webhook + Purchase查询

本教程使用Ramp Network法币买币，不是泛指所有“ramp”产品。用户法币支付后USDC交付到其Ethereum钱包；不增加平台点数余额。

## 第1步：无需账号，先跑mock闭环

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/ramp-demo.db go run ./cmd/server
```

打开 <http://127.0.0.1:8098/onramp>，点Ramp → 创建订单 → 模拟KYC通过 → 模拟支付完成 → 模拟资产交付。也可在另一终端运行：

```sh
python3 scripts/onramp-demo.py --provider ramp
python3 scripts/onramp-demo.py --provider ramp --drop-webhook
```

第二条会丢弃通知，通过模拟Host API补查订单。最终completed、29 USDC和模拟tx_hash。手续费1 USD是本地虚拟值，不代表Ramp报价。mock用公开可重现的secp256k1测试私钥签名，适配器只用对应公钥验证；真实模式绝不使用该测试密钥。

## 第2步：申请hostApiKey并区分key的用途

通过Ramp商户接入获取hostApiKey，配置允许域名与所需资产/地区。hostApiKey主要是集成标识，不是Webhook HMAC secret。

| 数据 | 作用 | 来源 |
| --- | --- | --- |
| hostApiKey | Widget和部分Host API识别集成 | Ramp商户后台/接入团队 |
| Ramp签名公钥 | 验证回调ECDSA签名 | 官方Webhooks文档，测试/生产不同 |
| purchase.id | 真正的供应商购买订单ID | SDK事件或已验证Webhook |
| purchaseViewToken | 查询某笔purchase的只读凭据 | SDK事件/购买对象，后端保管 |

**Ramp私钥属于Ramp，商户不应拥有或请求它。** 你需要的是验证公钥；不要给Ramp写一个自定义HMAC验签当作真实协议。

| 环境 | Widget | API |
| --- | --- | --- |
| 测试 | `https://app.demo.rampnetwork.com` | `https://api.demo.rampnetwork.com/api` |
| 生产 | `https://app.rampnetwork.com` | `https://api.rampnetwork.com/api` |

## 第3步：配置Go服务

```sh
export PAY_MODE=live
export ONRAMP_ENV=sandbox
export ONRAMP_PROVIDERS=ramp
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export RAMP_HOST_API_KEY='your_ramp_test_host_api_key'
go run ./cmd/server
```

通过本机受信任终端 `go run ./cmd/token -user alice` 生成联调token，在网页连接账户。密钥必须来自自己的商户，生产不能使用mock值。真正测试时使用自己的有效钱包，并先查商户允许的资产。

## 第4步：查询资产能力，生成托管页

```sh
curl -fsS "https://api.demo.rampnetwork.com/api/host-api/assets?hostApiKey=$RAMP_HOST_API_KEY"
```

Widget资产参数使用ETH_USDC；Host API资产对象可能返回短代码USDC，必须同时核对Ethereum USDC合约地址`0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48`和精度6。本项目适配这两种表示。检查资产是否可用、精度、法币范围和限额。本项目示例限定Ethereum USDC，不承诺所有账户都开通。Ramp支持更多资产与产品，扩展需增加资产映射和相应测试。

本地请求：

```http
POST /onramp/orders
Authorization: Bearer <app token>
Idempotency-Key: ramp-purchase-001
Content-Type: application/json

{"provider":"ramp","wallet_address":"0x1111111111111111111111111111111111111111","fiat_amount_minor":3000}
```

后端保存订单，再构建参数：

```text
hostApiKey=<key>
hostAppName=Pay Lab
swapAsset=ETH_USDC
fiatCurrency=USD
fiatValue=30.00
userAddress=0x...
enabledFlows=ONRAMP
webhookStatusUrl=https://pay.example.com/onramp/webhooks/ramp?order_id=on_...&binding=...
finalUrl=https://pay.example.com/onramp?order_id=on_...
```

所有参数由Go `url.Values`编码，再拼到Widget地址。`fiatValue`通常是用户可见预设，并非商户固定不可修改金额；若最终金额/资产变更，本项目转review，不冒充原意图已完成。

## 第5步：打开Widget，保存SDK购买事件

用户在Ramp完成身份验证、支付方式选择及法币支付。前端可以直接打开托管页，也可以在自己的应用用Ramp SDK。

若使用SDK，购买创建事件会给出purchase对象；只把 `purchase.id` 和 `purchaseViewToken` 发到自己的Go服务：

```http
POST /onramp/orders/{localOrderID}/bind
Authorization: Bearer <app token>
Content-Type: application/json

{"provider_order_id":"<purchase.id>","purchase_view_token":"<purchase.purchaseViewToken>"}
```

后端用这些值查询Ramp真实订单，核对钱包、金额、资产与网络后绑定。**浏览器能伪造SDK事件，因此必须再查询。** 供应商引用在数据库唯一绑定，不能让同一purchase关联多个本地订单。view token不会从本地订单GET接口返回。

托管页面模式没有SDK事件时，由验签Webhook取得同样信息。如果第一次通知完全丢失且没有SDK引用，需要到供应商侧找回；不能用本地`on_...`充当purchase ID。

## 第6步：Webhook是ECDSA，不是HMAC

Ramp发送：

```http
POST /onramp/webhooks/ramp?order_id=on_...&binding=...
X-Body-Signature: <Base64 DER signature>
Content-Type: application/json

{"type":"RELEASED","purchase":{"id":"...","status":"RELEASED","fiatCurrency":"USD","fiatValue":30,"receiverAddress":"0x...","asset":{"symbol":"USDC","decimals":6,"address":"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"},"cryptoAmount":"29000000","finalTxHash":"0x...","purchaseViewToken":"..."}}
```

验证步骤：

1. 对JSON对象递归按key稳定排序并紧凑序列化，不包括Webhook URL中的自定义query。
2. SHA256计算摘要。
3. 读取X-Body-Signature，Base64解码，解析DER ECDSA签名。
4. 使用对应环境的Ramp **secp256k1**公钥验证，不能用P-256替代。
5. 再核对本地订单关联token、供应商引用与钱包/金额/资产。
6. 写入收件箱，Worker更新订单。

Go标准库对secp256k1的直接支持有限，本项目使用 `decred/dcrd/dcrec/secp256k1/v4`。JSON规范化使用RFC8785实现，覆盖排序、数字与转义；测试验证重排/缩进不影响合法签名。官方测试/生产公钥在 `internal/onramp/protocol.go`，应跟随官方轮换更新。

`order_id`和`binding`只用于关联，**不是付款证明**；query不在Ramp签名覆盖范围，所以仍须校验完整purchase，且全局唯一绑定其id。这里没有因收到回调就增加平台余额。

## 第7步：处理精度与状态

Ramp的 `cryptoAmount` 是最小单位整数字符串。例如USDC精度6：`29000000 / 10^6 = 29.000000 USDC`，不能把它当成2900万USDC。使用大整数/有理数，不用浮点做余额计算。

| Ramp purchase状态 | 本地状态 |
| --- | --- |
| INITIALIZED / PAYMENT_STARTED | pending |
| PAYMENT_EXECUTED / FIAT_SENT / FIAT_RECEIVED / RELEASING | processing |
| RELEASED | completed，需哈希与数量完整 |
| RETURNED | refunded |
| EXPIRED / CANCELLED | cancelled |

Webhook外层type可能为CREATED/RELEASED/RETURNED；业务判断使用purchase内容，不只看type或页面成功提示。mock的“失败”采用EXPIRED场景，因此本地显示取消/过期。

## 第8步：使用purchaseViewToken查询

```sh
curl -fsS "https://api.demo.rampnetwork.com/api/host-api/purchase/PURCHASE_ID?secret=PURCHASE_VIEW_TOKEN"
```

这里的secret是**该笔purchaseViewToken**，不是hostApiKey。查询URL可能出现在代理日志里，需对secret参数脱敏。代码只记录摘要错误，不打印完整URL或token。

已绑定订单每30秒由Worker补查，也可调用本地 `/onramp/orders/{id}/sync`。mock会预置供应商引用与view token，因此全部回调丢失仍能演示恢复；真实模式需要第5或第6步先取得引用。

## 第9步：故障排查与上线验收

- 验签失败：检查测试/生产公钥、secp256k1、DER格式、Base64、稳定JSON，勿签URL query。
- 查询失败：确认purchase ID与view token属于同一订单，不能用hostApiKey替代。
- 金额显示差100万倍：cryptoAmount未按asset.decimals换算。
- RELEASED但review：钱包/金额/USDC/Ethereum与本地意图不一致，人工核对。
- 第一个回调丢失无法查询：需要SDK或供应商后台找回引用，不存在根据本地ID直接猜测订单的捷径。

Sandbox验收成功交付、失败/过期、重复/乱序通知、签名篡改、丢回调、重复绑定拒绝与用户越权。生产前确认商户资质、资产与地区可用性，切换key和`ONRAMP_ENV=production`。本项目未执行真实Ramp交易，不实现off-ramp或退款发起API。

官方资料：

- https://docs.rampnetwork.com/configuration
- https://docs.rampnetwork.com/webhooks
- https://docs.rampnetwork.com/rest-api-reference
- https://docs.rampnetwork.com/testing-environment
- https://docs.rampnetwork.com/events
