const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');

const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, '127.0.0.1', 'browser gate requires an isolated loopback server');
const screenshotDir = process.env.BALDA_ALIASES_SCREENSHOTS;
if (screenshotDir) fs.mkdirSync(screenshotDir, { recursive: true });

async function screenshot(page, name, viewport) {
  if (screenshotDir) await page.screenshot({ path: path.join(screenshotDir, `${name}-${viewport}.png`), fullPage: true });
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of [{ width: 1440, height: 900, name: 'desktop' }, { width: 390, height: 844, name: 'mobile' }]) {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      await page.goto(`${baseURL}/login`);
      await page.getByLabel(/^Username(?: \(required\))?$/).fill('administrator');
      await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct horse battery staple');
      await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);

      const name = `main_chat_${viewport.name}`;
      const inventory = await page.goto(`${baseURL}/aliases`);
      assert.equal(inventory.status(), 200);
      assert.equal(await page.locator('h1').textContent(), 'Aliases');
      await screenshot(page, 'inventory', viewport.name);
      await Promise.all([page.waitForURL(`${baseURL}/aliases?new=1`), page.getByRole('link', { name: 'Add alias' }).click()]);
      await assertPlaceholderDistinct(page.getByLabel('Alias name'), `${viewport.name} alias name`);
      await assertPlaceholderDistinct(page.getByLabel('Public locator'), `${viewport.name} alias locator`);
      await screenshot(page, 'create-empty', viewport.name);
      await page.getByLabel('Alias name').fill(name);
      await page.getByLabel('Public locator').fill('telegram:-1003953132277:0');
      await screenshot(page, 'create', viewport.name);
      await Promise.all([page.waitForURL(`${baseURL}/aliases/${name}`), page.getByRole('button', { name: 'Create alias' }).click()]);
      assert.equal(await page.getByLabel('Alias name').inputValue(), name);
      assert.equal(await page.locator('.app-sidebar [data-nav-link][href$="/aliases"]').getAttribute('aria-current'), 'page');
      await page.getByLabel('Public locator').fill('telegram:-1003953132278:0');
      await page.getByRole('button', { name: 'Save destination' }).click();
      await page.getByLabel('Public locator').waitFor();
      assert.equal(await page.getByLabel('Public locator').inputValue(), 'telegram:-1003953132278:0');
      await screenshot(page, 'detail', viewport.name);
      await page.getByLabel('Confirm deleting this alias.').check();
      await Promise.all([page.waitForURL(`${baseURL}/aliases`), page.getByRole('button', { name: 'Delete alias' }).click()]);
      assert.equal(await page.getByRole('link', { name, exact: true }).count(), 0);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${viewport.name} has no horizontal overflow`);
      assert.deepEqual(errors, [], `${viewport.name} browser errors`);
      await context.close();
    }
    console.log('Authenticated alias create, retarget and delete flow passed at desktop and mobile widths');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
