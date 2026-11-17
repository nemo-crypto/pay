# 验证记录

## 七通道并发优化与压力验证（2026-11-17）

本次保留现有金额、验签、账户权限、幂等键与账本约束，优化网络等待和数据库争用，不变更支付接口或增加自动重试扣款。

| 通道 | 核对与处理 |
| --- | --- |
| Stripe | 保留原幂等键、建单恢复租约与 PaymentIntent 校验；同时查单合并，后台查单有界并行 |
| Allscale | 保留金额/币种和签名校验；不盲目重建结果未知的支付；同时查单合并 |
| 微信支付 | 保留五场景协议、商户/AppID/OpenID/金额校验；共享查单合并和事务保护；压测选用 Native 模拟场景 |
| MoonPay | 保留 external order ID 查询及多条匹配拒绝逻辑；同时查单合并，后台有界并行 |
| Alchemy Pay | 保留请求签名、商户订单号与交付校验；同时查单合并，后台有界并行 |
| Transak | 令牌刷新合并；网络和 SQL 等待期间释放验签锁；持久化成功后发布新 token，保留 previous token |
| Ramp | 保留 purchase/view token 绑定、Ethereum USDC 合约和交付证明校验；同时查单合并 |

共用优化：储值与买币 Worker 独立运行，每组最多 8 个查单任务；inbox 仍按原流程逐条处理。HTTP Transport 复用连接，每 host 最多 32 个连接，保留 15 秒超时及禁止自动跳转。SQLite 仍单连接，事务使用 BEGIN IMMEDIATE，避免多个 Store 连接先读后写时升级锁失败；连接初始化保留外键与 5 秒 busy timeout。增加订单复用、用户账本和买币订单列表索引。限流表达到容量后的过期扫描最多每秒一次，保留容量和每用户额度；满表时新用户最多额外等待约一秒才可使用刚过期的槽位。

新增回归：七通道各 64 并发同键建单；三个储值通道重复查单只入账一次；两个独立 SQLite 连接各通道 64 并发重复成功、退款、迟到成功，余额最终为零且只有一笔 credit 和一笔 refund；路径包含空格、?、#；同订单执行合并和等待者独立取消；8 个慢查单同时执行并保持上限；Transak 慢刷新不占用验签锁、取消等待者不打断刷新。原有验签、重复回调、账户隔离、金额错误、乱序通知、持久化恢复测试一并运行。

实际验证命令：

```bash
go test -race -count=1 ./...
go vet ./...
go build ./cmd/server ./cmd/token
go test ./internal/service ./internal/onramp -run '^$' -bench 'Benchmark(Topup|Onramp)Lifecycle' -benchtime=10000x -cpu=64 -count=1
git diff --check
```

压测环境：Apple M5 / darwin arm64，Go 1.26.11，每通道使用独立临时磁盘 SQLite，64 个并发执行者、10,000 笔不同用户订单，共 70,000 笔，零测试错误。储值流程包含服务建单、模拟网关成功、主动查单和余额校验；买币流程包含建单、模拟 KYC/付款/交付、主动查单、完成状态及不增加平台账本的校验。P95 为单笔整个模拟流程的耗时；flows/s 是流程吞吐，**不是 HTTP QPS 或真实支付网关容量**。

| 通道 | flows/s | P95（ms） |
| --- | ---: | ---: |
| Stripe | 1484 | 67.55 |
| Allscale | 1486 | 66.60 |
| 微信 Native | 1512 | 65.68 |
| MoonPay | 1496 | 64.64 |
| Alchemy Pay | 1521 | 63.01 |
| Transak | 1458 | 67.16 |
| Ramp | 1463 | 67.11 |

全部 Go 测试（含 race）、vet、构建通过。压测不包含公网网络、真实签名/加密开销、真实商户扣款、链上确认、HTTP 认证/限流链路或长时间稳态容量；没有重跑浏览器 E2E，也没有进行真实商户联调。没有提供优化前后的性能倍率对比。

部署边界：仍为单实例本地 SQLite 模型，单写者是吞吐上限；查单/刷新合并及限流均为进程内机制。双连接事务回归不代表已经支持多实例部署，真实多副本仍需要共享事务数据库、任务协调、分布式限流和 token 刷新协调。合并请求由发起者的 context 控制，发起者取消时其他等待者可能收到同次失败，可按现有查单/补单流程恢复；不会缓存已完成的结果。真实供应商限额需独立联调后配置，不能用这里的模拟结果承诺生产容量。

## 微信支付五场景扩展（2026-11-12）

实现普通直连商户 API v3：小程序、公众号/微信内 JSAPI、外部手机 H5、PC Native、原生 App。沿用储值订单与事务账本，加入各场景身份绑定、CNY固定报价快照、商户RSA签名、平台响应/通知验签、AES-GCM通知解密、商户单号补查和本地模拟。首页新增场景选择和本地 Native 二维码编码，离线教程 `/guides/wechat` 已注册。

- 微信专项回归通过：五种下单路径及请求字段、独立重建原文验证商户请求和客户端签名、API成功响应验签、通知篡改/过期/错误平台ID/错误解密密钥、商户/AppID/OpenID/币种/金额不符、客户端URL校验、状态映射、PKCS#1和PKCS#8密钥读取、重复通知只入账一次、汇率变化后的原意图重放、丢通知查单、下单超时保留商户单号、早到回调不被晚到响应覆盖、H5付款链接过期、数据库重新打开后的身份保留、可信代理IP提取、配置完整性及演示模式不使用真实密钥。
- `GOCACHE=/tmp/chovy-pay-go-cache go test -race ./... -skip '^(TestStripeCreateFieldsAndIdempotency|TestAllscaleProtocol|TestAllscaleRejectsUnsignedResponseWhenRequired|TestTransakTokenCacheAndSessionAPI)$'`：所有包通过。跳过的4项是既有依赖本地TCP监听的测试，当前沙箱禁止监听；新增微信协议测试使用内存HTTP Transport，无需端口。
- `go vet ./...`、`go build ./cmd/server ./cmd/token`、JS语法及 `git diff --check` 通过（Go使用上述 `/tmp` 缓存）。二维码浏览器模块来自工作区已有 node-qrcode 1.5.4 / dijkstrajs 1.0.3，由 esbuild 0.28.2 离线打包，附MIT许可证，无新增Go依赖或运行时CDN。
- 新增 `npm run test:wechat` 五场景浏览器回归，更新原储值脚本的七通道卡片计数。浏览器**未通过环境启动**：pay 尚未安装 Playwright；使用工作区已有1.58.2尝试启动后，Chromium因macOS沙箱 `MachPortRendezvousServer: Permission denied` 退出，因此没有宣称本轮页面/E2E验收通过。

当前无法解析微信官方域名，未访问真实商户API、未扣款、未进行微信客户端或商户公网通知验收。后续需在正常环境执行 `npm ci`、安装Chromium，在独立全新demo服务/限流窗口运行三类浏览器回归，再用已开通产品的商户按五类终端分别联调。公众号OAuth、小程序code2session及原生OpenSDK工程由原应用接入；服务商/子商户、主动关单、退款流程及自动平台公钥轮换不在本轮实现内。

## 金额边界、限流隔离与对账索引（2026-11-12）

- 汇率报价在高精度整数中完成元到分的乘法，避免 `int64` 溢出；覆盖正负溢出输入、最大输出边界及大金额合法报价，保留向上取整规则。
- 限流键按读写前缀区分用户，覆盖 `alice` 写请求与 `alice-write` 读请求的历史碰撞场景，并确认写额度耗尽后仍可读取。
- 储值对账订单及未处理回调新增部分索引。旧库重新打开后账户余额和回调仍保留；`EXPLAIN QUERY PLAN` 确认两类查询使用新索引且无需临时排序。大库首次启动需留出创建索引的时间；尚未进行大数据量吞吐压测。
- 使用 `GOCACHE=/tmp/chovy-pay-go-cache`，`go vet ./...`、`go build ./cmd/server ./cmd/token` 和 `git diff --check` 通过。
- 完整 `go test -race ./...` 受当前沙箱禁止绑定本地端口限制，HTTP mock 服务启动报 `bind: operation not permitted`。以下命令跳过4个需要监听端口的现有测试后，所有包通过：

```sh
GOCACHE=/tmp/chovy-pay-go-cache go test -race ./... \
  -skip '^(TestStripeCreateFieldsAndIdempotency|TestAllscaleProtocol|TestAllscaleRejectsUnsignedResponseWhenRequired|TestTransakTokenCacheAndSessionAPI)$'
```

本轮未运行浏览器回归或真实网关交易。chovy 下24个仓库均尝试执行 `git pull --ff-only`，但因无法解析 `github.com` 全部失败；上述修改基于现有本地版本，需在网络可用后同步远端，并在允许本地监听端口的环境补跑完整测试。

日期：2026-11-09。环境：macOS、Go 1.25.11、Playwright 1.58.2 Chromium。

- `go test -race ./...`：通过，包含 HTTP 业务、认证、配置、支付协议和报价测试。
- `go vet ./...`：通过。
- `go build ./cmd/server ./cmd/token`：通过。
- `git diff --check`：通过。
- `scripts/e2e.cjs`：1440×1000 桌面与390×844手机，各完成 Stripe 和 Allscale 的创建、模拟付款、订单成功和余额增加2000点，共四条流程；无页面脚本异常和页面级横向溢出。已人工查看两种尺寸首页截图。

专项测试覆盖并发重复建单/重复通知、同键不同参数、金额/币种/订单不符、访客和跨用户访问、签名篡改与过期、同 nonce 不同事件、回调先到、丢回调查询补单、数据库重新打开、取消提示后的真实付款、乱序状态及重复全额退款。建单请求中断时，Stripe 恢复相同订单与到期参数，Allscale 保留复核且不重新创建。

真实网关请求由本地 HTTP mock 验证，Allscale HMAC 另用独立计算的固定测试向量核对。未进行真实商户扣款、链上交易、退款或部署；Dockerfile 未运行构建。此记录不代表通过生产支付认证。

## 六通道扩展验证（2026-11-09）

- 新增MoonPay、Alchemy Pay、Transak、Ramp本地闭环：创建、KYC、付款、交付；没有修改原点数账本。
- `go test -race ./...`、`go vet ./...`、`go build ./cmd/server ./cmd/token`：通过。
- 验签专项：MoonPay原始body HMAC、Alchemy排序JSON HMAC、Transak HS256 JWT、Ramp secp256k1 DER签名；伪造/重复/乱序事件、字段不符、未知状态/缺少交付信息均有测试。URL签名使用独立Python计算的固定测试向量。
- 订单专项：并发幂等、同键改参数、跨用户访问、非法演示步骤、失败/取消、丢通知查询恢复、事件收件箱恢复、同一供应商purchase不能重复绑定、Transak token缓存重启恢复。
- `scripts/onramp-demo.py --provider all --drop-webhook`：四家全部完成，状态completed，分别返回正确模拟数量；脚本不依赖第三方Python包且拒绝live环境。
- 四家买币浏览器：1440×1000桌面成功链路、390×844手机丢Webhook后主动查询恢复，共8条流程通过，无页面脚本错误或页面级横向溢出。
- 最终Stripe/Allscale桌面与手机回归4条通过，四家买币回归8条通过，合计12条页面流程。
- 六家`/guides/{provider}`离线教程均返回200，Markdown本地文件链接检查通过；已查看桌面订单页与手机首页截图。
- 官方协议来源见各家独立教程。只读核对Ramp公开assets API，确认USDC短代码需要结合Ethereum合约地址识别；未创建真实商户订单、扣款或链上交易。

mock与真实环境差异：mock固定手续费1 USD、预置交易引用及模拟链上哈希；真实Transak/Ramp需从SDK/验签通知取得引用，真实KYC/费用/资产可用性由供应商决定。Docker构建与真实商户Sandbox交易没有执行，不计为已验证项。

## 可靠性与教程体验回归（2026-11-09）

- `go test -race ./...`、`go vet ./...`、Go服务及token工具构建通过。
- 新增可靠性回归：确认通知先于建单网络失败时保留已交付状态；100个损坏事件被隔离后新付款可继续完成；临时错误退避封顶1小时；旧六列收件箱自动迁移且数据不丢失；400/503错误字段及供应商敏感信息不外泄。
- 离线阅读页：六家教程、总览及运维/API页渲染、原文兼容、HEAD、静态资源、未知页404、本地链接改写和并发渲染测试通过。
- `scripts/e2e.cjs`：桌面/手机共4条储值成功并增加点数流程通过，同时验证六通道卡片及按URL预选支付方式。
- `scripts/onramp-e2e.cjs`：桌面/手机共8条买币交付流程通过，手机包含丢Webhook主动查询恢复；教程导航、目录、剪贴板复制和页面无横向溢出通过。已查看手机首页、桌面与手机教程截图。
- 首次阅读页测试因移动端隐藏箭头导致选择器不匹配，已按链接地址定位修正；短时间重复运行曾触发本地每用户限流，最终在全新服务实例回归通过。

以上均使用本地mock，不代表真实商户Sandbox、生产扣款或链上交易已验收。隔离事件目前需要人工核对，未提供管理端重放接口。
