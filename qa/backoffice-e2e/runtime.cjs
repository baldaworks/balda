const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, '127.0.0.1', 'runtime harness requires isolated loopback server');
const password = 'correct horse battery staple'; // Isolated test account only.

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        const context = await browser.newContext({ viewport });
        const page = await context.newPage();
        const responses = [];
        page.on('request', request => {
          if (new URL(request.url()).pathname === '/auth/session/refresh') {
            responses.push({ event: 'request', method: request.method(), path: '/auth/session/refresh' });
          }
        });
        page.on('requestfailed', request => { responses.push({ event: 'failed', path: new URL(request.url()).pathname, error: request.failure()?.errorText }); });
        page.on('response', response => {
          const path = new URL(response.url()).pathname;
          if (!path.includes('/assets/')) responses.push({ path, status: response.status() });
        });
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
        const [rejected] = await Promise.all([
          page.waitForResponse(response => response.url() === `${baseURL}/access/users`),
          page.getByRole('button', { name: 'Create user', exact: true }).click({ timeout: 10000 }),
        ]);
        assert.equal(rejected.status(), 409);
        assert.equal((await rejected.text()).includes(password), false, 'server never reflects submitted password');
        await page.locator('#request-error:not([hidden])').waitFor();
        assert.equal(await page.locator('input[type="password"]').inputValue(), password);
        assert.equal((await page.content()).includes(`value="${password}"`), false, 'response never reflects password');
        await context.clearCookies({ name: 'balda_access' });
        try {
          await page.goto(`${baseURL}/account`);
          await page.waitForURL(`${baseURL}/account`);
          await page.locator('.viewer strong').waitFor();
        } catch (error) {
          const diagnostic = {
            viewport, path: new URL(page.url()).pathname,
            heading: await page.locator('h1').textContent(), responses,
            cookies: (await context.cookies()).map(cookie => ({ name: cookie.name, path: cookie.path })),
          };
          throw new Error(`Session restore failed: ${JSON.stringify(diagnostic)}`, { cause: error });
        }
        assert.equal(await page.locator('h1').textContent(), 'Account');
        await Promise.all([page.waitForURL(`${baseURL}/login`), page.getByRole('button', { name: 'Sign out', exact: true }).click()]);
        assert.equal((await context.cookies()).some(cookie => ['balda_access', 'balda_refresh'].includes(cookie.name)), false);
        await context.close();
    }
    console.log('Authenticated runtime browser: native/HTMX login, viewer isolation, conflict feedback, refresh and logout passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
