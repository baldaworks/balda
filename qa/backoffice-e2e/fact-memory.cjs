const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const origin = process.argv[2];
const base = process.argv[3] || '';
const width = Number(process.argv[4]);
const url = `${origin}${base}`;
assert.equal(new URL(origin).hostname, '127.0.0.1', 'isolated loopback only');
const password = 'correct horse battery staple';
const imported = 'Synthetic imported memory:\n  Preserve this whole entry.\nSecond paragraph stays together.';
const input = 'Synthetic UI fact:\n<script>window.factBrowserExecuted = true</script>\n  Keep indentation.';
const factLabel = /^Fact(?: \(required\))?$/;

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 900 }, javaScriptEnabled: true });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('requestfailed', request => errors.push(`${new URL(request.url()).pathname}: ${request.failure()?.errorText}`));
    const screenDir = path.join(process.env.BALDA_FACT_MEMORY_APPLICATION_SCREENSHOTS || '/tmp/balda-fact-memory-screens/application', base ? 'balda-backoffice' : 'root', `${width}`);
    fs.mkdirSync(screenDir, { recursive: true });
    async function screenshot(state) {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${state} has no overflow`);
      await page.screenshot({ path: path.join(screenDir, `${state}.png`), fullPage: true });
    }
    async function fixture(name) {
      const response = await context.request.post(`${origin}/__fact_test/${name}`);
      assert.equal(response.status(), 200, `fixture ${name}`);
    }
    async function snapshot() {
      const response = await context.request.get(`${origin}/__fact_test/snapshot`);
      assert.equal(response.status(), 200);
      return response.json();
    }
    async function version(expected) { assert.equal((await snapshot()).version, expected, 'durable version'); }
    async function login(target, username) {
      await target.goto(`${url}/login`);
      await target.getByLabel(/^Username(?: \(required\))?$/).fill(username);
      await target.getByLabel(/^Password(?: \(required\))?$/).fill(password);
      await Promise.all([target.waitForURL(`${url}/overview`), target.getByRole('button', { name: 'Sign in', exact: true }).click()]);
    }
    async function post(route, form, expectedStatus, extraHeaders = {}) {
      const csrf = (await context.cookies()).find(cookie => cookie.name === 'balda_csrf')?.value;
      assert.ok(csrf, 'ordinary browser CSRF cookie');
      const response = await context.request.post(`${url}${route}`, { form: { csrf_token: csrf, ...form }, headers: { Origin: origin, ...extraHeaders }, maxRedirects: 0 });
      assert.equal(response.status(), expectedStatus, `${route} response`);
      return response;
    }
    await login(page, 'administrator');
    await page.goto(`${url}/memory`);
    assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Fact', 'Actions']);
    assert.equal(await page.locator('.fact-text-value').count(), 1, 'whole imported file is one fact');
    assert.equal(await page.locator('.fact-text-value').textContent(), imported);
    await version(1);
    await screenshot('imported');

    await page.getByRole('link', { name: 'Add fact', exact: true }).click();
    await page.getByLabel(factLabel).fill('cancelled synthetic draft');
    await page.getByRole('link', { name: 'Cancel', exact: true }).click();
    await page.getByRole('heading', { name: 'Fact memory', exact: true }).waitFor();
    await version(1);
    const blank = await post('/memory', { fact: ' \n ' }, 400);
    assert.match(await blank.text(), /Fact/);
    await version(1);
    await page.getByRole('link', { name: 'Add fact', exact: true }).click();
    await page.getByLabel(factLabel).fill(input);
    await Promise.all([page.waitForURL(`${url}/memory`), page.getByRole('button', { name: 'Create fact' }).click()]);
    await version(2);
    assert.equal(await page.evaluate(() => window.factBrowserExecuted), undefined, 'fact escaped');
    assert.equal(await page.locator('.fact-text-value').last().textContent(), input);
    await screenshot('created');
    await page.getByLabel('Search facts').fill('UI FACT');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await page.waitForURL(/\?q=UI\+FACT|\?q=UI%20FACT/);
    assert.equal(await page.locator('.fact-text-value').count(), 1);
    await page.getByLabel('Search facts').fill('no synthetic matches');
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await page.getByText('No facts match your search.', { exact: true }).waitFor();
    await screenshot('no-match');
    await page.getByRole('link', { name: 'Clear', exact: true }).click();
    await page.locator('.fact-text-value').nth(1).waitFor();

    await page.goto(`${url}/memory/1`);
    const oldVersion = await page.locator('input[name="expected_version"]').first().inputValue();
    assert.equal(oldVersion, '2');
    await page.getByLabel(factLabel).fill('browser draft after concurrent writers');
    await fixture('mcp');
    await fixture('actor');
    const concurrent = await snapshot();
    assert.equal(concurrent.version, 4);
    assert.deepEqual(concurrent.entries.slice(2).map(entry => entry.fact), ['Synthetic MCP accepted fact', 'Synthetic actor accepted fact']);
    const [conflict] = await Promise.all([page.waitForResponse(response => response.url() === `${url}/memory/1` && response.request().method() === 'POST'), page.getByRole('button', { name: 'Save fact' }).click()]);
    assert.equal(conflict.status(), 409);
    await page.getByText(/Fact memory changed/).first().waitFor();
    assert.equal(await page.getByLabel(factLabel).inputValue(), 'browser draft after concurrent writers');
    assert.equal(await page.locator('input[name="expected_version"]').first().inputValue(), oldVersion);
    await version(4);
    await screenshot('conflict');
    await page.getByRole('link', { name: 'Reopen this fact', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('input[name="expected_version"]')?.value === '4');
    await page.waitForFunction(() => document.activeElement?.id === 'main-content');
    assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Fact memory'], 'settled nested editor keeps active navigation');
    await page.getByLabel(factLabel).fill('saved replacement');
    await page.getByRole('button', { name: 'Save fact' }).click();
    await page.getByRole('heading', { name: 'Fact memory', exact: true }).waitFor();
    await version(5);
    assert.equal((await snapshot()).entries[0].fact, 'saved replacement');
    await post('/memory/1/delete', { expected_version: '5' }, 400);
    await version(5);
    await post('/memory', { fact: 'denied fact', csrf_token: 'wrong' }, 403);
    await post('/memory', { fact: 'denied fact' }, 403, { Origin: 'https://foreign.example.test' });
    await version(5);

    const operatorContext = await browser.newContext({ viewport: { width, height: 844 } });
    const operator = await operatorContext.newPage();
    await login(operator, 'operator');
    assert.equal(await operator.locator('.app-sidebar a[href$="/memory"]').count(), 0);
    assert.equal((await operator.goto(`${url}/memory`)).status(), 403);
    const operatorCSRF = (await operatorContext.cookies()).find(cookie => cookie.name === 'balda_csrf')?.value;
    const denied = await operatorContext.request.post(`${url}/memory`, { form: { fact: 'denied fact', csrf_token: operatorCSRF }, headers: { Origin: origin }, maxRedirects: 0 });
    assert.equal(denied.status(), 403);
    await version(5);
    await operatorContext.close();

    await page.goto(`${url}/memory/1`);
    await page.getByLabel('Confirm deleting this fact.').check();
    await page.getByRole('button', { name: 'Delete fact', exact: true }).click();
    await page.getByRole('heading', { name: 'Fact memory', exact: true }).waitFor();
    await version(6);
    for (const [id, current] of [[2, 6], [3, 7], [4, 8]]) {
      await post(`/memory/${id}/delete`, { expected_version: String(current), confirm: 'yes' }, 303);
    }
    await page.goto(`${url}/memory`);
    await page.getByText('No facts yet.', { exact: true }).waitFor();
    await version(9);
    assert.equal((await snapshot()).entries.length, 0);
    await screenshot('empty');
    await fixture('unavailable');
    await page.goto(`${url}/memory?new=1`);
    await page.getByLabel(factLabel).fill('Synthetic safe unavailable draft');
    const [failure] = await Promise.all([page.waitForResponse(response => response.url() === `${url}/memory` && response.request().method() === 'POST'), page.getByRole('button', { name: 'Create fact' }).click()]);
    assert.equal(failure.status(), 503);
    await page.getByLabel(factLabel).waitFor();
    assert.equal(await page.getByLabel(factLabel).inputValue(), 'Synthetic safe unavailable draft');
    await screenshot('unavailable');
    await version(9);
    await fixture('disable-memory');
    await page.goto(`${url}/memory?new=1`);
    assert.equal(await page.locator('main textarea,main input,main button').count(), 0);
    await post('/memory', { fact: 'disabled fact' }, 403);
    await version(9);
    await screenshot('disabled');
    await fixture('enabled');
    await fixture('revoke');
    const validAdminCSRF = (await context.cookies()).find(cookie => cookie.name === 'balda_csrf')?.value;
    assert.ok(validAdminCSRF, 'revoked request retains valid CSRF proof');
    const revoked = await context.request.post(`${url}/memory`, { form: { fact: 'denied fact', csrf_token: validAdminCSRF }, headers: { Origin: origin }, maxRedirects: 0 });
    assert.ok([401, 403].includes(revoked.status()), 'revoked authority cannot mutate');
    await version(9);
    assert.deepEqual(errors, [], 'no application browser failures');
    await context.close();
    console.log(`PASS application ${base || '/'} ${width}: real login/global CRUD/search/import/MCP/actor/conflict/denials/empty/failures; screens ${screenDir}`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
