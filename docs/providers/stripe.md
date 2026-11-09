# Stripe：Go信用卡储值从零到到账

这条流程使用Stripe Checkout收取信用卡付款，付款核对成功后给平台账户增加点数。默认方案NT$1000→2000点，金额与点数由后端配置，客户端不能自行指定到账数量。

## 第1步：无需Stripe账号，跑本地mock

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/stripe-demo.db go run ./cmd/server
```

打开 <http://127.0.0.1:8098>，选择NT$1000、Stripe，点击前往支付和模拟付款成功。应看到`succeeded`、2000点到账和历史记录。默认`whsec_local_demo_only`只是公开mock签名密钥；真实Stripe不会接受它。

可勾选“模拟回调丢失”，付款后等待约30秒后台查询恢复。模拟失败/过期应不加点。返回首页、刷新、重启都不重复加点。

## 第2步：准备Stripe测试凭据

登录Stripe Dashboard，使用测试模式的secret key `sk_test_...`；创建Webhook端点后取得`whsec_...`。如果使用Stripe CLI，CLI转发给出的Webhook secret与Dashboard端点secret不同，必须使用实际发送方对应的那个。

本项目REST API版本固定`2024-06-20`，Webhook端点版本也应对齐。托管模式不需要把secret放进前端；嵌入式模式需自己的publishable key与Stripe.js。

## 第3步：启动真实网关联调模式

```sh
export PAY_MODE=live
export HTTP_ADDR=127.0.0.1:8098
export PUBLIC_URL=https://pay.example.com
export CHECKOUT_ALLOWED_ORIGINS=https://pay.example.com
export JWT_SECRET='replace-with-at-least-32-random-bytes'
export STRIPE_SECRET_KEY='sk_test_replace_me'
export STRIPE_WEBHOOK_SECRET='whsec_replace_me'
export STRIPE_LIVE=false
go run ./cmd/server
```

使用HTTPS反向代理指向8098。开发时也可由Stripe CLI把事件转发给本机：

```sh
stripe listen --forward-to http://127.0.0.1:8098/webhooks/stripe
```

使用CLI输出的signing secret重新启动Go。程序live模式仍要求HTTPS PUBLIC_URL，因为它会生成给真实收银台的回跳地址。`go run ./cmd/token -user alice`可在受信任终端生成联调token，真实应用应连接自己的登录系统。

## 第4步：前端向Go创建储值订单

```http
POST /topups
Authorization: Bearer <token>
Idempotency-Key: stripe-purchase-001
Content-Type: application/json

{"plan_id":"demo-1000","gateway":"stripe","ui_mode":"hosted"}
```

Go根据套餐取得TWD元和点数，先保存订单、幂等键、每日额度预留，再调用Stripe。NT$1000转换为100000最小货币单位，不能把1000当作Stripe的分。

等价API示意：

```sh
curl -fsS https://api.stripe.com/v1/checkout/sessions \
  -u "$STRIPE_SECRET_KEY:" \
  -H 'Stripe-Version: 2024-06-20' \
  -H 'Idempotency-Key: checkout-LOCAL_ORDER_ID' \
  -d mode=payment \
  -d 'payment_method_types[0]=card' \
  -d 'line_items[0][quantity]=1' \
  -d 'line_items[0][price_data][currency]=twd' \
  -d 'line_items[0][price_data][unit_amount]=100000' \
  -d 'line_items[0][price_data][product_data][name]=Top-up 2000 coins' \
  -d 'metadata[order_id]=LOCAL_ORDER_ID' \
  --data-urlencode 'success_url=https://pay.example.com/top-up/success?order_id=LOCAL_ORDER_ID' \
  --data-urlencode 'cancel_url=https://pay.example.com/top-up/cancel?order_id=LOCAL_ORDER_ID'
```

这个示例说明协议；正常使用由Go创建并持久化，不要绕过本地订单直接生成无法关联的真实Session。实现还设置固定expires_at和payment_intent metadata，重试保持相同请求参数及`checkout-{orderID}`幂等键。

## 第5步：用户完成Checkout

Go返回`order_id`和`checkout.url`，前端跳转。测试模式可按Stripe官方测试指南使用测试卡，例如4242测试卡，不填真实卡号。付款回跳后仅查询`GET /topups/{id}`与`GET /me`，不能直接加点。

embedded模式后端返回client_secret；需要在已有前端接Stripe.js和publishable key并调整CSP。本项目自带网页采用hosted模式。

## 第6步：接收并验证Webhook

注册完整端点：

```text
https://pay.example.com/webhooks/stripe
```

订阅checkout.session.completed、checkout.session.async_payment_succeeded、checkout.session.async_payment_failed、checkout.session.expired、charge.refunded。

读取Stripe-Signature中的`t`和一个或多个`v1`，用webhook secret对`timestamp + "." + 原始body`计算HMAC-SHA256，hex比较，并检查±5分钟时间窗口。必须保留原始body，不先重新序列化。

付款确认要求`payment_status=paid`，并匹配本地Session、金额、币种和订单ID。`checkout.session.completed`事件名称本身不等于款项已支付。验签通过后持久化inbox，返回200，由Worker原子更新订单、唯一credit账本和余额。

## 第7步：查询、恢复支付和退款

```text
GET  /topups/{id}              本地订单
GET  /topups/pending           可付款订单，空结果204
POST /topups/{id}/resume       查询Stripe后返回可恢复的Checkout
POST /topups/{id}/cancel-hint  隐藏提示，不撤销真实付款
GET  /me/ledger                点数账本
```

回调丢失时后台查询`GET /v1/checkout/sessions/{id}`。建单结果未知，使用相同订单和幂等键恢复，不生成新收费意图；Session到期前30分钟停止自动恢复未知创建并转复核。

全额退款可以从Stripe Dashboard发起，`charge.refunded`验签后冲回点数，重复通知只冲回一次。部分退款转人工复核；本项目没有退款发起API，也没有消费扣点业务。

## 第8步：错误排查与验收

- 401：应用Bearer不合法，检查issuer/audience/exp，不是把Stripe key当登录token。
- 签名400：CLI和Dashboard secret混用、代理改body、服务器时钟错误。
- 不到账：检查payment_status、币种金额及metadata，不凭回跳成功补点。
- 幂等409：同一键用了不同参数，新的购买意图才用新键。
- 退款后余额不符：检查退款事件和ledger，不直接覆盖余额。

验收成功、失败、到期、关闭浏览器、重复回调、丢回调、恢复订单、全额退款与越权访问。正式上线用生产key并设STRIPE_LIVE=true，Webhook也切生产secret；本仓库未执行真实扣款。

官方资料：https://docs.stripe.com/checkout/fulfillment 、https://docs.stripe.com/webhooks/signature 、https://docs.stripe.com/testing 。代码入口：`internal/payment/stripe.go`、`internal/service/service.go`、`internal/store/store.go`。
