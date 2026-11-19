const byId = id => document.getElementById(id);
const show = (id, value) => { byId(id).textContent = value; };
const pretty = value => JSON.stringify(value, null, 2);
const picker = byId('api-choice');
let exchanges = [];
let lastContext;

function renderExchange() {
  const { direction, method, path, request, status, response, decoded, fields, next } = exchanges[picker.selectedIndex];
  show('api-direction', direction);
  show('api-method', method);
  show('api-path', path);
  show('api-status', status);
  const hasBody = request.body !== undefined;
  show('api-encoding', hasBody ? (request.encoding ?? 'JSON body') : '');
  show('api-request', hasBody ? pretty(request.body) : '无请求体');
  byId('api-headers').hidden = !Object.keys(request.headers).length;
  byId('api-headers').open = false;
  show('api-headers-body', pretty(request.headers));
  show('api-response', response === null ? '无 JSON 响应体' : pretty(response));
  byId('api-decoded').hidden = !decoded;
  byId('api-decoded').open = false;
  show('api-decoded-body', decoded ? pretty(decoded) : '');
  byId('api-fields').replaceChildren(...fields.map(([key, type, detail]) => {
    const row = document.createElement('div');
    const term = document.createElement('dt');
    const code = document.createElement('code');
    const info = document.createElement('dd');
    const source = document.createElement('small');
    code.textContent = key;
    source.textContent = type;
    term.append(code, source);
    info.textContent = detail;
    row.append(term, info);
    return row;
  }));
  show('api-next', next);
  show('copy-status', '');
  document.querySelector('[data-copy="api-request"]').hidden = !hasBody;
  document.querySelector('[data-copy="api-response"]').hidden = response === null;
}

export function showStepContracts(context, title, examples) {
  show('api-step', title);
  if (context === lastContext) return;
  lastContext = context;
  exchanges = examples;
  picker.replaceChildren(...examples.map(example => new Option(example.title)));
  byId('api-choice-label').hidden = examples.length < 2;
  renderExchange();
}

picker.addEventListener('change', renderExchange);
for (const button of document.querySelectorAll('[data-copy]')) {
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(byId(button.dataset.copy).textContent);
      show('copy-status', '已复制。');
    } catch {
      show('copy-status', '无法自动复制，可选中示例手动复制。');
    }
  });
}
