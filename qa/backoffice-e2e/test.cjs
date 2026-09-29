const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const routes = [
  '', 'login', 'login-error', 'refresh', 'refresh-error', 'refresh-conflict',
  'password', 'password-error', 'overview', 'overview-empty', 'access-list',
  'access-create', 'access-empty', 'access-error', 'access', 'access-primary',
  'access-long', 'account', 'account-many', 'account-many-next',
  'account-session-states', 'account-empty', 'account-error', 'audit',
  'audit-empty', 'error',
  'form-bad-request', 'form-forbidden', 'form-conflict', 'form-server-error',
];
const expectedStatus = new Map([
  ['form-bad-request', 400], ['form-forbidden', 403],
  ['form-conflict', 409], ['form-server-error', 500],
]);

async function startPreview() {
  const server = spawn('go', ['run', './cmd/balda', 'backoffice', 'qa', 'serve', '--listen', '127.0.0.1:0'], {
    cwd: path.resolve(__dirname, '../..'),
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  let output = '';
  const url = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('QA preview did not start within 90 seconds')), 90000);
    server.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/Backoffice QA: (http:\/\/127\.0\.0\.1:\d+)\/qa\/ui\//);
      if (match) { clearTimeout(timeout); resolve(match[1]); }
    });
    server.stderr.on('data', chunk => { output += chunk; });
    server.once('exit', code => { clearTimeout(timeout); reject(new Error(`QA preview exited (${code}): ${output}`)); });
  });
  return { server, url };
}

async function checkPage(browser, baseURL, viewport) {
  const page = await browser.newPage({ viewport });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  for (const route of routes) {
    const response = await page.goto(`${baseURL}/qa/ui/${route}`);
    assert.equal(response.status(), expectedStatus.get(route) ?? 200, `${route} HTTP status at ${viewport.width}px`);
    assert.equal(await page.locator('main#main-content').count(), 1, `${route} main landmark at ${viewport.width}px`);
    const headingColors = await page.locator('h1, h2').evaluateAll(elements => elements.map(element => getComputedStyle(element).color));
    assert.equal(new Set(headingColors).size, 1, `${route} consistent heading colors at ${viewport.width}px`);
    assert.equal(await page.locator('h1').count(), 1, `${route} heading at ${viewport.width}px`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${route} document overflow at ${viewport.width}px`);
    assert.deepEqual(errors, [], `${route} script errors at ${viewport.width}px`);
    if (process.env.BACKOFFICE_E2E_SCREENSHOTS) {
      fs.mkdirSync(process.env.BACKOFFICE_E2E_SCREENSHOTS, { recursive: true });
      await page.screenshot({ path: path.join(process.env.BACKOFFICE_E2E_SCREENSHOTS, `${route || 'gallery'}-${viewport.width}.png`), fullPage: true });
    }
  }

  const nativeLayouts = new Map();
  const destinations = ['Overview', 'Account', 'Access', 'Audit'];
  async function layout() {
    return page.locator('main#main-content').evaluate(element => {
      const bounds = element.querySelector('.container-fluid').getBoundingClientRect();
      return { x: bounds.x, width: bounds.width, headingColor: getComputedStyle(element.querySelector('h1')).color };
    });
  }
  for (const name of destinations) {
    await page.goto(`${baseURL}/qa/ui/${name.toLowerCase()}`);
    nativeLayouts.set(name, await layout());
  }
  await page.goto(`${baseURL}/qa/ui/overview`);
  for (const name of ['Account', 'Access', 'Audit', 'Overview']) {
    const navigation = page.getByRole('navigation', { name: viewport.width < 992 ? 'Mobile navigation' : 'Primary navigation', exact: true });
    if (viewport.width < 992) await page.getByText('Menu', { exact: true }).click();
    const response = page.waitForResponse(response => response.url() === `${baseURL}/qa/ui/${name.toLowerCase()}`);
    await navigation.getByText(name, { exact: true }).click();
    await response;
    await page.waitForFunction(expected => document.querySelector('h1')?.textContent === expected, name);
    assert.equal(await page.locator('main#main-content').count(), 1, `${name} HTMX keeps one main landmark`);
    assert.deepEqual(await layout(), nativeLayouts.get(name), `${name} HTMX layout matches native navigation`);
    await page.waitForFunction(expected => [...document.querySelectorAll('[data-nav-link]')].filter(link => link.textContent.trim() === expected).every(link => link.getAttribute('aria-current') === 'page'), name);
  }

  await page.goto(`${baseURL}/qa/ui/audit`);
  assert.equal(await page.locator('section[aria-label="Security events"] table').count(), 1);
  assert.equal(await page.locator('section[aria-label="Security events"] tbody tr').count(), 3);
  assert.deepEqual(await page.locator('section[aria-label="Security events"] thead th').allTextContents(),
    ['Time (UTC)', 'Action', 'Actor', 'Target', 'Outcome', 'Details']);
  await page.getByText('Dates and actor ID').click();
  assert.equal(await page.locator('#audit-actor').isVisible(), true);
  await page.locator('section[aria-label="Security events"] summary').first().click();
  assert.equal(await page.getByText('Action code').first().isVisible(), true);

  await page.goto(`${baseURL}/qa/ui/refresh`);
  assert.match(await page.locator('main').evaluate(element => getComputedStyle(element).backgroundImage), /linear-gradient/);
  assert.equal(await page.getByRole('button', { name: 'Restore session' }).count(), 1);
  await page.goto(`${baseURL}/qa/ui/refresh-error`);
  assert.equal(await page.getByRole('link', { name: 'Sign in again' }).count(), 1);
  await page.goto(`${baseURL}/qa/ui/refresh-conflict`);
  assert.equal(await page.getByRole('link', { name: 'Reopen page' }).count(), 1);

  if (viewport.width < 576) {
    await page.goto(`${baseURL}/qa/ui/audit`);
    const table = page.locator('section[aria-label="Security events"] .table-responsive');
    assert.equal(await table.evaluate(element => element.scrollWidth > element.clientWidth), true);
    await table.evaluate(element => { element.scrollLeft = element.scrollWidth; });
    assert.equal(await page.getByText('Swipe the table sideways').isVisible(), true);
    await page.getByText('Menu', { exact: true }).click();
    assert.equal(await page.getByRole('navigation', { name: 'Mobile navigation' }).isVisible(), true);
  }
  await page.close();
}

(async () => {
  const { server, url } = await startPreview();
  try {
    const browser = await chromium.launch({ headless: true });
    try {
      for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 768, height: 1024 }, { width: 390, height: 844 }]) {
        await checkPage(browser, url, viewport);
      }
      const noScript = await browser.newPage({ javaScriptEnabled: false });
      await noScript.goto(`${url}/qa/ui/audit`);
      assert.equal(await noScript.locator('table tbody tr').count(), 3);
      await noScript.getByText('Dates and actor ID').click();
      assert.equal(await noScript.locator('#audit-actor').isVisible(), true);
      await noScript.close();
      console.log(`Backoffice E2E: ${routes.length} gallery pages and states at 390/768/1024/1440px, consistent heading colors, native/HTMX layout, audit, refresh, and navigation passed`);
    } finally { await browser.close(); }
  } finally { process.kill(-server.pid, 'SIGINT'); }
})().catch(error => { console.error(error); process.exitCode = 1; });
