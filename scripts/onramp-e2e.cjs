const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
(async () => {
  const base = process.env.PAY_BASE_URL || "http://127.0.0.1:8098";
  fs.mkdirSync("test-results", { recursive: true });
  const browser = await chromium.launch({ headless: true });
  try {
    for (const [device, width, height] of [
      ["desktop", 1440, 1000],
      ["mobile", 390, 844],
    ]) {
      const context = await browser.newContext({ viewport: { width, height } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      for (const provider of ["moonpay", "alchemypay", "transak", "ramp"]) {
        await page.goto(base + "/onramp");
        await page.waitForFunction(() =>
          document.querySelector("#status").textContent.includes("第一笔"),
        );
        assert.equal(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
          true,
          "page overflow",
        );
        await page.locator(`[data-provider=${provider}]`).click();
        const guide = await context.request.get(base + "/guides/" + provider);
        assert.equal(guide.status(), 200);
        assert.match(await guide.text(), /第1步/);
        await page.locator("#create").click();
        await page.waitForURL("**/onramp?order_id=*");
        for (const action of ["kyc", "pay"]) {
          await page.locator(`[data-action=${action}]`).click();
        }
        if (device === "mobile") await page.locator("#drop").check();
        await page.locator("[data-action=deliver]").click();
        if (device === "mobile") {
          await page.waitForFunction(() =>
            document.querySelector("#status").textContent.includes("3/3"),
          );
          assert.doesNotMatch(
            await page.locator("#order-title").innerText(),
            /资产已交付/,
          );
          await page.locator("#sync").click();
        }
        await page.waitForFunction(() =>
          document
            .querySelector("#order-title")
            .textContent.includes("资产已交付"),
        );
        const result = JSON.parse(await page.locator("#result").innerText());
        assert.equal(result.status, "completed");
        assert.ok(result.tx_hash);
        assert.equal(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
          true,
          "order overflow",
        );
        await page.screenshot({
          path: `test-results/onramp-${device}-${provider}.png`,
          fullPage: true,
        });
      }
      await page.goto(base + "/onramp");
      await page.waitForFunction(() =>
        document.querySelector("#status").textContent.includes("第一笔"),
      );
      await page.screenshot({
        path: `test-results/onramp-${device}-home.png`,
        fullPage: true,
      });
      assert.deepEqual(errors, []);
      await context.close();
      console.log(
        `${device}: four on-ramp providers passed (mobile also tests dropped webhook recovery)`,
      );
    }
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
