const $ = (id) => document.getElementById(id);
let token = "",
  plans = [],
  selected = "",
  pending = null,
  active = new URLSearchParams(location.search).get("order_id"),
  timer,
  mode,
  intent;
const labels = {
  pending: "等待支付确认",
  succeeded: "已到账",
  failed: "付款失败",
  expired: "已过期",
  flagged: "需要人工复核",
  refunded: "已退款",
};
async function api(path, options = {}) {
  const headers = { ...options.headers };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (options.body) headers["Content-Type"] = "application/json";
  const r = await fetch(path, { ...options, headers });
  if (r.status === 204) return null;
  const body = await r.json();
  if (!r.ok)
    throw new Error(`${body.message || body.code} (${body.code || r.status})`);
  return body;
}
function message(text) {
  $("status").textContent = text;
}
function buttonLock(value) {
  $("checkout").disabled = value;
  document
    .querySelectorAll("[data-outcome]")
    .forEach((b) => (b.disabled = value));
}
function resetIntent() {
  intent = null;
}
function addCell(row, text) {
  const td = document.createElement("td");
  td.textContent = text;
  row.append(td);
}
async function refresh() {
  if (!token) return;
  const [me, data] = await Promise.all([api("/me"), api("/topups")]);
  $("balance").textContent = me.coins.toLocaleString();
  $("orders").replaceChildren();
  for (const o of data.topups) {
    const tr = document.createElement("tr");
    addCell(tr, o.id.slice(0, 14) + "…");
    addCell(tr, o.gateway);
    addCell(tr, `NT$ ${o.nt_amount.toLocaleString()}`);
    addCell(tr, labels[o.status] || o.status);
    addCell(tr, o.total_credited.toLocaleString());
    $("orders").append(tr);
  }
  if (!data.topups.length) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 5;
    td.textContent = "还没有储值记录";
    tr.append(td);
    $("orders").append(tr);
  }
  pending = await api("/topups/pending");
  $("recovery").hidden = !pending || Boolean(active);
  if (pending)
    $("pending-text").textContent =
      `你有一笔 NT$ ${pending.nt_amount} 的储值尚未完成`;
}
async function watch() {
  clearTimeout(timer);
  if (!active) return;
  try {
    const o = await api(`/topups/${encodeURIComponent(active)}`);
    message(
      `${labels[o.status] || o.status} · ${o.id}${
        o.status_detail ? " · " + o.status_detail : ""
      }`,
    );
    if (
      o.status === "succeeded" ||
      o.status === "refunded" ||
      o.status === "flagged" ||
      o.status === "failed" ||
      o.status === "expired"
    ) {
      await refresh();
      return;
    }
    await refresh();
    timer = setTimeout(watch, 3000);
  } catch (e) {
    message(e.message + "；可手动刷新重试");
  }
}
async function boot() {
  try {
    const data = await api("/topup/plans");
    mode = data.mode;
    $("mode").textContent = mode === "demo" ? "本地演示 · 不扣款" : "网关联调";
    plans = data.plans;
    selected = plans[0]?.id;
    for (const p of plans) {
      const b = document.createElement("button");
      b.className = "plan";
      b.setAttribute("aria-pressed", String(p.id === selected));
      const price = document.createElement("strong");
      price.textContent = `NT$ ${p.nt_amount.toLocaleString()}`;
      const coins = document.createElement("span");
      coins.textContent = `${p.coin_amount.toLocaleString()} 点`;
      b.append(price, coins);
      b.onclick = () => {
        selected = p.id;
        resetIntent();
        document
          .querySelectorAll(".plan")
          .forEach((x) => x.setAttribute("aria-pressed", String(x === b)));
      };
      $("plans").append(b);
    }
    document.querySelectorAll("[name=gateway]").forEach((x) => {
      x.disabled = !data.gateways.includes(x.value);
      x.onchange = resetIntent;
    });
    const requested = new URLSearchParams(location.search).get("gateway");
    const available =
      [...document.querySelectorAll("[name=gateway]")].find(
        (x) => !x.disabled && x.value === requested,
      ) || document.querySelector("[name=gateway]:not(:disabled)");
    if (available) available.checked = true;
    else $("checkout").disabled = true;
    if (mode === "demo") {
      token = (await api("/demo/session", { method: "POST" })).token;
    } else {
      $("login").hidden = false;
    }
    if (active) {
      $("catalog").hidden = true;
      $("plans").hidden = true;
      $("gateways").hidden = true;
      $("checkout").hidden = true;
      $("title").textContent = "确认本次储值";
      $("simulation").hidden =
        mode !== "demo" || location.pathname !== "/demo/checkout";
      if (token) await watch();
    } else message("选择方案和付款方式，开始储值。");
    await refresh();
  } catch (e) {
    message(e.message);
  }
}
$("checkout").onclick = async () => {
  buttonLock(true);
  try {
    const gateway = document.querySelector("[name=gateway]:checked").value;
    if (!intent) intent = crypto.randomUUID();
    const o = await api("/topups", {
      method: "POST",
      headers: { "Idempotency-Key": intent },
      body: JSON.stringify({ plan_id: selected, gateway }),
    });
    if (o.checkout?.url) {
      location.assign(o.checkout.url);
      return;
    }
    active = o.order_id;
    message("订单已存在，正在查询实际状态");
    await watch();
  } catch (e) {
    message(e.message + "；结果未知时请重试原购买意图。");
  } finally {
    buttonLock(false);
  }
};
$("refresh").onclick = async () => {
  try {
    await refresh();
    if (active) await watch();
  } catch (e) {
    message(e.message);
  }
};
$("authenticate").onclick = async () => {
  token = $("token").value;
  $("token").value = "";
  try {
    await refresh();
    if (active) await watch();
  } catch (e) {
    message(e.message);
  }
};
$("resume").onclick = async () => {
  if (!pending) return;
  try {
    const o = await api(`/topups/${pending.order_id}/resume`, {
      method: "POST",
    });
    if (o.resumable && o.checkout?.url) {
      location.assign(o.checkout.url);
      return;
    }
    message(
      `当前订单${labels[o.status] || o.status}，请根据结果返回方案选择。`,
    );
    await refresh();
  } catch (e) {
    message(e.message);
  }
};
$("abandon").onclick = async () => {
  if (!pending) return;
  try {
    await api(`/topups/${pending.order_id}/cancel-hint`, { method: "POST" });
    resetIntent();
    await refresh();
    message("已隐藏未完成提示；这不会撤销网关付款，后续真实付款仍会核对入账。");
  } catch (e) {
    message(e.message);
  }
};
document.querySelectorAll("[data-outcome]").forEach(
  (b) =>
    (b.onclick = async () => {
      buttonLock(true);
      try {
        await api(`/demo/orders/${active}/pay`, {
          method: "POST",
          body: JSON.stringify({
            outcome: b.dataset.outcome,
            deliver_webhook: !$("drop-webhook").checked,
          }),
        });
        await watch();
      } catch (e) {
        message(e.message);
      } finally {
        buttonLock(false);
      }
    }),
);
window.addEventListener("pagehide", () => clearTimeout(timer));
void boot();
