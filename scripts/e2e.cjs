// 启动全新 demo 数据库后运行：npm ci && npx playwright install chromium && npm run test:e2e
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
(async () => {
  fs.mkdirSync("test-results", { recursive: true });
  const browser = await chromium.launch({ headless: true });
  try {
    for (const [name, width, height] of [
      ["desktop", 1440, 1000],
      ["mobile", 390, 844],
    ]) {
      const context = await browser.newContext({ viewport: { width, height } });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      for (const gateway of ["stripe", "allscale"]) {
        await page.goto(
          (process.env.PAY_BASE_URL || "http://127.0.0.1:8098") +
            `/?gateway=${gateway}`,
        );
        await page.waitForFunction(
          () => document.querySelector("#balance").textContent !== "—",
        );
        const before = Number(
          (await page.locator("#balance").innerText()).replaceAll(",", ""),
        );
        assert.equal(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
          true,
          "horizontal overflow",
        );
        assert.equal(await page.locator(".provider-card").count(), 7);
        assert.equal(
          await page.locator(`[name=gateway][value=${gateway}]`).isChecked(),
          true,
        );
        await page.locator("#checkout").click();
        await page.waitForURL("**/demo/checkout?order_id=*");
        await page.locator("[data-outcome=succeeded]").click();
        await page.waitForFunction(
          (expected) =>
            Number(
              document
                .querySelector("#balance")
                .textContent.replaceAll(",", ""),
            ) === expected,
          before + 2000,
        );
        assert.match(await page.locator("#status").innerText(), /已到账/);
        await page.screenshot({
          path: `test-results/${name}-${gateway}.png`,
          fullPage: true,
        });
      }
      assert.deepEqual(errors, []);
      await page.goto(process.env.PAY_BASE_URL || "http://127.0.0.1:8098");
      await page.waitForFunction(
        () => document.querySelector("#balance").textContent !== "—",
      );
      await page.screenshot({
        path: `test-results/${name}-home.png`,
        fullPage: true,
      });
      await context.close();
      console.log(
        `${name}: Stripe + Allscale payment and balance checks passed`,
      );
    }
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
