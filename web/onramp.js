const $ = (id) => document.getElementById(id);
const names = {
  moonpay: "MoonPay",
  alchemypay: "Alchemy Pay",
  transak: "Transak",
  ramp: "Ramp",
};
const labels = {
  pending: "等待付款",
  processing: "处理中 / 等待资产交付",
  completed: "资产已交付",
  failed: "失败",
  cancelled: "取消或过期",
  refunded: "已退回",
  review: "需要人工核对",
};
let token = "",
  selected = "moonpay",
  mode = "",
  intent = null,
  active = new URLSearchParams(location.search).get("order_id"),
  timer;
async function api(path, options = {}) {
  const headers = { ...options.headers };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (options.body) headers["Content-Type"] = "application/json";
  const r = await fetch(path, { ...options, headers });
  const b = await r.json();
  if (!r.ok) throw new Error(b.code || b.message || `HTTP ${r.status}`);
  return b;
}
function message(s) {
  $("status").textContent = s;
}
function quote() {
  intent = null;
  const amount = Number($("amount").value);
  $("quote").textContent =
    mode === "demo"
      ? `模拟报价：${amount.toFixed(2)} USD，虚拟手续费 1 USD，预计 ${(
          amount - 1
        ).toFixed(2)} USDC。此费率仅用于教学，地址是演示占位符。`
      : "服务商收银台给出最终报价、手续费及可用支付方式。这里只固定本地购买意图；供应商返回金额不符时转人工核对。";
}
async function history() {
  if (!token) return;
  const { orders } = await api("/onramp/orders");
  $("history").replaceChildren();
  for (const o of orders) {
    const tr = document.createElement("tr");
    for (const text of [
      o.id.slice(0, 13) + "…",
      names[o.provider],
      `${(o.fiat_amount_minor / 100).toFixed(2)} USD`,
      labels[o.status] || o.status,
      o.crypto_amount ? `${o.crypto_amount} USDC` : "—",
    ]) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.append(td);
    }
    const a = document.createElement("a");
    a.href = `/onramp?order_id=${encodeURIComponent(o.id)}`;
    a.textContent = tr.firstChild.textContent;
    tr.firstChild.replaceChildren(a);
    $("history").append(tr);
  }
}
function render(o) {
  $("purchase").hidden = true;
  $("order").hidden = false;
  $("order-title").textContent = `${names[o.provider]} · ${
    labels[o.status] || o.status
  }`;
  $("order-id").textContent = o.id;
  $("order-details").textContent = `${(o.fiat_amount_minor / 100).toFixed(
    2,
  )} USD → USDC / Ethereum · ${o.wallet_address}`;
  $("result").textContent = JSON.stringify(
    {
      status: o.status,
      detail: o.detail,
      provider_order_id: o.provider_order_id || null,
      crypto_amount: o.crypto_amount || null,
      tx_hash: o.tx_hash || null,
    },
    null,
    2,
  );
  $("demo").hidden = mode !== "demo";
  $("bind-box").hidden = mode === "demo" || Boolean(o.provider_order_id);
  const terminal = [
    "completed",
    "failed",
    "cancelled",
    "refunded",
    "review",
  ].includes(o.status);
  document.querySelectorAll("[data-action]").forEach((b) => {
    const stage = { kyc: 0, pay: 1, deliver: 2 }[b.dataset.action];
    b.disabled =
      terminal ||
      o.demo_stage >= 3 ||
      (stage !== undefined && stage !== o.demo_stage);
  });
  $("checkout-link").hidden = mode === "demo" || !o.checkout_url;
  if (o.checkout_url) $("checkout-link").href = o.checkout_url;
  message(
    `${labels[o.status]}${
      mode === "demo" ? " · 演示步骤 " + o.demo_stage + "/3" : ""
    }`,
  );
  return terminal;
}
async function watch() {
  clearTimeout(timer);
  if (!active || !token) return;
  try {
    const o = await api(`/onramp/orders/${encodeURIComponent(active)}`);
    const done = render(o);
    await history();
    if (!done) timer = setTimeout(watch, 3000);
  } catch (e) {
    message(e.message);
  }
}
$("amount").oninput = quote;
$("wallet").oninput = () => {
  intent = null;
};
$("create").onclick = async () => {
  if (!token) {
    message("请先连接账户");
    return;
  }
  $("create").disabled = true;
  try {
    const amount = Number($("amount").value);
    if (!Number.isInteger(amount) || amount < 30 || amount > 1000)
      throw new Error("请输入30–1000整数美元");
    intent ||= crypto.randomUUID();
    const o = await api("/onramp/orders", {
      method: "POST",
      headers: { "Idempotency-Key": intent },
      body: JSON.stringify({
        provider: selected,
        wallet_address: $("wallet").value,
        fiat_amount_minor: amount * 100,
      }),
    });
    active = o.id;
    window.history.replaceState(
      null,
      "",
      `/onramp?order_id=${encodeURIComponent(active)}`,
    );
    render(o);
    await watch();
  } catch (e) {
    message(e.message + "；网络异常重试会复用原幂等键。");
  } finally {
    $("create").disabled = false;
  }
};
document.querySelectorAll("[data-action]").forEach(
  (b) =>
    (b.onclick = async () => {
      document
        .querySelectorAll("[data-action]")
        .forEach((x) => (x.disabled = true));
      try {
        const o = await api(`/onramp/demo/${active}/advance`, {
          method: "POST",
          body: JSON.stringify({
            action: b.dataset.action,
            deliver_webhook: !$("drop").checked,
          }),
        });
        render(o);
        await watch();
      } catch (e) {
        message(e.message);
        await watch();
      }
    }),
);
$("sync").onclick = async () => {
  try {
    const o = await api(`/onramp/orders/${active}/sync`, { method: "POST" });
    render(o);
    await watch();
  } catch (e) {
    message(e.message);
  }
};
$("bind").onclick = async () => {
  try {
    await api(`/onramp/orders/${active}/bind`, {
      method: "POST",
      body: JSON.stringify({
        provider_order_id: $("provider-ref").value,
        purchase_view_token: $("view-token").value,
      }),
    });
    $("view-token").value = "";
    await watch();
  } catch (e) {
    message(e.message);
  }
};
$("authenticate").onclick = async () => {
  token = $("token").value;
  $("token").value = "";
  try {
    await history();
    await watch();
  } catch (e) {
    message(e.message);
  }
};
async function boot() {
  try {
    const c = await api("/onramp/providers");
    mode = c.mode;
    $("mode").textContent =
      mode === "demo"
        ? "本地 mock · 不扣款"
        : "真实网关 · " +
          (c.providers.every((p) => p.sandbox) ? "Sandbox" : "含 Production");
    selected = c.providers[0]?.id;
    for (const p of c.providers) {
      const b = document.createElement("button");
      b.className = "plan";
      b.textContent = names[p.id];
      b.dataset.provider = p.id;
      b.setAttribute("aria-pressed", String(p.id === selected));
      b.onclick = () => {
        selected = p.id;
        intent = null;
        document
          .querySelectorAll("[data-provider]")
          .forEach((x) => x.setAttribute("aria-pressed", String(x === b)));
      };
      $("providers").append(b);
      const a = document.createElement("a");
      a.href = p.guide;
      a.textContent = `${names[p.id]} · 完整教程 ↗`;
      a.target = "_blank";
      a.rel = "noopener";
      $("guides").append(a);
    }
    if (!selected) {
      $("create").disabled = true;
      message("尚未启用 on-ramp 通道，请按文档配置。");
    }
    if (mode === "demo") {
      token = (await api("/demo/session", { method: "POST" })).token;
      $("wallet").value = "0x1111111111111111111111111111111111111111";
    } else {
      $("login").hidden = false;
    }
    quote();
    await history();
    if (active) await watch();
    else if (selected) message("选择服务商，创建你的第一笔买币演示订单。");
  } catch (e) {
    message(e.message);
  }
}
window.addEventListener("pagehide", () => clearTimeout(timer));
void boot();
