const phase = (title, action, actor, customer, server, evidence) => ({ title, action, actor, customer, server, evidence });
const order = phase('建立本地订单', '创建演示订单', '平台', '核对支付金额和到账资产，提交一次购买意图。', '保存本地订单编号、金额、币种和幂等键。重复提交同一个意图时，返回同一笔订单。', ['本地订单', '已创建 · 等待供应商参考号']);
const topupReceipt = phase('发放点数', '确认点数入账', '平台账本', '储值记录变成已完成，模拟点数余额增加。', '事务内更新订单、写入唯一入账记录，再增加点数。重复通知返回同一个结果。', ['credit 账本', '同一订单只记一次']);
const deliveryReceipt = phase('记录钱包交付', '确认钱包到账', '平台订单', '在交付记录里看到模拟 USDC 数量、网络和交易哈希。', '核对订单对应的钱包、网络、币种、金额、交付数量与交易哈希，写入最终状态。', ['交付记录', '数量与交易哈希已核对']);
const payer = (title, action, customer, server, evidence) => phase(title, action, '付款人', customer, server, evidence);
const gateway = (title, action, customer, server, evidence) => phase(title, action, '支付通道', customer, server, evidence);
const proof = (title, customer, server, evidence) => phase(title, '验收模拟回调', '平台服务端', customer, server, evidence);

export const wechatScenes = {
  native: { name: 'PC 扫码', entry: '领取订单二维码', field: 'code_url', customer: '电脑显示本单二维码，由手机微信扫码并打开确认页。', check: '二维码随当前订单生成。等待扫码期间，网页只查询自己订单的状态。' },
  mini: { name: '小程序', entry: '打开小程序收银台', field: 'wx.requestPayment', customer: '在小程序里确认付款。', check: '服务端校验小程序 AppID 与当前付款人的 OpenID 绑定，再返回签名所需字段。' },
  jsapi: { name: '微信内网页', entry: '唤起微信网页支付', field: 'WeixinJSBridge', customer: '在微信内置浏览器唤起支付界面。', check: '使用对应公众号 AppID 与 OpenID。浏览器支付组件返回后，查询原订单。' },
  h5: { name: '手机浏览器 H5', entry: '跳转手机微信', field: 'h5_url', customer: '从微信外的手机浏览器跳转到微信确认，再返回业务页。', check: '核对 H5 场景、客户端 IP 与允许的回跳域名。回到页面后沿用原订单查询。' },
  app: { name: '原生 App', entry: '调用 App 支付 SDK', field: 'prepay_id', customer: '在原生应用调用微信 SDK 打开付款。', check: '按 App 场景定义签名字段并核对 AppID；SDK 返回结果后由应用查单。' },
};

export const providers = [
  {
    id: 'wechat', name: '微信支付', kind: '微信钱包', quote: 'NT$1,000 → ¥222.23（示例）',
    intro: '从五种客户端场景中选一种，走完预支付、付款回跳、平台验签和点数入账。',
    query: '用原商户单号查询微信订单，验证查单响应签名，再核对商户号、AppID、金额及适用的付款人身份。',
    steps: scene => {
      const mode = wechatScenes[scene];
      return [
        order,
        gateway('下发微信预支付凭证', mode.entry, mode.customer, mode.check, ['预支付载体', mode.field]),
        payer('付款并返回业务页', '模拟微信付款', '确认教学报价，看到客户端付款反馈。', '页面收到回跳后进入待确认；订单金额仍按创建时的 CNY 原始金额核对。', ['浏览器状态', '已返回 · 账本未变动']),
        proof('验签并解密通知', '订单显示正在确认。', '按平台公钥验证通知签名，使用 APIv3 密钥解密；核对商户、AppID、订单和原始总额后收下事件。', ['微信事件', 'TRANSACTION.SUCCESS · 签名、金额已核对']),
        topupReceipt,
      ];
    },
  },
  {
    id: 'stripe', name: 'Stripe', kind: '银行卡', quote: 'NT$1,000 · TWD 示例金额',
    intro: '从本地订单进入托管 Checkout，比较收银台完成、款项 paid 和点数入账。',
    query: '查询原 Checkout Session，确认 payment_status 为 paid，并核对金额、币种与本地订单引用。',
    steps: [
      order,
      gateway('创建 Checkout Session', '打开模拟 Checkout', '从 Pay Lab 进入教学收银台，订单使用固定 TWD 金额。', '创建托管 Session，并保存 Session ID 与本地订单的关联。', ['支付凭证', 'Checkout Session ID']),
      payer('完成银行卡付款', '模拟卡付款', '看到收银台完成提示，并回到订单页。', '延迟付款方式可能先完成 Checkout。平台继续等待可证实的 paid 状态。', ['收银台状态', '已返回 · 点数尚未发放']),
      proof('核对 Checkout 与付款状态', '这笔储值正在核对。', '验证 Stripe-Signature 所覆盖的原始请求，再匹配 Session、金额、币种与订单，并确认 payment_status=paid。', ['Stripe 事件', 'payment_status = paid']),
      topupReceipt,
    ],
  },
  {
    id: 'allscale', name: 'Allscale', kind: '稳定币付款', quote: 'NT$1,000 储值 · 收银台使用模拟稳定币',
    intro: '用稳定币给平台储值，观察发出转账、等待链上确认与平台发放点数。',
    query: '查询原 Checkout Intent 的完整详情。状态为 20 后，仍要核对金额、币种和 Intent 关联。',
    steps: [
      order,
      gateway('创建 Checkout Intent', '打开模拟收银台', '查看本单支持的币种、网络和冻结报价。', '保存 Checkout Intent ID 和原先的报价，确保事件对应当前订单。', ['供应商参考号', 'Checkout Intent ID']),
      payer('发出稳定币转账', '模拟转账', '看到转账已提交。', '保存教学转账状态。供应商状态 10 表示链上确认中，点数保持零变动。', ['供应商状态', '10 · 链上确认中']),
      proof('查验已确认的 Intent', '等链上确认完成后，订单进入入账处理。', '验证通知签名及事件标识；匹配通知的 all_scale_checkout_intent_id，再核对金额和币种。', ['供应商状态', '20 · 已确认']),
      topupReceipt,
    ],
  },
  {
    id: 'moonpay', name: 'MoonPay', kind: '托管买币页', onramp: true,
    intro: '从签名托管页开始，区分身份确认、法币付款和最终钱包交付。',
    query: '按 externalTransactionId 查询。返回恰好一条时再核对金额、USDC、钱包和订单关联；零条保持等待，多条送复核。',
    steps: [
      order,
      gateway('生成签名托管链接', '打开模拟托管页', '进入教学托管页，核对 USD、USDC 和收款网络。', '把本地订单号作为 externalTransactionId，并为托管链接签名。', ['订单关联', 'externalTransactionId']),
      payer('完成模拟身份确认', '通过模拟 KYC', '确认教学身份审核通过；真实审核由供应商负责。', '平台保留本地订单。身份确认阶段不写交付记录。', ['用户阶段', '身份确认完成']),
      payer('支付法币并等待发币', '模拟 USD 付款', '看到付款提交，页内仍在处理交付。', 'pending 继续等待，平台只通过验证后的供应商证据更新最终结果。', ['MoonPay 状态', 'pending · 交付处理中']),
      proof('验证交易完成通知', '收银台结束后，可以回到订单页看交付结果。', '验 Moonpay-Signature-V2；识别 completed，核对订单、币种、钱包、交付数量与交易哈希。', ['MoonPay 状态', 'completed · 有交付证据']),
      deliveryReceipt,
    ],
  },
  {
    id: 'alchemypay', name: 'Alchemy Pay', kind: 'Ramp Page', onramp: true,
    intro: '用 PAY_SUCCESS、TRANSFER、FINISHED 三种状态看清「付了钱」和「币到钱包」的时间差。',
    query: '按 merchantOrderNo 查询交易详情，验证查询请求使用的商户配置，并对账 FINISHED 与交付数量、哈希。',
    steps: [
      order,
      gateway('生成签名 Ramp Page', '打开模拟 Ramp Page', '查看业务订单号、金额和目标网络。', '签收银台 URL，把 merchantOrderNo 和服务端回调关联到当前订单。', ['订单关联', 'merchantOrderNo']),
      payer('完成模拟身份确认', '通过模拟 KYC', '在供应商教学页面完成身份核验。', '业务端只保留当前订单与后续状态，不在公开示例接收任何身份资料。', ['用户阶段', '身份确认完成']),
      payer('付款成功，转账处理中', '模拟 USD 付款', '看到法币已付款，USDC 还在交付途中。', 'PAY_SUCCESS 表示收到付款；TRANSFER 继续等待交付。', ['Alchemy Pay 状态', 'PAY_SUCCESS → TRANSFER']),
      proof('验证 FINISHED 回调', '可查看最终钱包交付记录。', '校验 newSignature，再核对商户、订单、金额、网络、地址、cryptoQuantity 和 txHash。', ['Alchemy Pay 状态', 'FINISHED · 包含数量和哈希']),
      deliveryReceipt,
    ],
  },
  {
    id: 'transak', name: 'Transak', kind: '一次性 Widget', onramp: true,
    intro: '跟着 Token、Session 和 Widget URL 走到订单绑定及交付确认。',
    query: '使用已绑定的供应商订单 ID 查单。若通知丢失且从未取得该 ID，先找回并由服务端验证绑定。',
    steps: [
      order,
      gateway('取得 Token，创建一次性 Session', '打开模拟 Widget', '打开与本单关联的托管 Widget。', '服务端缓存 Partner Access Token，创建一次性 widgetUrl；凭据留在服务端。', ['支付凭证', 'Session ID / widgetUrl']),
      payer('审核身份并关联供应商订单', '模拟 KYC 和订单绑定', '在托管组件完成教学身份确认。', '可从 SDK 获取订单引用，服务端再用查询确认 partnerOrderId、金额、钱包和网络后绑定。', ['订单关联', 'partnerOrderId + provider order ID']),
      payer('付款并等待 Transak 交付', '模拟 USD 付款', '看到已付款，等待供应商发币。', 'PENDING_DELIVERY_FROM_TRANSAK 仍是处理中；不要使用旧的一次性 URL 恢复未知订单。', ['Transak 状态', 'PENDING_DELIVERY_FROM_TRANSAK']),
      proof('验收 COMPLETED 订单对象', '查看订单的最终状态和交付明细。', '用服务端 Partner Access Token 验证通知中的 JWT，再核对 webhookData、金额、钱包和交易哈希。', ['Transak 状态', 'COMPLETED · 订单证明有效']),
      deliveryReceipt,
    ],
  },
  {
    id: 'ramp', name: 'Ramp', kind: 'Hosted Widget', onramp: true,
    intro: '从核对资产能力开始，体验 Purchase 绑定、ECDSA 验证和 USDC 最小单位换算。',
    query: '用已绑定的 purchase.id 与该订单的 purchaseViewToken 查询，再验证钱包、资产、数额及交易哈希。',
    steps: [
      order,
      gateway('核对资产能力并打开 Hosted Widget', '打开模拟 Widget', '查看固定的 Ethereum USDC 教学订单。', '确认选定网络及币种可用，并生成关联到本单的托管入口。', ['支付入口', 'Hosted Widget']),
      payer('完成模拟身份确认并绑定 Purchase', '模拟 KYC 和 Purchase 绑定', '在托管页通过教学身份确认并创建购买。', '保存供应商 purchase.id；查询凭证 purchaseViewToken 留在服务端。', ['订单关联', 'purchase.id']),
      payer('付款并等待 RELEASED', '模拟 USD 付款', '看到法币付款成功，等待资产发送。', 'FIAT_RECEIVED 和 RELEASING 都继续处理。平台路由的订单关联参数只负责匹配。', ['Ramp 状态', 'FIAT_RECEIVED → RELEASING']),
      proof('校验签名与交付精度', '看到模拟 USDC 数量与交易记录。', '用 Ramp 公钥校验 ECDSA 签名，核对 RELEASED、钱包与哈希。USDC 精度为 6，因此 29000000 最小单位等于 29 USDC。', ['Ramp 状态', 'RELEASED · 29000000 最小单位']),
      deliveryReceipt,
    ],
  },
];
