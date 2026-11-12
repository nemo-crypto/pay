// 全新 demo 服务运行后执行；五场景共10次写请求，避免和其他回归共用同一分钟限流窗口。
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const base = process.env.PAY_BASE_URL || "http://127.0.0.1:8098";
  const browser = await chromium.launch({ headless: true });
  fs.mkdirSync("test-results", { recursive: true });
  try {
    const scenes = ["mini", "jsapi", "h5", "native", "app"];
    for (const [i, scene] of scenes.entries()) {
      const context = await browser.newContext({ viewport: i % 2 ? { width: 390, height: 844 } : { width: 1440, height: 1000 } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.goto(`${base}/?gateway=wechat#topup`);
      await page.waitForFunction(() => document.querySelector("#balance").textContent !== "—");
      assert.match(await page.locator("#mode").innerText(), /本地演示/);
      await page.locator("#wechat-scene").selectOption(scene);
      assert.match(await page.locator("#wechat-quote").innerText(), /222\.23/);
      const before = Number((await page.locator("#balance").innerText()).replaceAll(",", ""));
      const created = page.waitForResponse((r) => r.url() === `${base}/topups` && r.request().method() === "POST");
      await page.locator("#checkout").click();
      const response = await created;
      assert.equal(response.status(), 201);
      const order = await response.json();
      assert.equal(order.gateway, "wechat");
      assert.equal(order.payment_currency, "CNY");
      assert.equal(order.payment_amount_minor, 22223);
      assert.equal(order.checkout.mode, scene);
      assert.ok(order.order_id.length <= 32);
      if (["mini", "jsapi", "app"].includes(scene)) {
        assert.equal(order.checkout.wechat.signType, "RSA");
        assert.ok(order.checkout.wechat.paySign);
      }
      await page.waitForURL("**/demo/checkout?order_id=*");
      if (scene === "native") {
        assert.match(order.checkout.code_url, /^weixin:\/\/wxpay\//);
        // 演示收银台仍测试内嵌的实际浏览器二维码模块。
        const canvasResult = await page.evaluate(async (code) => {
          const { default: QRCode } = await import("/vendor/qrcode.mjs");
          const canvas = document.createElement("canvas");
          await QRCode.toCanvas(canvas, code, { width: 256 });
          return { width: canvas.width, image: canvas.toDataURL() };
        }, order.checkout.code_url);
        assert.equal(canvasResult.width, 256);
        assert.match(canvasResult.image, /^data:image\/png;base64,/);
      }
      await page.locator("[data-outcome=succeeded]").click();
      await page.waitForFunction((expected) => Number(document.querySelector("#balance").textContent.replaceAll(",", "")) === expected, before + 2000);
      assert.match(await page.locator("#status").innerText(), /已到账/);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "horizontal overflow");
      await page.goto(`${base}/guides/wechat`);
      assert.match(await page.locator("h1").innerText(), /微信支付/);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "guide overflow");
      await page.screenshot({ path: `test-results/wechat-${scene}.png`, fullPage: true });
      assert.deepEqual(errors, []);
      await context.close();
      console.log(`WeChat ${scene}: CNY quote, payment, balance and guide checks passed`);
    }
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exitCode = 1; });
