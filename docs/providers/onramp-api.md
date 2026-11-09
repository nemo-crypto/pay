# 四家法币买币接入教程

本目录是可以运行、可以逐步验证的教学实现。四家通道都使用 Go 适配器、本地 mock API、真实算法签名、SQLite 持久订单与 Webhook 收件箱。默认无需商户账号；在本机完成闭环后，再按各家教程切换到商户 Sandbox。

| 通道 | 本项目采用的产品/API | 服务端凭据 | 完成判据 | 教程 |
| --- | --- | --- | --- | --- |
| MoonPay | On-ramp URL Widget + Buy Transaction API | public key、secret key、webhook key | `completed` + 交付数量/交易哈希 | [MoonPay](moonpay.md) |
| Alchemy Pay | Ramp Page + v4 Query Trade | appId、appSecret | `FINISHED`；`PAY_SUCCESS`不算交付 | [Alchemy Pay](alchemypay.md) |
| Transak | Partner API + Create Widget URL | API key、secret、自动缓存 access token | `COMPLETED` + 交付信息 | [Transak](transak.md) |
| Ramp | Hosted Widget + Host API Purchase | hostApiKey、官方网关公钥、purchaseViewToken | `RELEASED` + 交付信息 | [Ramp](ramp.md) |

## 1. 十分钟跑通四家

在项目根目录运行（Go 1.23+，建议1.25；命令行演示另需 Python3）：

```sh
HTTP_ADDR=127.0.0.1:8098 \
PUBLIC_URL=http://127.0.0.1:8098 \
DATABASE_PATH=data/onramp-demo.db \
go run ./cmd/server
```

打开 <http://127.0.0.1:8098/onramp>。选择供应商、保持30 USD和演示地址，依次点击「创建订单」「模拟 KYC 通过」「模拟支付完成」「模拟资产交付」。结果应为 `completed`、29 USDC，并显示模拟交易哈希。

29 USDC 来自本项目虚拟规则“30 USD减1 USD手续费”，绝不是任何一家真实报价。mock 地址和交易哈希不是链上资金证明。本地不会收集证件、卡号或私钥。

另一终端运行：

```sh
python3 scripts/onramp-demo.py
python3 scripts/onramp-demo.py --provider all --drop-webhook
```

第一条逐家验证幂等创建和成功流程；第二条丢弃全部回调，再由服务端主动查询 mock 网关完成订单。脚本首先检查 demo 模式，拒绝操作真实网关。

## 2. 本地统一 API

先用 `POST /demo/session` 获取演示 token。该接口只在 demo 存在；真实模式使用你的认证系统签发的 HS256 JWT（issuer、audience、密钥须一致）。`cmd/token` 仅用于受信任的本地联调。

| 方法 | 地址 | 参数 / 结果 |
| --- | --- | --- |
| GET | `/onramp/providers` | 可用通道、模式、USD金额范围、资产和网络 |
| POST | `/onramp/orders` | Bearer + Idempotency-Key；`provider,wallet_address,fiat_amount_minor` |
| GET | `/onramp/orders` | 当前用户最近50条订单 |
| GET | `/onramp/orders/{id}` | 当前用户订单及状态，不主动访问网关 |
| POST | `/onramp/orders/{id}/sync` | 后端主动查询供应商，不信任浏览器结果 |
| POST | `/onramp/orders/{id}/bind` | `provider_order_id,purchase_view_token`；查询核对后绑定 |
| POST | `/onramp/demo/{id}/advance` | `action=kyc/pay/deliver/fail/cancel`，可选`deliver_webhook=false` |
| POST | `/onramp/webhooks/{provider}` | 按各家官方算法验签，不使用 Bearer |

`fiat_amount_minor=3000` 表示30.00 USD，不是3000 USD。当前教学范围限定 USD 30–1000、USDC、Ethereum。这个限制是项目的产品范围，不代表供应商只支持这些国家/币种/网络；扩展前需要查询供应商能力并增加资产映射、精度和回归测试。

```sh
PAY_TOKEN=$(curl -fsS -X POST http://127.0.0.1:8098/demo/session | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
curl -fsS http://127.0.0.1:8098/onramp/orders \
  -H "Authorization: Bearer $PAY_TOKEN" \
  -H 'Idempotency-Key: my-onramp-purchase-001' \
  -H 'Content-Type: application/json' \
  -d '{"provider":"moonpay","wallet_address":"0x1111111111111111111111111111111111111111","fiat_amount_minor":3000}'
```

`checkout_url` 在 demo 指向本地页；live 指向服务商。相同用户和幂等键始终返回同一订单，同键改参数409。网络超时应使用原键，不应新造购买意图。建单网络结果未知转 `review`，不自动重建外部 Session。

## 3. 为什么不直接增加原来的点数余额

Stripe/Allscale 储值与本模块的买币交易不同：on-ramp 供应商把币交付到用户钱包，并不代表商户收到可支配资产。本模块记录买币成功但不修改 `users.coins` 或原有 ledger。若业务要“买币后充值商户余额”，还需要独立的用户转账/商户收款确认、链上归集与入账流程，不能单凭买币回调加点。

## 4. 状态、恢复和持久化

`pending → processing → completed`；另外有 `failed/cancelled/refunded/review`。已完成订单不会被迟到 pending/failed 事件覆盖；退款状态保留；字段不符转 review，不自动完成。completed 必须有数量和交易哈希，并匹配本地钱包、金额、法币、资产、网络和订单关联。

Webhook 通过签名校验后先写 `onramp_events`，返回成功，Worker 每30秒消费并更新订单。无唯一通知ID的供应商按已验证业务对象的规范化哈希去重。外部引用全局按 `(provider,ref)` 唯一绑定，不能一单多用。Worker 也查询最近7天的 pending/processing，回调丢失仍可恢复，但 Transak/Ramp 必须先知道供应商订单引用。

表：`onramp_orders`、`onramp_events`、`onramp_mock`、`onramp_tokens`。Transak 当前及上一代 token 写入受权限保护的 SQLite，重启可恢复验签；生产可进一步接入 KMS 加密存储。备份这些表时按凭据数据处理，不上传数据库。mock HTTP transport 不会向公网透传请求。

## 5. 真实商户模式共同步骤

1. 完成对应供应商商户准入，获得自己的 Sandbox/测试凭据；国家、KYC、资产、支付方式可用性由商户配置决定。
2. 配置公网 HTTPS、域名/IP白名单与回调。`PAY_MODE=live` 只表示调用供应商 API；再用 `ONRAMP_ENV=sandbox|production` 控制供应商环境。
3. `ONRAMP_PROVIDERS` 默认空，显式填写要启用的供应商，不会悄悄回退 mock。填齐对应凭据；`JWT_SECRET` 至少32字节。
4. 原有 Stripe/Allscale 可以保持未启用，或按原教程独立配置。服务端不要将 secret、access token、purchaseViewToken 发到前端或打印日志。
5. 完成小额商户测试：身份验证、付款失败、浏览器关闭、回调重复、丢回调和状态核对。最终报价由供应商收银台负责，前端输入不是交付证明。

## 6. 当前实现边界与上线验收

本地四家流程已经可以完整运行；真实适配器根据官方文档实现，但未用商户凭据做公网联调。当前没有通用退款发起、自动解冻review或自动转换网络功能。KYC 在供应商托管页面完成，不在本服务伪造通过状态；demo 的 KYC 按钮只更新 mock 场景。

必须核验商户测试实际返回的状态、资产代码及 webhook 字段。未知状态或字段缺失默认拒绝完成。订单进入review后，应先查供应商后台、API和链上结果，再做有审计的人工处置；不能改状态让它“看起来成功”。

浏览器回归 `npm run test:onramp`，后端回归 `go test -race ./internal/onramp`。截图和结果见项目验证记录。

管理员可通过 `GET /admin/onramp/reconciliation` 检查未处理事件、review订单、缺少供应商引用和超过7天未完成的订单数量。
