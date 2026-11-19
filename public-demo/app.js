const providers = [
  { id: 'wechat', name: '微信支付', note: '模拟微信场景收银台，查看报价、服务端通知与平台点数的关系。' },
  { id: 'stripe', name: 'Stripe', note: '模拟托管收银台，区分银行卡付款完成和服务端确认入账。' },
  { id: 'allscale', name: 'Allscale', note: '模拟稳定币收银台，观察链上付款确认后平台点数怎样入账。' },
  { id: 'moonpay', name: 'MoonPay', onramp: true, note: '通过托管买币页演练身份确认、法币付款与钱包交付。' },
  { id: 'alchemypay', name: 'Alchemy Pay', onramp: true, note: '通过 Ramp Page 演练订单状态查询，并核对最终交付。' },
  { id: 'transak', name: 'Transak', onramp: true, note: '从一次性 Widget 会话开始，区分付款完成与供应商交付。' },
  { id: 'ramp', name: 'Ramp', onramp: true, note: '从托管页面发起购买，用订单状态跟踪支付和资产发送。' },
];

const topup = [
  ['创建订单', '等用户发起模拟储值', '创建演示订单'],
  ['完成付款', '等待收银台返回付款结果', '模拟付款成功'],
  ['确认入账', '服务端确认订单后发放点数', '模拟服务端确认'],
];
const onramp = [
  ['创建订单', '等用户发起模拟买币', '创建演示订单'],
  ['通过身份确认', '模拟供应商核验结果', '模拟通过'],
  ['完成付款', '等待供应商确认法币已付', '模拟付款成功'],
  ['确认交付', '服务端确认模拟钱包已到账', '模拟资产交付'],
];

const labels = ['未创建', '待处理', '已完成', '已失败'];
const byId = id => document.getElementById(id);
const advance = byId('advance');
const fail = byId('fail');
const reset = byId('reset');
let active = providers[0];
let index = 0;
let failed = false;

for (const item of providers) {
  const label = document.createElement('label');
  const input = document.createElement('input');
  input.type = 'radio';
  input.name = 'provider';
  input.value = item.id;
  input.checked = item === active;
  input.addEventListener('change', () => {
    active = item;
    startOver();
  });
  const name = document.createElement('span');
  name.textContent = item.name;
  label.append(input, name);
  byId(item.onramp ? 'onramp-options' : 'topup-options').append(label);
}

function startOver() {
  failed = false;
  index = 0;
  render();
}

function render() {
  const steps = active.onramp ? onramp : topup;
  const finished = index === steps.length;
  const asset = active.onramp ? '29 模拟 USDC' : '2,000 模拟点数';
  byId('category').textContent = active.onramp ? '法币买币' : '储值';
  byId('provider').textContent = active.name;
  byId('description').textContent = active.note;
  byId('guide').href = 'https://github.com/nemo-crypto/pay/blob/main/docs/providers/' + active.id + '.md';
  byId('amount').textContent = active.onramp ? '30 USD' : 'NT$1,000';
  byId('asset').textContent = asset;
  const status = byId('status');
  status.textContent = labels[failed ? 3 : finished ? 2 : index ? 1 : 0];
  status.dataset.state = failed ? 'failed' : finished ? 'done' : 'pending';
  byId('timeline').replaceChildren(...steps.map(([title, copy], position) => {
    const row = document.createElement('li');
    row.dataset.state = position < index ? 'done' : position === index ? (failed ? 'failed' : 'current') : 'waiting';
    if (position === index && !failed) row.setAttribute('aria-current', 'step');
    const heading = document.createElement('strong');
    heading.textContent = title;
    const summary = document.createElement('span');
    summary.textContent = position < index ? '已完成' : row.dataset.state === 'failed' ? '演练在这一步终止' : copy;
    row.append(heading, summary);
    return row;
  }));
  byId('feedback').textContent = failed ? '订单未完成，模拟资产没有变动。' : finished
    ? '服务端已确认，到账 ' + asset + '。'
    : index === steps.length - 1 ? '付款记录已出现，等待服务端确认结果。' : '';
  advance.hidden = failed || finished;
  advance.textContent = steps[index]?.[2] ?? '';
  fail.hidden = failed || finished || index === 0;
  reset.hidden = index === 0 && !failed;
}

advance.addEventListener('click', () => {
  const steps = active.onramp ? onramp : topup;
  if (!failed && index < steps.length) index++;
  render();
  if (advance.hidden) reset.focus();
});
fail.addEventListener('click', () => {
  failed = true;
  render();
  reset.focus();
});
reset.addEventListener('click', () => {
  startOver();
  advance.focus();
});
render();
