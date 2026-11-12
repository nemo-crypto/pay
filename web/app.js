import QRCode from "/vendor/qrcode.mjs";

const $ = (id) => document.getElementById(id);
let token = "",
  plans = [],
  selected = "",
  pending = null,
  active = new URLSearchParams(location.search).get("order_id"),
  timer,
  mode,
  intent,
  wechatRate = "",
  wechatCheckout;
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
  $("wechat-checkout").hidden = true;
  updateWechatOptions();
}
const wechatHints = {
  native: "电脑网页展示本地生成的付款二维码，使用手机微信扫码完成付款。",
  h5: "适用于微信外的手机浏览器，将跳转微信收银台；微信内请选 JSAPI。",
  jsapi: "须在微信内打开，并先通过公众号网页授权绑定当前 AppID 的身份。",
  mini: "由小程序内 wx.requestPayment 调起；普通浏览器可查看接口返回参数。",
  app: "由原生 App 的微信 OpenSDK 调起；普通浏览器可查看接口返回参数。",
};
function updateWechatOptions() {
  const isWechat = document.querySelector("[name=gateway]:checked")?.value === "wechat";
  $("wechat-options").hidden = !isWechat;
  $("wechat-hint").textContent = wechatHints[$("wechat-scene").value];
  const plan = plans.find((p) => p.id === selected);
  if (isWechat && plan && wechatRate) {
    // 展示报价仅供确认；实际扣款以服务端冻结的分金额为准。
    const [whole, fraction = ""] = wechatRate.split(".");
    const scale = 10n ** BigInt(fraction.length);
    const rate = BigInt(whole) * scale + BigInt(fraction || "0");
    if (rate > 0n) {
      const numerator = BigInt(plan.nt_amount) * 100n * scale;
      const fen = (numerator + rate - 1n) / rate;
      $("wechat-quote").textContent = `预计人民币 ¥${fen / 100n}.${String(fen % 100n).padStart(2, "0")}，对应 NT$ ${plan.nt_amount} 储值；下单后冻结报价。`;
    }
  }
}
async function openCheckout(checkout) {
  if (checkout?.url) {
    if (checkout.mode === "h5" && mode !== "demo") {
      const link = document.createElement("a");
      link.href = checkout.url;
      link.referrerPolicy = "origin";
      document.body.append(link);
      link.click();
      link.remove();
    } else location.assign(checkout.url);
    return true;
  }
  if (checkout?.code_url || checkout?.wechat) {
    wechatCheckout = checkout;
    $("wechat-checkout").hidden = false;
    $("wechat-action").textContent = checkout.code_url
      ? "使用手机微信扫码付款，订单将在服务端确认后更新。"
      : wechatHints[checkout.mode];
    $("wechat-payload").textContent = checkout.code_url || JSON.stringify(checkout.wechat, null, 2);
    $("wechat-invoke").hidden = checkout.mode !== "jsapi";
    $("wechat-qr").hidden = !checkout.code_url;
    if (checkout.code_url) await QRCode.toCanvas($("wechat-qr"), checkout.code_url, { width: 256, errorCorrectionLevel: "M" });
    $("wechat-expiry").textContent = checkout.expires_at ? `付款参数有效至 ${new Date(checkout.expires_at).toLocaleTimeString()}` : "";
  }
  return false;
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
    addCell(tr, `NT$ ${o.nt_amount.toLocaleString()}${o.payment_currency === "CNY" ? ` / ¥${(o.payment_amount_minor / 100).toFixed(2)}` : ""}`);
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
    if (o.status !== "pending" || !o.checkout) $("wechat-checkout").hidden = true;
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
    wechatRate = data.wechat_twd_per_cny || "";
    for (const option of $("wechat-scene").options) option.disabled = !data.wechat_scenes?.includes(option.value);
    const suggested = /MicroMessenger/i.test(navigator.userAgent) ? "jsapi" : /Android|iPhone|iPad/i.test(navigator.userAgent) ? "h5" : "native";
    const scene = [...$("wechat-scene").options].find((o) => !o.disabled && o.value === suggested) || [...$("wechat-scene").options].find((o) => !o.disabled);
    if (scene) $("wechat-scene").value = scene.value;
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
    updateWechatOptions();
    if (mode === "demo") {
      token = (await api("/demo/session", { method: "POST" })).token;
    } else {
      $("login").hidden = false;
    }
    if (active) {
      $("catalog").hidden = true;
      $("plans").hidden = true;
      $("gateways").hidden = true;
      $("wechat-options").hidden = true;
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
      body: JSON.stringify({ plan_id: selected, gateway, ...(gateway === "wechat" ? { wechat_scene: $("wechat-scene").value } : {}) }),
    });
    active = o.order_id;
    if (await openCheckout(o.checkout)) return;
    message(o.replayed || o.reused ? "订单已存在，正在查询实际状态" : "订单已创建，请按所选场景完成付款");
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
    if (o.resumable && o.checkout) {
      active = o.order_id;
      if (await openCheckout(o.checkout)) return;
      await watch();
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
$("wechat-scene").onchange = resetIntent;
$("wechat-copy").onclick = async () => {
  try { await navigator.clipboard.writeText($("wechat-payload").textContent); message("付款参数已复制。"); }
  catch { message("复制失败，请手动选择付款参数复制。"); }
};
$("wechat-invoke").onclick = () => {
  if (!window.WeixinJSBridge || !wechatCheckout?.wechat) {
    message("请在微信内打开此页，并完成公众号身份绑定。");
    return;
  }
  window.WeixinJSBridge.invoke("getBrandWCPayRequest", wechatCheckout.wechat, () => {
    message("正在向服务端确认支付结果…");
    void watch();
  });
};
void boot();
