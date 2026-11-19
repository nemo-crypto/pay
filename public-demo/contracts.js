// Examples mirror the repository's handlers and adapters. IDs and credentials are placeholders.
const topId = 'top_example';
const onId = 'on_example';
const wallet = '0x1111111111111111111111111111111111111111';
const tx = '0x' + 'a'.repeat(64);
const timestamp = '2030-01-01T10:00:00Z';
const payOrigin = 'http://127.0.0.1:8098';
const checkoutURL = `${payOrigin}/demo/checkout?order_id=${topId}`;
const onCheckoutURL = `${payOrigin}/onramp/checkout?order_id=${onId}`;
const auth = { Authorization: 'Bearer <应用访问令牌>' };
const json = { 'Content-Type': 'application/json' };
const received = { received: true };

function contract(title, direction, method, path, request, status, response, fields, next) {
  return { title, direction, method, path, request, status, response, fields, next };
}

function data(headers, body, encoding) {
  return { headers, body, encoding };
}

function topFields(id) {
  const [currency, minor] = { wechat: ['CNY', 22223], stripe: ['TWD', 100000], allscale: ['USD', 3125] }[id];
  return { currency: 'TWD', nt_amount: 1000, payment_currency: currency, payment_amount_minor: minor };
}

function wechatCheckout(scene) {
  const base = { mode: scene, url: checkoutURL };
  if (scene === 'native') return { ...base, code_url: 'weixin://wxpay/bizpayurl?pr=DEMO_top_example' };
  if (scene === 'h5') return base;
  const app = scene === 'app';
  return {
    ...base, wechat: {
      appId: 'wx_example', timeStamp: '1893492000', nonceStr: '<支付随机串>',
      package: app ? 'prepay_example' : 'prepay_id=prepay_example', signType: 'RSA', paySign: '<示例签名>',
      ...(app ? { partnerId: '<示例商户号>' } : {}),
    },
  };
}

function topOrder(id, scene, status = 'pending') {
  const isPaid = status === 'succeeded';
  return {
    id: topId, gateway: id, ...(id === 'wechat' ? { wechat_scene: scene } : {}),
    plan_id: 'demo-1000', status, ...topFields(id), coin_amount: 2000, coins_credited: isPaid ? 2000 : 0,
    ...(id === 'allscale' && isPaid ? { crypto: { chain_id: 8453, coin_symbol: 'USDC', amount_coins: '31.25', tx_hash: 'demo-not-a-real-transaction' } } : {}),
  };
}

function onOrder(id, status = 'pending', stage = 0) {
  return {
    id: onId, provider: id, wallet_address: wallet, fiat_currency: 'USD', fiat_amount_minor: 3000,
    asset: 'USDC', network: 'ethereum', status, mode: 'demo', demo_stage: stage,
    ...(stage === 0 ? { estimated_crypto_amount: '29.00', checkout_url: onCheckoutURL } : {}),
    ...(status === 'completed' ? { crypto_amount: '29.00', tx_hash: tx } : {}),
  };
}

function localOrder(id, scene, onramp) {
  const path = onramp ? '/onramp/orders' : '/topups';
  const body = onramp ? { provider: id, wallet_address: wallet, fiat_amount_minor: 3000 } : {
    plan_id: 'demo-1000', gateway: id, ...(id === 'wechat' ? { wechat_scene: scene } : { ui_mode: 'hosted' }),
  };
  const response = onramp ? onOrder(id) : {
    order_id: topId, gateway: id, status: 'pending', ...topFields(id),
    checkout: id === 'wechat' ? wechatCheckout(scene) : { mode: 'hosted', url: checkoutURL },
    replayed: false, reused: false, first_topup_discount: false,
  };
  return contract('创建业务订单', '前端 → Pay Lab', 'POST', path,
    data({ ...auth, ...json, 'Idempotency-Key': 'example-purchase-001' }, body), '201 Created', response,
    [
      ['Idempotency-Key', '请求头 · string', '同一个购买意图重试沿用此值。同键换参数会冲突。'],
      [onramp ? 'fiat_amount_minor' : 'plan_id', onramp ? '请求 JSON · integer' : '请求 JSON · string', onramp ? '3000 个美分，即 30 USD。' : '固定储值套餐，由服务端查价格和点数。'],
      [onramp ? 'id / checkout_url' : 'order_id / checkout', '响应 JSON', '保存本地订单号和收银台信息；建单成功仍处于待支付状态。'],
      [onramp ? 'wallet_address' : 'payment_amount_minor', onramp ? '请求 JSON · string' : '响应 JSON · integer', onramp ? '教学收款地址；币种与网络由本项目固定为 Ethereum USDC。' : '供应商金额的最小单位，结合 payment_currency 解读。'],
    ], '保存订单号。进入适用的收银台；请求超时先复用原幂等键或查询原单。');
}

function createConflict(id, scene, onramp) {
  const original = localOrder(id, scene, onramp);
  const changed = onramp ? { fiat_amount_minor: 5000 } : { plan_id: 'demo-2000' };
  return contract('识别幂等键冲突', original.direction, original.method, original.path,
    data(original.request.headers, { ...original.request.body, ...changed }), '409 Conflict',
    {
      code: onramp ? 'CONFLICT' : 'IDEMPOTENCY_CONFLICT',
      message: onramp ? '请求与已有订单或当前操作状态冲突，请查询原订单' : '幂等键已用于不同请求',
      retriable: false, trace_id: 'trace_example', ...(!onramp ? { details: { order_id: '' } } : {}),
    }, [
      ['Idempotency-Key', '请求头 · string', '此例重用了首次请求的键，但更改了金额或套餐。'],
      ['code / trace_id', '响应 JSON · string', '用错误码提示并记录追踪 ID；先查原单。'],
    ], '保留原购买意图用于查询或恢复。用户主动确认另一笔购买时才生成新键。');
}

function userOrder(id, scene, onramp, status, stage) {
  return contract('查询订单状态', '前端 → Pay Lab', 'GET', onramp ? '/onramp/orders/{id}' : '/topups/{id}',
    data(auth), '200 OK', onramp ? onOrder(id, status, stage) : topOrder(id, scene, status),
    [
      ['{id}', '路径参数 · string', '创建时返回的 Pay Lab 本地订单 ID。'],
      ['status', '响应 JSON · string', '决定业务页显示等待、失败或最终成功。'],
      [onramp ? 'crypto_amount / tx_hash' : 'coins_credited', '响应 JSON', onramp ? '交付成功后才有最终数量和交易哈希；金额是字符串。' : '顾客实际增加的点数，和套餐的 coin_amount 分开。'],
    ], '回跳只触发查询。按服务端状态刷新页面及资产，保留原订单以便恢复未决结果。');
}

function demoAction(id, onramp, action, deliver) {
  const stages = { kyc: ['pending', 1], pay: ['processing', 2], deliver: ['completed', 3], fail: [id === 'ramp' ? 'cancelled' : 'failed', 4] };
  const response = onramp ? onOrder(id, deliver ? stages[action][0] : 'pending', stages[action][1]) : {
    accepted: true, message: '虚拟收银台已更新；到账以订单查询为准',
  };
  return contract('本地模拟操作', '本地演示页 → Pay Lab（仅 demo 模式）', 'POST',
    onramp ? '/onramp/demo/{id}/advance' : '/demo/orders/{id}/pay',
    data({ ...auth, ...json }, onramp ? { action, deliver_webhook: deliver } : { outcome: action, deliver_webhook: deliver }),
    '200 OK', response, [
      [onramp ? 'action' : 'outcome', '请求 JSON · string', onramp ? 'kyc / pay / deliver / fail 按演示顺序执行。' : 'succeeded 或 failed 表示模拟收银台结果。'],
      ['deliver_webhook', '请求 JSON · boolean', 'false 只更新模拟供应商状态，用于练习漏通知。部分负面事件需要查询确认。'],
      [onramp ? 'demo_stage / status' : 'accepted', '响应 JSON', onramp ? '模拟操作进度与本地订单状态是两个字段；回调或查询才会同步订单结果。' : '仅表示模拟请求受理，随后 GET 原订单。'],
    ], '这些请求只用于本地 Go 演练。当前公开页面展示交换样例，按钮只改变浏览器内教学状态。');
}

function providerCheckout(id, scene) {
  const map = {
    stripe: contract('创建 Checkout Session', 'Pay Lab → Stripe', 'POST', '/v1/checkout/sessions',
      data({ 'Content-Type': 'application/x-www-form-urlencoded', 'Idempotency-Key': 'checkout-' + topId,
        Authorization: 'Basic <服务端凭据>' }, {
        mode: 'payment', ui_mode: 'hosted', client_reference_id: topId, 'metadata[order_id]': topId,
        'line_items[0][price_data][currency]': 'twd', 'line_items[0][price_data][unit_amount]': '100000',
        'line_items[0][quantity]': '1',
      }, '表单字段（用 JSON 结构展示）'), '成功响应', { id: 'cs_example', url: '<Stripe 托管 URL>', livemode: false },
      [['unit_amount', '表单 · string', 'TWD 最小金额单位，100000 对应示例 NT$1,000。'], ['metadata[order_id]', '表单 · string', '绑定本地订单；后续查单和通知都要核对。']],
      '服务端保存 Session ID；前端使用 Pay Lab 返回的 checkout.url。'),
    allscale: contract('创建 Checkout Intent', 'Pay Lab → Allscale', 'POST', '/v1/checkout_intents/',
      data({ ...json, 'X-API-Key': '<商户 key>', 'X-Timestamp': '<时间戳>', 'X-Nonce': '<随机串>', 'X-Signature': '<签名>' },
        { currency: 1, amount_cents: 3125, order_id: topId, order_description: '点数储值 2000 点', user_id: 'demo_user', redirect_url: '<业务回跳 URL>' }),
      '成功响应', { code: 0, payload: { allscale_checkout_intent_id: 'intent_example', checkout_url: '<Allscale 托管 URL>' } },
      [['currency / amount_cents', '请求 JSON · integer', 'currency=1 表示 USD。3125 是冻结后的 31.25 USD 教学报价。'], ['allscale_checkout_intent_id', '响应 JSON · string', '保存供应商引用。通知键的命名是 all_scale_checkout_intent_id。']],
      '核对返回入口，打开 Pay Lab checkout.url；发出转账以后仍要等确认。'),
    moonpay: contract('生成签名托管链接', 'Pay Lab 拼接 URL → 前端打开托管页', 'GET', 'MoonPay 托管入口（配置 URL）',
      data({}, { currencyCode: 'usdc', baseCurrencyCode: 'usd', baseCurrencyAmount: '30.00',
        externalTransactionId: onId, walletAddress: wallet, lockAmount: 'true', redirectURL: '<业务回跳 URL>', signature: '<签名>' }, 'URL 查询参数（用 JSON 结构展示）'),
      '浏览器页面', null, [['externalTransactionId', '查询参数 · string', '供应商回调和查单用本地订单号匹配。'], ['signature', '查询参数 · string', '由服务器生成。前端使用已签好的 checkout_url。']],
      '托管页处理身份和支付；保存本地 ID，回跳后再查询。'),
    alchemypay: contract('生成签名 Ramp Page', 'Pay Lab 拼接 URL → 前端打开托管页', 'GET', '/index/rampPageBuy',
      data({}, { appId: '<商户 appId>', timestamp: '1893492000000', merchantOrderNo: onId, crypto: 'USDC', network: 'ETH', fiat: 'USD',
        fiatAmount: '30.00', address: wallet, showTable: 'buy', callbackUrl: '<后端通知 URL>', redirectUrl: '<业务回跳 URL>', sign: '<签名>' }, 'URL 查询参数（用 JSON 结构展示）'),
      '浏览器页面', null, [['merchantOrderNo', '查询参数 · string', '用原来创建的 Pay Lab 订单号。'], ['callbackUrl / redirectUrl', '查询参数 · string', '服务端通知与浏览器回跳各有用途。']],
      '回跳只触发本地查单。PAY_SUCCESS、TRANSFER 等中间状态继续等待。'),
    transak: contract('创建一次性 Widget Session', 'Pay Lab → Transak', 'POST', '/api/v2/auth/session',
      data({ ...json, 'x-api-key': '<商户 key>', 'access-token': '<服务端 Token>', 'x-user-ip': '<用户 IP>' }, {
        widgetParams: { apiKey: '<商户 key>', referrerDomain: '<业务域名>', fiatCurrency: 'USD', fiatAmount: 30, cryptoCurrencyCode: 'USDC',
          network: 'ethereum', walletAddress: wallet, disableWalletAddressForm: true, partnerOrderId: onId, redirectURL: '<业务回跳 URL>', productsAvailed: 'BUY' },
      }), '成功响应', { data: { widgetUrl: '<一次性 Widget URL>' } },
      [['widgetParams.partnerOrderId', '请求 JSON · string', 'Pay Lab 订单 ID，回调或查单时核实该关联。'], ['data.widgetUrl', '响应 JSON · string', '返回的 URL 有使用次数和过期限制；不要复用过期的 URL。']],
      '前端只打开 Pay Lab 返回的 checkout_url，Token 留在后端。'),
    ramp: contract('打开 Hosted Widget', 'Pay Lab 拼接 URL → 前端打开托管页', 'GET', 'Ramp Hosted Widget（配置 URL）',
      data({}, { hostApiKey: '<商户 key>', hostAppName: 'Pay Lab', swapAsset: 'ETH_USDC', fiatCurrency: 'USD', fiatValue: '30.00',
        userAddress: wallet, webhookStatusUrl: '<后端通知 URL>', finalUrl: '<业务回跳 URL>', enabledFlows: 'ONRAMP' }, 'URL 查询参数（用 JSON 结构展示）'),
      '浏览器页面', null, [['swapAsset', '查询参数 · string', '选择本示例固定的 Ethereum USDC。'], ['webhookStatusUrl / finalUrl', '查询参数 · string', '付款后可回跳查单，而通知直接发服务端。']],
      '等待托管页生成 purchase.id，再通过可信记录完成关联。'),
  };
  if (id !== 'wechat') return map[id];
  const product = scene === 'mini' ? 'jsapi' : scene;
  const body = { appid: 'wx_example', mchid: '<商户号>', description: '点数储值 2000 点',
    out_trade_no: topId, notify_url: '<后端通知 URL>', time_expire: timestamp, amount: { total: 22223, currency: 'CNY' } };
  if (product === 'jsapi') body.payer = { openid: '<服务端已绑定 OpenID>' };
  if (product === 'h5') body.scene_info = { payer_client_ip: '192.0.2.1', h5_info: { type: 'Wap' } };
  const key = { native: 'code_url', h5: 'h5_url' }[product] ?? 'prepay_id';
  return contract('微信统一下单', 'Pay Lab → 微信支付', 'POST', '/v3/pay/transactions/' + product,
    data({ ...json, Authorization: 'WECHATPAY2-SHA256-RSA2048 <签名参数>' }, body), '成功响应', { [key]: '<教学占位>' },
    [['out_trade_no', '请求 JSON · string', '本地商户单号。缺回调时用它查询原单。'], ['amount.total / currency', '请求 JSON · integer / string', '22223 分 = 222.23 CNY。'], [key, '响应 JSON · string', '供应商返回的预支付载体，由 Pay Lab 转成前端 checkout 字段。']],
    scene === 'native' ? '前端读取 checkout.code_url，生成当前订单二维码。' : scene === 'h5' ? '前端读取 checkout.url，跳转到微信确认页面。' : '按微信客户端场景读取 checkout.wechat，再调用支付组件。');
}

const stripeSession = { id: 'cs_example', status: 'complete', payment_status: 'paid', amount_total: 100000,
  currency: 'twd', payment_intent: 'pi_example', metadata: { order_id: topId }, livemode: false };
const intent = { all_scale_checkout_intent_id: 'intent_example', order_id: topId, status: 20, currency: 1, amount_cents: 3125,
  chain_id: 8453, coin_symbol: 'USDC', amount_coins: '31.25', tx_hash: tx };
const wechatTransaction = {
  appid: 'wx_example', mchid: '<商户号>', out_trade_no: topId, transaction_id: 'wx_tx_example',
  trade_state: 'SUCCESS', amount: { total: 22223, currency: 'CNY' },
};
const moonTransaction = { id: 'moon_example', externalTransactionId: onId, walletAddress: wallet,
  baseCurrency: { code: 'usd' }, currency: { code: 'usdc' }, baseCurrencyAmount: 30, quoteCurrencyAmount: 29,
  status: 'completed', cryptoTransactionId: tx };
const alchemyTransaction = { appId: '<商户 appId>', merchantOrderNo: onId, orderNo: 'ach_example', address: wallet,
  fiat: 'USD', crypto: 'USDC', network: 'ETH', amount: '30.00', cryptoQuantity: '29.00', txHash: tx, status: 'FINISHED' };
const transakTransaction = { id: 'transak_example', partnerOrderId: onId, walletAddress: wallet, fiatCurrency: 'USD',
  cryptoCurrency: 'USDC', network: 'ethereum', fiatAmount: 30, cryptoAmount: 29, transactionHash: tx, status: 'COMPLETED' };
const rampTransaction = { id: 'purchase_example', receiverAddress: wallet, fiatCurrency: 'USD', fiatValue: 30,
  asset: { symbol: 'USDC', decimals: 6, address: '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48' }, cryptoAmount: '29000000',
  finalTxHash: tx, purchaseViewToken: '<该订单查询凭证>', status: 'RELEASED' };

const webhookSamples = {
  wechat: {
    headers: { 'Wechatpay-Timestamp': '1893492000', 'Wechatpay-Nonce': '<通知随机串>', 'Wechatpay-Serial': '<平台公钥 ID>', 'Wechatpay-Signature': '<签名>' },
    body: { id: 'evt_wx_example', event_type: 'TRANSACTION.SUCCESS', resource_type: 'encrypt-resource', resource: {
      algorithm: 'AEAD_AES_256_GCM', original_type: 'transaction', ciphertext: '<密文>', nonce: '<解密随机串>', associated_data: 'transaction',
    } },
    decoded: wechatTransaction,
    fields: [['resource', '请求 JSON · object', '验平台签名后解密，读取业务交易。'], ['out_trade_no / amount.total', '解密 JSON', '核对原单号、商户、AppID 和原始 CNY 总额。']],
  },
  stripe: {
    headers: { 'Stripe-Signature': 't=<时间戳>,v1=<签名>' },
    body: { id: 'evt_stripe_example', type: 'checkout.session.completed', livemode: false, data: { object: stripeSession } },
    fields: [['data.object.payment_status', '请求 JSON · string', 'paid 才符合此例的付款确认条件。'], ['data.object.amount_total / metadata.order_id', '请求 JSON', '核对币种、金额和业务订单，保留原始请求体验签。']],
  },
  allscale: {
    headers: { 'X-API-Key': '<商户 key>', 'X-Webhook-Id': 'evt_allscale_example', 'X-Webhook-Timestamp': '<时间戳>',
      'X-Webhook-Nonce': '<随机串>', 'X-Webhook-Signature': '<签名>' },
    body: { webhook_id: 'evt_allscale_example', ...intent },
    fields: [['all_scale_checkout_intent_id', '请求 JSON · string', '和创建响应里的 allscale_checkout_intent_id 对应。'], ['status / amount_cents', '请求 JSON · integer', '状态 20 表示确认；核对冻结金额和币种。']],
  },
  moonpay: {
    headers: { 'Moonpay-Signature-V2': 't=<时间戳>,s=<签名>' },
    body: { type: 'transaction_updated', data: moonTransaction },
    fields: [['data.externalTransactionId', '请求 JSON · string', '用它匹配 Pay Lab 购买订单。'], ['data.status / cryptoTransactionId', '请求 JSON', 'completed 且具备数量、哈希，再核对币种和收款人。']],
  },
  alchemypay: {
    headers: { timestamp: '1893492000000' }, body: { ...alchemyTransaction, newSignature: '<签名>' },
    fields: [['newSignature', '请求 JSON · string', '校验当前支持的签名字段。'], ['merchantOrderNo / status', '请求 JSON', '关联本地订单；PAY_SUCCESS 仍待交付，FINISHED 要核对数量和哈希。']],
  },
  transak: {
    headers: {}, body: { data: '<已签名的 JWT>' }, decoded: { eventID: 'ORDER_UPDATE', webhookData: transakTransaction },
    fields: [['data', '请求 JSON · string', '外层只携带已签名 JWT；先通过 HS256 验证。'], ['webhookData.partnerOrderId / status', 'JWT claims', '核对本地订单关联、金额、收款人及 COMPLETED 交付状态。']],
  },
  ramp: {
    headers: { 'X-Body-Signature': '<Base64 DER ECDSA 签名>' }, body: { type: 'RELEASED', purchase: rampTransaction },
    fields: [['purchase.cryptoAmount / asset.decimals', '请求 JSON · string / integer', '29000000 ÷ 10⁶ = 29 USDC。'], ['purchase.id / finalTxHash', '请求 JSON · string', '核对绑定的供应商订单和交付哈希。']],
  },
};

function webhook(id, onramp, replay = false) {
  const sample = webhookSamples[id];
  const path = onramp ? '/onramp/webhooks/' + id : '/webhooks/' + id;
  const query = onramp && id !== 'moonpay' ? '?order_id=on_example&binding=<订单关联值>' : '';
  const example = contract(replay ? '重放已处理的供应商通知' : '接收并验证 Webhook', '支付通道 → Pay Lab（服务端）', 'POST', path + query,
    data({ ...json, ...sample.headers }, sample.body), id === 'wechat' ? '204 No Content' : '200 OK', id === 'wechat' ? null : received,
    sample.fields, replay ? '供应商通知可以重发，服务端对同一事件和账本结果去重；前端重复查询得到同一个到账金额。'
      : '响应表示接收通知。实际入账或交付结果从 Pay Lab 订单查询读取。');
  return { ...example, decoded: sample.decoded };
}

const querySamples = {
  wechat: ['/v3/pay/transactions/out-trade-no/{orderId}?mchid=<商户号>', { Authorization: 'WECHATPAY2-SHA256-RSA2048 <签名参数>' }, wechatTransaction, 'out_trade_no / trade_state', '验证查单响应签名，核对商户、订单、金额和 SUCCESS。'],
  stripe: ['/v1/checkout/sessions/{sessionId}', { Authorization: 'Basic <服务端凭据>' }, stripeSession, 'id / payment_status', '按同一个 Session 查单，并确认 paid。'],
  allscale: ['/v1/checkout_intents/{intentId}', { 'X-API-Key': '<商户 key>', 'X-Timestamp': '<时间戳>', 'X-Nonce': '<随机串>', 'X-Signature': '<签名>' }, { code: 0, payload: intent }, 'payload.status / amount_cents', '读取完整详情，核对冻结的金额和币种。'],
  moonpay: ['/v1/transactions/ext/{localOrderId}?apiKey=<发布 key>', {}, [moonTransaction], '[].externalTransactionId', '接口直接返回数组，仅在恰好一条且匹配原单时应用。'],
  alchemypay: ['/open/api/v4/merchant/query/trade?merchantOrderNo=on_example&side=BUY', { appid: '<商户 appId>', timestamp: '<毫秒时间戳>', sign: '<签名>' }, { data: alchemyTransaction }, 'merchantOrderNo / status', '验证原单、订单状态与交付凭证。'],
  transak: ['/partners/api/v2/order/{providerOrderId}', { 'x-api-key': '<商户 key>', 'access-token': '<服务端 Token>' }, { data: transakTransaction }, 'id / partnerOrderId', '使用已绑定的供应商订单 ID；本地订单号不能替代。'],
  ramp: ['/api/host-api/purchase/{purchaseId}?secret=<该订单查询凭证>', {}, rampTransaction, 'id / status / finalTxHash', '用 purchase.id 和订单专属查询凭证核验。'],
};

function providerQuery(id) {
  const [path, headers, response, field, explanation] = querySamples[id];
  return contract('主动查询供应商订单', 'Pay Lab → 支付通道（服务端）', 'GET', path,
    data(headers), '成功响应', response, [[field, '响应 JSON', explanation]], '把可信查询结果同步到本地订单。前端继续查询自己的订单状态。');
}

function bind(id) {
  const isRamp = id === 'ramp';
  const body = isRamp ? { provider_order_id: 'purchase_example', purchase_view_token: '<该订单查询凭证>' } : { provider_order_id: 'transak_example' };
  return contract('保存并验证买币订单引用', '前端 → Pay Lab', 'POST', '/onramp/orders/{id}/bind',
    data({ ...json, ...auth }, body), '200 OK', { ...onOrder(id, 'pending', 1), provider_order_id: body.provider_order_id },
    [['provider_order_id', '请求 JSON · string', '托管 SDK 生成的供应商订单引用，服务端必须先验证；不能单靠 SDK 事件判断支付。'],
      ...(isRamp ? [['purchase_view_token', '请求 JSON · string', 'Ramp 原购买单查询凭证，由后端存储；订单响应不返回。']] : [])],
    '持久化验证过的关联，后续补查使用这笔引用。');
}

function ledger() {
  return contract('读取点数账本', '前端 → Pay Lab', 'GET', '/me/ledger', data(auth), '200 OK',
    { entries: [{ order_id: topId, kind: 'credit', delta: 2000, created_at: timestamp }] },
    [['entries[].order_id', '响应 JSON · string', '匹配当前储值订单。'], ['entries[].delta', '响应 JSON · integer', '点数变更量。重复通知只对应一条有效 credit。']],
    '关联业务单和账本结果，刷新点数余额。买币交付属于钱包资产，不从这个接口增加点数。');
}

function recovery(id, scene, onramp) {
  const result = manualSync(id, onramp);
  return [providerQuery(id), ...(result ? [result] : []), userOrder(id, scene, onramp, onramp ? 'completed' : 'succeeded', 3)];
}

function manualSync(id, onramp) {
  if (!onramp) return null;
  return contract('对原买币单发起同步', '前端 → Pay Lab', 'POST', '/onramp/orders/{id}/sync', data(auth), '200 OK',
    onOrder(id, 'completed', 3), [['{id}', '路径参数 · string', '使用原本地订单 ID。Pay Lab 根据已验证绑定关系查供应商。'],
      ['status / tx_hash', '响应 JSON', '同步后仍以最新订单状态和交易哈希判断是否完成。']],
    '已绑定供应商引用时才可查单。缺引用可返回 WAITING_PROVIDER_REFERENCE，先等待或核对绑定。');
}

export function contractsFor(provider, scene, scenario, index, length, replayed) {
  const { id, onramp } = provider;
  const proofIndex = length - 2;
  if (index === 0) return [localOrder(id, scene, onramp), createConflict(id, scene, onramp)];
  if (index === 1) return [providerCheckout(id, scene), localOrder(id, scene, onramp)];
  if (index === length - 1) {
    return [
      userOrder(id, scene, onramp, onramp ? 'completed' : 'succeeded', 3),
      ...(!onramp ? [ledger()] : []),
      ...(scenario === 'duplicate' ? [webhook(id, onramp, replayed)] : []),
    ];
  }
  if (index === proofIndex) {
    const drop = scenario === 'missing';
    const result = drop ? recovery(id, scene, onramp) : [webhook(id, onramp)];
    if (onramp) result.push(demoAction(id, true, 'deliver', !drop));
    else if (!drop) result.push(demoAction(id, false, 'succeeded', true));
    return result;
  }
  if (onramp && index === 2) {
    return [demoAction(id, true, 'kyc', true), ...(['ramp', 'transak'].includes(id) ? [bind(id)] : [])];
  }
  const failed = scenario === 'failure';
  const action = onramp ? (failed ? 'fail' : 'pay') : failed ? 'failed' : 'succeeded';
  const deliver = onramp ? scenario !== 'missing' : failed && id === 'stripe';
  let status = 'pending';
  if (deliver) status = failed ? (id === 'ramp' ? 'cancelled' : 'failed') : 'processing';
  return [demoAction(id, onramp, action, deliver), userOrder(id, scene, onramp, status, failed ? 4 : 2)];
}
