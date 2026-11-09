# 验证记录

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
