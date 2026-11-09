# Allscale：Go稳定币储值从零到到账

这条流程让用户在Allscale Checkout用支持的稳定币付款，确认后增加平台点数。USDT/USDC和EVM网络由实际收银台决定；它不同于其他四家“法币买币到用户钱包”的示例。

## 第1步：不需要商户密钥的本地闭环

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/allscale-demo.db go run ./cmd/server
```

打开 <http://127.0.0.1:8098>，选择NT$1000、Allscale → 前往支付 → 模拟成功。应得到`succeeded`和2000点，记录模拟crypto信息。mock key=`local-demo-key`，secret=`local-demo-secret`，不会请求Allscale或执行链上转账。

再试“确认中”“少付/人工复核”和勾选“丢弃回调”。确认中不加点；少付需要复核；丢通知后后台约30秒查询补单。

## 第2步：准备Allscale商户与报价

开通Checkout产品，获取API key、API secret并配置Webhook。公共端点为`https://openapi.allscale.io`。官方当前集成说明中测试商户也使用该端点，**测试商户付款仍可能转移真实资金**，不能把它当成纯虚拟免费沙箱。

本项目储值套餐以TWD元计价，但官方公开币种枚举没有TWD。Go使用配置的固定TWD/USD汇率转为USD报价，`currency=1`代表USD，不传虚构的TWD枚举。

例如NT$1000、`ALLSCALE_TWD_PER_USD=32.00`，得到3125美分（31.25 USD）。使用有理数计算并向上取整到美分，每笔订单保存不可变报价快照。商户负责维护合理汇率。

## 第3步：配置Go真实适配器

```sh
export PAY_MODE=live
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export ALLSCALE_API_KEY='your_merchant_key'
export ALLSCALE_API_SECRET='your_merchant_secret'
export ALLSCALE_TWD_PER_USD=32.00
export ALLSCALE_SIGNED_RESPONSES=false
go run ./cmd/server
```

如果商户启用了响应签名能力，再设`ALLSCALE_SIGNED_RESPONSES=true`。不要拿未经确认的假key发送真实支付请求。公网HTTPS代理必须保留Webhook原始body、路径与query。

## 第4步：前端创建储值订单

```http
POST /topups
Authorization: Bearer <app token>
Idempotency-Key: allscale-purchase-001
Content-Type: application/json

{"plan_id":"demo-1000","gateway":"allscale","ui_mode":"hosted"}
```

后端保存本地订单和报价，再调用：

```http
POST https://openapi.allscale.io/v1/checkout_intents/
Content-Type: application/json
X-API-Key: <key>
X-Timestamp: <Unix seconds>
X-Nonce: <random nonce>
X-Signature: v1=<Base64 HMAC>

{"currency":1,"amount_cents":3125,"order_id":"top_...","order_description":"点数储值 2000 点","user_id":"alice","redirect_url":"https://pay.example.com/top-up/success?order_id=top_..."}
```

**末尾斜杠不可省略**。请求签名canonical string用换行依次连接：

```text
HTTP_METHOD
PATH
QUERY
TIMESTAMP
NONCE
SHA256_HEX(raw_body)
```

无query时保留空行。使用API secret做HMAC-SHA256，Base64后加`v1=`。时间戳是秒，与Alchemy Pay的毫秒不同。

返回`code=0`和payload中的`allscale_checkout_intent_id`、`checkout_url`。Go保存供应商引用并校验Checkout域名，前端打开URL。

## 第5步：用户选币和网络后付款

用户在实际Checkout选择支持的USDT/USDC及网络并按报价付款。本服务不硬编码收银台链列表。链上提交交易不等于确认成功；关闭页面也不撤销付款。

状态10表示链上确认中，20才是confirmed。回跳后查询订单，不能把客户端tx_hash当作入账依据。

## 第6步：回调签名与字段差异

注册地址：

```text
https://pay.example.com/webhooks/allscale
```

通知Header包含X-API-Key、X-Webhook-Id、X-Webhook-Timestamp、X-Webhook-Nonce、X-Webhook-Signature。验签canonical string为：

```text
allscale:webhook:v1
HTTP_METHOD
PATH
QUERY
WEBHOOK_ID
TIMESTAMP
NONCE
SHA256_HEX(raw_body)
```

HMAC-SHA256+Base64并带`v1=`，时间窗口±5分钟。body的webhook_id应与Header一致。创建结果字段是`allscale_checkout_intent_id`，**通知字段却是`all_scale_checkout_intent_id`**，不要漏掉中间下划线。

验签后先按已保存的Intent ID匹配，再核对order_id（如有）、amount_cents和currency。确认状态20或官方无status的成功通知，满足所有核对条件后持久化事件并入账。重复event/nonce与唯一ledger约束防止重复到账。

## 第7步：查询状态，补回丢失通知

```text
GET https://openapi.allscale.io/v1/checkout_intents/{intentID}
```

请求签名同前，GET空body仍需要对应SHA256。代码查询完整详情而非只查数字status，因为还必须核对金额和币种。

| 状态 | 含义与本地处理 |
| --- | --- |
| 1 / 2 / 3 | 等待处理中 |
| 4 | 供应商人工审核，继续等待 |
| 5 | 退回处理中，不能当成功 |
| 10 | 链上确认中，不加点 |
| 20 | 已确认，核对后加点 |
| -1 / -2 | 失败或KYT拒绝 |
| -3 | 少付，转flagged人工复核 |
| -4 / -5 | 取消或超时 |

官方通常只对成功发通知，负面状态必须依赖轮询。Worker每30秒查询未决订单，丢通知也能恢复。超过7天的未决情况需要运营核对，不应无限等待后台自动修复。

## 第8步：创建超时不能盲目重试

Allscale公共协议没有声明创建Intent幂等，order_id也不保证唯一。请求超时但网关可能已创建时，本项目标记provider_create_unknown/flagged，不自动创建第二个Intent。

管理员先在商户后台核实原Intent，再调用：

```http
POST /admin/orders/{localOrderID}/attach
Authorization: Bearer <admin token>
Content-Type: application/json

{"provider_ref":"<verified intent ID>"}
```

Go重新查询并匹配金额、币种、本地order_id才绑定和更新状态。不能输入任意tx_hash让系统加点。Allscale Claim Link是出款能力，不当作Checkout退款API；本项目不实现自动Allscale退款发起。

## 第9步：错误排查与验收

- 签名错误：检查尾斜杠、query空行、秒时间戳、body是否被重编码、secret是否正确。
- 接口失败：检查商户开通权限及请求code，不把非零code当成功。
- 状态20不到账：核对供应商引用、金额、币种、是否已flagged或重复入账。
- -3少付：人工处理，不自动按目标套餐全额加点。
- 未知建单：找回原引用，不换幂等键反复创建。

验证成功、确认中、失败、少付、重复回调、丢通知查询和人工绑定。真实商户小额联调尚待执行；网络/币种以Checkout为准。

官方参考：

- https://github.com/allscale-io/allscale-checkout-skill/blob/318bc701ad681f41ca0b96d4f59fad1729fae344/.claude/commands/integrate-allscale.md
- https://github.com/allscale-io/allscale-wordpress-plugin/tree/a661c1a1539697b175f1643782d8d15c5bf58cc2

代码入口：`internal/payment/allscale.go`、`internal/service/service.go`、`internal/store/store.go`。协议独立实现，未复制插件业务代码。
