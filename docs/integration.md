# 接入与运维

## 与参考前端的对应关系

本项目依据参考前端的 `/topup/plans`、`/topups`、pending/resume/cancel-hint、订单轮询及 `/me.coins` 契约设计。`nt_amount` 是整数 TWD 元，不是分；Stripe 转为分，Allscale 转为 USD 美分。状态包括 pending/succeeded/failed/expired/flagged/refunded。默认返回地址为 `/top-up/success?order_id=...` 与 `/top-up/cancel?order_id=...`。

前端 API base URL 指向 Go 服务，跨域时将前端完整 origin 加入 `CHECKOUT_ALLOWED_ORIGINS`，包括协议和端口，不加末尾斜杠。JWT 的密钥、issuer、audience 必须与真实认证系统一致；若原系统使用 RS256/JWKS，应替换 auth 适配器，不要直接接受未验证的旧 token。

`source`、`live_room` 可随订单记录；`tracking_event_id` 接受但不实现分析事件投递。`first_topup_discount=false`，促销点数为0；首储标记来自首次实际入账。原应用的首储折扣、卡片管理、推广分成、直播归因结算、消费扣点均不在此实现内。套餐 ID 通过 `PLANS_FILE` 配置为原前端需要的值，文件为 `[{"id":"plan-a","nt_amount":1000,"coin_amount":2000}]`。

演示网页使用 hosted 模式。Stripe embedded 后端会返回 `client_secret`，已有前端需自行加载 Stripe.js、使用自己的 publishable key 挂载 Checkout，并配置其 CSP。Allscale 仅 hosted；恢复接口当前仅恢复 Stripe（与参考契约一致），Allscale 通过订单查询确认结果。不要在 Allscale 尚未查明结果时诱导用户重复付款。

## Stripe 联调

1. 设置 `PAY_MODE=live`、HTTPS `PUBLIC_URL`、随机 `JWT_SECRET`、`STRIPE_SECRET_KEY=sk_test_...`、`STRIPE_LIVE=false`。
2. 在 Stripe 注册 `/webhooks/stripe`，设置对应 `STRIPE_WEBHOOK_SECRET`，或使用 Stripe CLI 转发至本机。应用 PUBLIC_URL 的 HTTPS 要求可通过本机 TLS 反向代理满足。
3. 注册 `checkout.session.completed`、`checkout.session.async_payment_succeeded`、`checkout.session.async_payment_failed`、`checkout.session.expired`、`charge.refunded`。端点事件版本对齐 `2024-06-20`。
4. 真实联调验证 Checkout、回跳、回调、重复通知与后台补单。生产密钥需要 `STRIPE_LIVE=true`，必须使用对应生产端点的签名 secret。
5. 全额退款在 Stripe Dashboard 发起，回调冲回点数；部分退款冻结账户待人工复核。项目没有退款发起 API。退款通知先于成功通知时会留在 inbox，待 payment_intent 绑定后继续处理。

建单先落库再访问网关，Stripe 使用固定 `checkout-{orderID}` 幂等键。建单结果未知后，最多每分钟恢复一次原请求；达到 Session 到期前30分钟的边界停止自动创建，转人工复核，避免到期参数失效或幂等缓存过期后重复创建。

## Allscale 联调

设置 `ALLSCALE_API_KEY`、`ALLSCALE_API_SECRET`、`ALLSCALE_TWD_PER_USD`，启用 `PAY_MODE=live`。回调注册为完整 HTTPS `/webhooks/allscale`。API 地址固定 `https://openapi.allscale.io`。官方当前说明中测试商户也使用此端点，测试商户付款仍可能转移真实资金，不能当作免费沙箱。

`POST /v1/checkout_intents/` 尾斜杠不可省略；currency 是枚举1（USD）。创建结果字段是 `allscale_checkout_intent_id`，通知字段则是 `all_scale_checkout_intent_id`。请求 HMAC 签名 METHOD/PATH/QUERY/TIMESTAMP/NONCE/BODY_HASH；回调另含 `allscale:webhook:v1` 与 webhook ID。验签基于原始请求体，时间窗口5分钟，服务器需同步时钟。

启用 `ALLSCALE_SIGNED_RESPONSES=true` 时也会验证网关响应签名；需确认商户已启用该能力。反向代理必须保留请求体、请求路径及 query，不能改写带签名的 webhook URL。

只有状态20确认后入账；10为链上确认中，4为人工处理，5为资金退回中，-3少付需要复核。公开协议通常只通知成功，失败与超时依赖主动查询。币种、金额和网关引用必须与订单快照匹配。

Allscale 没有公开的建单幂等保证，`order_id` 并不保证唯一。请求超时、响应丢失等结果未知时，本系统标记复核，不自动重建。管理员先在商户后台找回真实 intent ID，再调用：

```text
POST /admin/orders/{order_id}/attach
Authorization: Bearer <admin token>
Content-Type: application/json

{"provider_ref":"商户后台核实的 intent ID"}
```

服务端重新查询该 intent 并核对 order_id、金额和币种；只有匹配后才绑定并更新状态。不能提交任意交易哈希让后台加点。其他 flagged 订单没有通用自动解冻接口，需要审核证据、制定有审计记录的补偿操作；禁止直接修改余额。Allscale Claim Link 是出款产品，不当作 Checkout 退款接口。

## 数据、任务与监控

数据库包含 users、orders、requests（幂等键）、ledger、inbox、demo_payments。网络调用在事务外，余额、账本、订单与 inbox 完成状态在同一事务内提交。重复事件通过唯一约束去重，同 nonce 不同事件被拒绝。金额不匹配不入账，并记录 payment_mismatch。

内置 Worker 每30秒运行，先消费最多100条到期 inbox，再查询最多50条待对账订单。自动轮询覆盖最近7天的 pending/failed/expired 订单，迟到成功可在未退款、未复核且未入账时补入一次。未知引用通知保留并重试；订单绑定后自动恢复。超过7天的未决订单及长期未知通知需要运营检查，不应无限依赖自动修复。

`/healthz` 只检查数据库；管理员查看 `/admin/reconciliation` 统计并监控持续增长的未处理事件、未知建单和账本差异。`trace_id` 当前只是响应标识，不提供完整分布式追踪。日志不输出凭据或完整支付请求体。

限流为单进程按用户每分钟读180次、写10次；部署层另配 webhook/IP 限流，避免阻断合法回调重试。SQLite 使用 WAL、外键与 busy timeout；不要将同一 SQLite 文件通过网络盘提供给多个实例。备份使用 SQLite 一致性备份工具，或停止服务后复制数据库及相关 WAL 文件；仅在线复制主 `.db` 可能丢数据。

## 部署

`make build` 后直接运行二进制，网页已内嵌。设置 `PAY_MODE=live` 和 HTTPS 公网地址，进程监听内部地址，TLS 在反向代理终止。数据库目录必须持久化且只允许服务用户访问。Dockerfile 提供非 root 运行的构建示例，`/data` 必须挂载持久卷；镜像默认 live，需要传完整环境变量。Docker 构建未作为真实部署验收。

上线前在各商户环境完成低额端到端验收、丢回调恢复和退款验证，再决定开放业务流量。当前测试与演示没有连接真实支付网关或发起真实扣款。

## 六家教程入口

统一总览与启动步骤见根目录README；Stripe、Allscale、MoonPay、Alchemy Pay、Transak、Ramp各自逐步教程见 [教程目录](providers/README.md)。四家买币接口与储值接口共用Go服务，但买币交付不直接修改平台点数。

## 买币事件重试与旧数据库升级

启动时自动为 `onramp_events` 增加 `attempts`、`next_attempt`、`last_error`、`dead_letter`，并建立待处理索引。迁移在事务中执行，重复启动安全，原有事件保留；升级前仍应按上述方式备份 SQLite。

每次 Worker 最多读取100个到期事件。临时失败从30秒开始指数退避，封顶1小时；永久关联冲突、缺失订单或损坏数据进入隔离队列。管理员统计中的 `unprocessed_events` 包括延迟和隔离项，`deferred_events` 仅统计等待重试项，`dead_letter_events` 统计隔离项。已确认的付款不会因建单网络超时被回退为未知状态。

排查时可只读查看事件元信息，不应输出完整通知、钱包或供应商凭据：

```sql
SELECT provider, event_id, order_id, attempts, next_attempt, last_error
FROM onramp_events
WHERE processed = 0 AND dead_letter = 1
ORDER BY created LIMIT 50;
```

先核对供应商查询与本地购买意图，确认冲突原因。已有订单可以通过查询接口同步验证结果；损坏事件保留审计，项目当前没有管理端重放或解除隔离接口，不建议直接修改订单状态。

离线教程现以 HTML 渲染：`/guides/`，支持目录、代码复制、移动端表格滚动；原 Markdown 用 `?format=raw` 或 `.md` 后缀访问。
