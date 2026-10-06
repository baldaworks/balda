const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const routes = [
 'mcp-oauth-return',
 'mcp-authorizing', 'mcp-device-issued', 'mcp-device-pending', 'mcp-device-authorized', 'mcp-device-denied', 'mcp-device-expired', 'mcp-device-failed', 'mcp-authorization-unavailable', 'mcp-authorization-retry',
  'mcp-public', 'mcp-auth-required', 'mcp-revoked', 'mcp-stdio', 'mcp-saved-start-failed', 'mcp-retained', 'mcp', 'mcp-empty', 'mcp/new', 'mcp/connections/qa-worker', 'mcp/connections/config:qa-worker', 'mcp-probe', 'mcp-invalid', 'mcp-conflict', 'mcp-unavailable',
  '', 'style-guide', 'layout', 'layout-long', 'login', 'login-error', 'refresh', 'refresh-error', 'refresh-conflict',
  'password', 'password-error', 'overview', 'overview-empty', 'access-list',
  'access-create', 'access-empty', 'access-error', 'access', 'access-primary',
  'access-long', 'bindings-issued', 'bindings-pending', 'bindings-unavailable', 'bindings-disabled', 'account', 'account-many', 'account-many-next',
  'account-2fa-off', 'account-2fa-enabled', 'account-2fa-unavailable', 'webauthn-register', 'webauthn-assert', 'step-up', 'account-session-states', 'account-empty', 'account-error', 'audit',
  'audit-empty', 'error',
  'form-bad-request', 'form-forbidden', 'form-conflict', 'form-server-error',
];
const expectedStatus = new Map([
 ['mcp-saved-start-failed',503], ['mcp-authorization-unavailable',503], ['mcp-invalid',400],['mcp-conflict',409],['mcp-unavailable',503],
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

async function checkMCPTransport(browser, baseURL, viewport) {
  const page = await browser.newPage({ viewport });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(`${baseURL}/qa/ui/mcp/new`);
  await page.getByLabel('Command', { exact: true }).fill('draft-command');
  await page.locator('#env-key-0').fill('DRAFT_ENV');
  await page.locator('#env-value-0').fill('synthetic-draft-value');
  const transport = page.getByLabel('Transport', { exact: true });
  for (const value of ['http', 'sse']) {
    await transport.selectOption(value);
    assert.equal(await page.getByLabel('Command', { exact: true }).isVisible(), false, `${value} hides local process fields`);
    assert.equal(await page.locator('#env-key-0').isVisible(), false, `${value} hides environment fields`);
    assert.equal(await page.getByLabel('Server URL', { exact: true }).isVisible(), true, `${value} shows remote server fields`);
    assert.equal(await page.locator('#header-key-0').isVisible(), true, `${value} shows header fields`);
    const names = await transport.evaluate(select => [...new FormData(select.form).keys()]);
    for (const name of ['command', 'args', 'directory', 'env_key', 'env_value']) {
      assert.equal(names.includes(name), false, `${value} excludes inactive ${name} from submission`);
    }
    assert.equal(names.includes('url'), true, `${value} submits its URL`);
    await page.getByText('Client settings — optional', { exact: true }).click();
    assert.equal(await page.getByLabel('Client ID', { exact: true }).isVisible(), true, `${value} exposes optional native client settings`);
    await page.getByLabel('Client secret', { exact: true }).fill('synthetic-client-draft');
    const form = page.locator('form[data-mcp-create]');
    assert.equal(await form.getAttribute('hx-boost'), 'false', 'creation uses native submit');
    assert.equal(await form.getAttribute('hx-post'), null, 'OAuth controls share a native form');
    assert.equal(await page.getByRole('button', { name: 'Create and authorize in browser', exact: true }).isVisible(), true);
    await page.getByText('Client settings — optional', { exact: true }).click();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${value} has no overflow at ${viewport.width}px`);
  }
  await page.getByLabel('Server URL', { exact: true }).fill('https://synthetic.example.test/mcp');
  await transport.selectOption('stdio');
  assert.equal(await page.getByLabel('Command', { exact: true }).isVisible(), true, 'stdio shows local process fields');
  assert.equal(await page.locator('#env-key-0').isVisible(), true, 'stdio shows environment fields');
  assert.equal(await page.getByLabel('Command', { exact: true }).inputValue(), 'draft-command', 'switching back preserves non-secret draft fields');
  assert.equal(await page.getByLabel('Server URL', { exact: true }).isVisible(), false, 'stdio hides remote server fields');
  assert.equal(await page.locator('#header-key-0').isVisible(), false, 'stdio hides header fields');
  const names = await transport.evaluate(select => [...new FormData(select.form).keys()]);
  for (const name of ['url', 'client_id', 'client_secret', 'scopes', 'header_key', 'header_value']) {
    assert.equal(names.includes(name), false, `stdio excludes inactive ${name} from submission`);
  }
  await page.getByRole('link', { name: 'All MCP servers', exact: true }).click();
  await page.getByRole('link', { name: 'Add server', exact: true }).click();
  await page.getByRole('heading', { name: 'Add MCP server', exact: true }).waitFor();
  assert.equal(await page.getByLabel('Server URL', { exact: true }).isVisible(), false, 'HTMX navigation initializes the stdio form');
  await page.getByLabel('Transport', { exact: true }).focus();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Tab');
  assert.equal(await page.getByLabel('Server URL', { exact: true }).isVisible(), true, 'keyboard transport selection updates the form');
  assert.equal(await page.getByLabel('Command', { exact: true }).isVisible(), false);
  assert.deepEqual(errors, [], 'transport switching has no browser errors');
  await page.close();
}

async function checkTopBar(page, label) {
  const geometry = await page.evaluate(() => {
    const header = document.querySelector('.app-header');
    const bounds = header.getBoundingClientRect();
    const brand = document.querySelector('.sidebar-brand').getBoundingClientRect();
    const controls = [...header.querySelectorAll('.sidebar-toggle, .viewer-actions > *')].filter(element => element.getClientRects().length).map(element => {
      const control = element.getBoundingClientRect();
      return { top: control.top, bottom: control.bottom, center: control.top + control.height / 2 };
    });
    return {
      top: bounds.top, bottom: bounds.bottom, center: bounds.top + (bounds.height - 1) / 2,
      brandBottom: brand.bottom, controls,
      headingTop: document.querySelector('h1').getBoundingClientRect().top,
    };
  });
  assert.equal(geometry.top, 0, `${label} top bar stays at viewport top`);
  assert.equal(geometry.bottom, geometry.brandBottom, `${label} top bar aligns with sidebar brand`);
  for (const control of geometry.controls) {
    assert.equal(control.top >= geometry.top && control.bottom <= geometry.bottom, true, `${label} top bar control is visible`);
    assert.equal(Math.abs(control.center - geometry.center) <= 1, true, `${label} top bar controls are vertically centered`);
  }
  assert.equal(geometry.headingTop >= geometry.bottom, true, `${label} heading stays below top bar`);
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
    if (await page.locator('.login-page').count()) {
      const theme = await page.locator('.login-page').evaluate(element => ({
        scheme: getComputedStyle(element).colorScheme,
        card: getComputedStyle(element.querySelector('.card')).backgroundColor,
        heading: getComputedStyle(element.querySelector('h1')).color,
      }));
      assert.equal(theme.scheme, 'dark', `${route} auth color scheme`);
      assert.equal(theme.card, 'rgb(33, 37, 41)', `${route} dark auth card`);
      assert.equal(theme.heading, 'rgb(222, 226, 230)', `${route} legible auth heading`);
      const controls = await page.locator('.login-page .form-control').evaluateAll(elements => elements.map(element => getComputedStyle(element).backgroundColor));
      assert.equal(controls.every(color => [...color.matchAll(/\d+/g)].every(match => Number(match[0]) < 80)), true, `${route} dark form controls`);
      const secondaryActions = await page.locator('.login-page .btn-outline-secondary').evaluateAll(elements => elements.map(element => getComputedStyle(element).color));
      assert.equal(secondaryActions.every(color => color === theme.heading), true, `${route} legible secondary actions`);
    }
    const contrastFailures = await page.locator('h1, h2, .form-label, .status-chip, .btn:not(:disabled)').evaluateAll(elements => {
      const rgb = color => color.match(/[\d.]+/g).map(Number);
      const luminance = color => rgb(color).slice(0, 3).map(value => { const channel = value / 255; return channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4; }).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0);
      return elements.filter(element => element.textContent.trim()).flatMap(element => {
        let parent = element;
        while (parent && rgb(getComputedStyle(parent).backgroundColor)[3] === 0) parent = parent.parentElement;
        const foreground = luminance(getComputedStyle(element).color);
        const background = luminance(getComputedStyle(parent || document.documentElement).backgroundColor);
        const ratio = (Math.max(foreground, background) + .05) / (Math.min(foreground, background) + .05);
        return ratio < 4.5 ? [{ text: element.textContent.trim(), ratio }] : [];
      });
    });
    assert.deepEqual(contrastFailures, [], `${route} text contrast at ${viewport.width}px`);
    assert.equal(await page.locator('h1').count(), 1, `${route} heading at ${viewport.width}px`);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${route} document overflow at ${viewport.width}px`);
    if (await page.locator('.app-header').count()) await checkTopBar(page, `${route} native at ${viewport.width}px`);
    assert.deepEqual(errors, [], `${route} script errors at ${viewport.width}px`);
    if (process.env.BACKOFFICE_E2E_SCREENSHOTS) {
      fs.mkdirSync(process.env.BACKOFFICE_E2E_SCREENSHOTS, { recursive: true });
      await page.screenshot({ path: path.join(process.env.BACKOFFICE_E2E_SCREENSHOTS, `${(route || 'gallery').replaceAll('/', '-')}-${viewport.width}.png`), fullPage: true });
    }
  }

  const nativeLayouts = new Map();
  const destinations = ['Overview', 'Account', 'Access', 'MCP', 'Audit'];
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
    const navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
    if (viewport.width < 992) await page.getByRole('button', { name: 'Toggle navigation' }).click();
    const response = page.waitForResponse(response => response.url() === `${baseURL}/qa/ui/${name.toLowerCase()}`);
    await navigation.getByText(name, { exact: true }).click();
    await response;
    await page.waitForFunction(expected => document.querySelector('h1')?.textContent === expected, name);
    assert.equal(await page.locator('main#main-content').count(), 1, `${name} HTMX keeps one main landmark`);
    assert.deepEqual(await layout(), nativeLayouts.get(name), `${name} HTMX layout matches native navigation`);
    await page.waitForFunction(expected => [...document.querySelectorAll('[data-nav-link]')].filter(link => link.textContent.trim() === expected).every(link => link.getAttribute('aria-current') === 'page'), name);
    assert.equal(await page.locator('main').evaluate(element => element === document.activeElement), true, `${name} main receives focus after navigation`);
    await checkTopBar(page, `${name} HTMX at ${viewport.width}px`);
  }

  await page.goBack();
  await page.waitForFunction(() => document.querySelector('h1')?.textContent === 'Audit');
  assert.equal(await page.locator('main#main-content').count(), 1, 'history restores one main');
  await page.waitForFunction(() => document.querySelector('.sidebar-menu a[aria-current="page"]')?.textContent.trim() === 'Audit');
  await checkTopBar(page, `Audit history at ${viewport.width}px`);
  await page.goForward();
  await page.waitForFunction(() => document.querySelector('h1')?.textContent === 'Overview');
  await checkTopBar(page, `Overview history at ${viewport.width}px`);

  await page.goto(`${baseURL}/qa/ui/layout-long`);
  await page.evaluate(() => window.scrollTo(0, 400));
  await page.waitForFunction(() => scrollY > 0);
  assert.equal(await page.locator('.app-header').evaluate(element => element.getBoundingClientRect().top), 0, `scrolled top bar stays visible at ${viewport.width}px`);
  assert.equal(await page.locator('.app-header .viewer-actions').isVisible(), true, 'scrolled viewer controls remain visible');
  await page.goto(`${baseURL}/qa/ui/style-guide`);
  assert.equal(await page.locator('.qa-guide-section').count(), 9);
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
  assert.equal(await page.locator('main').evaluate(element => getComputedStyle(element).colorScheme), 'dark');
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
    await page.getByRole('button', { name: 'Toggle navigation' }).click();
    assert.equal(await page.getByRole('navigation', { name: 'Primary navigation', exact: true }).isVisible(), true);
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('button', { name: 'Toggle navigation' }).getAttribute('aria-expanded'), 'false');
  }
  await page.goto(`${baseURL}/qa/ui/overview-empty`);
  assert.equal(await page.locator('.app-footer').count(), 1);
  assert.equal(await page.locator('.viewer strong').textContent(), 'qa-superuser');
  assert.equal(await page.locator('.app-footer').evaluate(element => Math.round(element.getBoundingClientRect().bottom)), viewport.height, 'short page footer fits viewport');
  const toggle = page.getByRole('button', { name: 'Toggle navigation' });
  const originalWidth = await page.locator('.app-main').evaluate(element => element.clientWidth);
  await toggle.click();
  await page.waitForTimeout(350);
  if (viewport.width >= 992) {
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    assert.equal(await page.locator('.app-main').evaluate(element => element.clientWidth) > originalWidth, true);
    assert.equal(await page.locator('.app-header').evaluate(element => element.getBoundingClientRect().left), 0, 'collapsed sidebar expands top bar to viewport edge');
  } else {
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await page.locator('.app-main').evaluate(element => element.inert), true);
    const links = page.locator('.sidebar-menu a');
    await links.last().focus();
    await page.keyboard.press('Tab');
    assert.equal(await links.first().evaluate(element => element === document.activeElement), true, 'mobile focus stays within visible menu');
    await page.locator('.sidebar-overlay').click({ position: { x: viewport.width - 10, y: 400 } });
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false', 'backdrop closes mobile menu');
    await toggle.click();
    await page.keyboard.press('Escape');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    assert.equal(await toggle.evaluate(element => element === document.activeElement), true);
    assert.equal(await page.locator('.app-main').evaluate(element => element.inert), false);
  }
  await page.close();
}

(async () => {
  const { server, url } = await startPreview();
  try {
    const browser = await chromium.launch({ headless: true });
    try {
      for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 768, height: 1024 }, { width: 390, height: 844 }]) {
        await checkMCPTransport(browser, url, viewport);
        await checkPage(browser, url, viewport);
      }
      const noScript = await browser.newPage({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
      await noScript.goto(`${url}/qa/ui/mcp/new`);
      await noScript.getByLabel('Transport', { exact: true }).selectOption('http');
      assert.equal(await noScript.getByLabel('Command', { exact: true }).isVisible(), true, 'native form keeps its explained transport fields available');
      assert.equal(await noScript.getByLabel('Server URL', { exact: true }).isVisible(), true, 'native remote URL field is usable');
      await noScript.getByText('Client settings — optional', { exact: true }).click();
      assert.equal(await noScript.getByLabel('Client ID', { exact: true }).isVisible(), true, 'client settings use native details without JavaScript');
      assert.equal(await noScript.getByRole('button', { name: 'Create and authorize in browser', exact: true }).isVisible(), true);
      await noScript.goto(`${url}/qa/ui/audit`);
      assert.equal(await noScript.locator('table tbody tr').count(), 3);
      await noScript.getByText('Dates and actor ID').click();
      assert.equal(await noScript.locator('#audit-actor').isVisible(), true);
      await noScript.locator('.native-navigation summary').click();
      await noScript.getByRole('navigation', { name: 'Mobile navigation' }).getByText('Account', { exact: true }).click();
      assert.equal(await noScript.locator('h1').textContent(), 'Account');
      await checkTopBar(noScript, 'Account without JavaScript');
      await noScript.evaluate(() => window.scrollTo(0, 400));
      await noScript.waitForFunction(() => scrollY > 0);
      assert.equal(await noScript.locator('.app-header').evaluate(element => element.getBoundingClientRect().top), 0, 'top bar stays visible without JavaScript');
      await noScript.goto(`${url}/qa/ui/bindings-issued`);
      assert.equal(await noScript.locator('[data-binding-channel]').count(), 4);
      assert.equal(await noScript.locator('input[data-binding-secret]').count(), 9);
      assert.equal(await noScript.locator('input[data-binding-secret]').first().inputValue(), 'bind_SYNTHETIC_PREVIEW_ONLY');
      await noScript.getByText('Replace invitation', { exact: true }).first().click();
      assert.equal(await noScript.locator('input[name=replace]').first().isVisible(), true, 'native replacement confirmation is usable');
      await noScript.close();
      console.log(`Backoffice E2E: ${routes.length} gallery pages and states at 390/768/1024/1440px, consistent heading colors, native/HTMX layout, audit, refresh, and navigation passed`);
    } finally { await browser.close(); }
  } finally { process.kill(-server.pid, 'SIGINT'); }
})().catch(error => { console.error(error); process.exitCode = 1; });
