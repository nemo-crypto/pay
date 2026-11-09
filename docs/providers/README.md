# Pay Lab 六通道教程总入口

按统一顺序学习：启动mock → 创建订单 → 完成收银台步骤 → 验证通知 → 查询恢复 → 切商户测试环境。

主README提供六家总览、统一启动和操作流程。下面分别深入每家协议：

| 通道 | 业务结果 | 独立教程 |
| --- | --- | --- |
| Stripe | 信用卡储值、点数到账 | [完整教程](stripe.md) |
| Allscale | 稳定币储值、点数到账 | [完整教程](allscale.md) |
| MoonPay | 法币买币、交付到用户钱包 | [完整教程](moonpay.md) |
| Alchemy Pay | 法币买币、交付到用户钱包 | [完整教程](alchemypay.md) |
| Transak | 法币买币、交付到用户钱包 | [完整教程](transak.md) |
| Ramp | 法币买币、交付到用户钱包 | [完整教程](ramp.md) |

共用启动命令（项目根目录）：

```sh
HTTP_ADDR=127.0.0.1:8098 PUBLIC_URL=http://127.0.0.1:8098 DATABASE_PATH=data/pay-demo.db go run ./cmd/server
```

储值入口：http://127.0.0.1:8098 ，买币入口：http://127.0.0.1:8098/onramp 。默认全部使用mock，不需要真实凭据。

本地离线教程分别位于 `/guides/stripe`、`/guides/allscale`、`/guides/moonpay`、`/guides/alchemypay`、`/guides/transak`、`/guides/ramp`。

[买币共用API、状态和持久化](onramp-api.md)；[原前端储值对接与运维](../integration.md)。所有教程都明确mock和真实网关的差异；买币交付不等于商户收到可入账资金。
