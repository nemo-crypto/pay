import { providers, wechatScenes } from './flows.js';
import { contractsFor } from './contracts.js';
import { showStepContracts } from './dev-panel.js';

const byId = id => document.getElementById(id);
const advance = byId('advance');
const replay = byId('replay');
const reset = byId('reset');
const scene = byId('wechat-scene');
const scenario = byId('scenario');
const runs = new Map();
let active = providers[0];
let inspected = null;

function setCopy(id, value) {
  byId(id).textContent = value;
}

for (const [id, item] of Object.entries(wechatScenes)) {
  scene.add(new Option(item.name, id));
}

for (const item of providers) {
  const label = document.createElement('label');
  const input = document.createElement('input');
  input.type = 'radio';
  input.name = 'provider';
  input.value = item.id;
  input.checked = item === active;
  input.addEventListener('change', () => {
    active = item;
    openRun();
  });
  const copy = document.createElement('span');
  const name = document.createElement('strong');
  const kind = document.createElement('small');
  name.textContent = item.name;
  kind.textContent = item.kind;
  copy.append(name, kind);
  label.append(input, copy);
  byId(item.onramp ? 'onramp-options' : 'topup-options').append(label);
}

function runKey() {
  return [active.id, active.id === 'wechat' ? scene.value : '', scenario.value].join(':');
}

function currentRun() {
  const key = runKey();
  if (!runs.has(key)) runs.set(key, { step: 0, failed: false, replayed: false, events: [] });
  return runs.get(key);
}

function openRun() {
  inspected = null;
  render();
}

function currentSteps() {
  return active.id === 'wechat' ? active.steps(scene.value) : active.steps;
}

function stepDetail(steps, index) {
  const step = steps[index];
  if (scenario.value === 'failure' && index === steps.length - 3) {
    return {
      ...step,
      title: '收到失败或未完成结果',
      action: '模拟付款失败',
      customer: '页面提示本次付款未完成，订单没有发放点数或交付记录。',
      server: '用供应商可信状态核对失败原因。遇到网络超时、结果仍未知时保留待确认，先查原单。此情形演练的是已明确失败的结果。',
      evidence: ['演练异常', '已确认失败 · 资产未发放'],
    };
  }
  if (scenario.value === 'missing' && index === steps.length - 2) {
    return {
      ...step,
      title: '通知未到，主动补查',
      action: '查询原订单',
      customer: '页面停在付款已提交、等待确认。可以刷新状态，不需要重新付款。',
      server: active.query,
      evidence: ['恢复方式', '已验证原订单查询结果'],
    };
  }
  return step;
}

function renderTimeline(steps, run, selected) {
  byId('timeline').replaceChildren(...steps.map((_, index) => {
    const row = document.createElement('li');
    const button = document.createElement('button');
    const title = document.createElement('strong');
    const state = document.createElement('small');
    button.type = 'button';
    row.dataset.state = index < run.step ? 'done' : index > run.step ? 'waiting' : run.failed ? 'failed' : 'current';
    button.setAttribute('aria-pressed', String(index === selected));
    if (row.dataset.state === 'current') button.setAttribute('aria-current', 'step');
    title.textContent = stepDetail(steps, index).title;
    state.textContent = { done: '已完成', waiting: '未执行', current: '当前步骤', failed: '失败' }[row.dataset.state];
    button.append(title, state);
    button.addEventListener('click', () => {
      inspected = index;
      render();
      byId('step-title').focus();
    });
    row.append(button);
    return row;
  }));
}

function renderEvents(run) {
  byId('event-empty').hidden = Boolean(run.events.length);
  byId('events').replaceChildren(...run.events.map(([key, value]) => {
    const row = document.createElement('li');
    const label = document.createElement('strong');
    const content = document.createElement('span');
    label.textContent = key;
    content.textContent = value;
    row.append(label, content);
    return row;
  }));
}

function render() {
  const run = currentRun();
  const steps = currentSteps();
  const finished = run.step === steps.length;
  const selected = inspected ?? Math.min(run.step, steps.length - 1);
  const previewing = inspected !== null && inspected !== run.step && !finished && !run.failed;
  const step = stepDetail(steps, selected);
  const asset = active.onramp ? '29 模拟 USDC' : '2,000 模拟点数';
  const paid = run.step > steps.length - 3 && !run.failed;
  const verified = run.step > steps.length - 2 && !run.failed;
  const payment = !paid ? (run.failed ? '付款失败' : '未确认付款') : verified ? '已核实付款' : '已提交付款';
  const progress = finished ? '已完成' : run.failed ? '已失败' : `${run.step + 1} / ${steps.length} 步`;
  byId('wechat-settings').hidden = active.id !== 'wechat';
  setCopy('category', active.onramp ? '法币买币' : '平台储值');
  setCopy('provider', active.name);
  setCopy('description', active.intro);
  setCopy('amount', active.quote ?? '30 USD → Ethereum USDC');
  setCopy('payment-state', payment);
  setCopy('asset', finished ? asset : active.onramp ? '尚未交付 USDC' : '尚未发放点数');
  setCopy('status', progress);
  byId('status').dataset.state = finished ? 'done' : run.failed ? 'failed' : 'pending';
  renderTimeline(steps, run, selected);
  setCopy('step-role', step.actor);
  setCopy('step-title', step.title);
  setCopy('step-user', step.customer);
  setCopy('step-server', step.server);
  setCopy('evidence-key', (selected < run.step || (run.failed && selected === run.step) ? '已记录 · ' : '预期证据 · ') + step.evidence[0]);
  setCopy('evidence-value', step.evidence[1]);

  let feedback = '';
  if (run.replayed) feedback = '相同通知已拦截，订单状态和到账金额保持不变。';
  else if (finished) feedback = '本次演练完成，到账 ' + asset + '。';
  else if (run.failed) feedback = '订单以失败结束，模拟资产没有变动。';
  else if (previewing) feedback = '正在查看步骤说明，尚未执行此步骤。';
  else if (run.step === steps.length - 2) feedback = '已有付款反馈，等待可核实的服务端结果。';
  setCopy('feedback', feedback);
  advance.textContent = previewing ? '回到当前步骤' : step.action;
  advance.hidden = finished || run.failed;
  replay.hidden = !finished || scenario.value !== 'duplicate' || run.replayed;
  reset.hidden = run.step === 0 && !run.failed;
  renderEvents(run);
  showStepContracts(
    [runKey(), selected, run.replayed].join(':'),
    step.title,
    contractsFor(active, scene.value, scenario.value, selected, steps.length, run.replayed),
  );
}

advance.addEventListener('click', () => {
  const run = currentRun();
  const steps = currentSteps();
  if (run.failed || run.step >= steps.length) return;
  if (inspected !== null && inspected !== run.step) {
    openRun();
    return;
  }
  const step = stepDetail(steps, run.step);
  run.events.push(step.evidence);
  if (scenario.value === 'failure' && run.step === steps.length - 3) run.failed = true;
  else run.step++;
  openRun();
  if (advance.hidden) (replay.hidden ? reset : replay).focus();
});
replay.addEventListener('click', () => {
  const run = currentRun();
  if (run.replayed || scenario.value !== 'duplicate' || run.step !== currentSteps().length) return;
  run.replayed = true;
  run.events.push(['重复通知', '识别到与已处理事件相同的通知，跳过本次入账']);
  render();
  reset.focus();
});
reset.addEventListener('click', () => {
  runs.delete(runKey());
  openRun();
  advance.focus();
});
scene.addEventListener('change', openRun);
scenario.addEventListener('change', openRun);
render();
