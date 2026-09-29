const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, '127.0.0.1', 'runtime harness requires isolated loopback server');
const password = 'correct horse battery staple'; // Isolated test account only.

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      for (const javaScriptEnabled of [true, false]) {
        const context = await browser.newContext({ javaScriptEnabled, viewport });
        const page = await context.newPage();
        const login = await page.goto(`${baseURL}/login`);
        assert.equal(login.status(), 200, `login: ${await page.locator("body").innerText()}`);
        assert.equal(await page.locator("#username").count(), 1, `login title: ${await page.title()}`);
        await page.getByLabel(/^Username(?: \(required\))?$/).fill('administrator');
        await page.getByLabel(/^Password(?: \(required\))?$/).fill(password);
        await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
        assert.equal(await page.locator('.viewer strong').textContent(), 'administrator');
        await page.goto(`${baseURL}/access`);
        await page.getByRole('link', { name: 'operator', exact: true }).click();
        await page.waitForSelector('h2:text-is("Chat bindings")');
        assert.equal(await page.locator('.viewer strong').textContent(), 'administrator', 'viewer is independent of inspected user');
        await page.goto(`${baseURL}/access/new`);
        await page.getByLabel(/^Display name(?: \(required\))?$/).fill('Collision example');
        await page.getByLabel(/^Username(?: \(required\))?$/).fill('administrator');
        await page.getByLabel(/^Temporary password(?: \(required\))?$/).fill(password);
        if (!javaScriptEnabled) { await page.mouse.wheel(0, 800); await page.waitForTimeout(300); }
        const [rejected] = await Promise.all([
          page.waitForResponse(response => response.url() === `${baseURL}/access/users`),
          page.getByRole('button', { name: 'Create user', exact: true }).click({ timeout: 10000 }),
        ]);
        assert.equal(rejected.status(), 409);
        assert.equal((await rejected.text()).includes(password), false, 'server never reflects submitted password');
        if (javaScriptEnabled) await page.locator('#request-error:not([hidden])').waitFor();
        else await page.getByRole('alert').waitFor();
        if (javaScriptEnabled) assert.equal(await page.locator('input[type="password"]').inputValue(), password);
        else assert.equal(await page.locator('input[type="password"]').count(), 0);
        assert.equal((await page.content()).includes(`value="${password}"`), false, 'response never reflects password');
        await context.clearCookies({ name: 'balda_access' });
        const restored = await page.goto(`${baseURL}/account`);
        if (javaScriptEnabled) await page.waitForURL(`${baseURL}/account`);
        else {
          assert.equal(restored.status(), 401);
          await Promise.all([page.waitForURL(`${baseURL}/account`), page.getByRole('button', { name: 'Restore session' }).click()]);
        }
        await page.locator('.viewer strong').waitFor();
        assert.equal(await page.locator('h1').textContent(), 'Account');
        await Promise.all([page.waitForURL(`${baseURL}/login`), page.getByRole('button', { name: 'Sign out', exact: true }).click()]);
        assert.equal((await context.cookies()).some(cookie => ['balda_access', 'balda_refresh'].includes(cookie.name)), false);
        await context.close();
      }
    }
    console.log('Authenticated runtime browser: native/HTMX login, viewer isolation, conflict feedback, refresh and logout passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
