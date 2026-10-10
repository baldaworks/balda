const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require('playwright');

const origin = process.argv[2];
const basePath = process.argv[3] || '';
assert.equal(new URL(origin).hostname, '127.0.0.1', 'layout browser uses an isolated local listener');
assert.ok(basePath === '' || basePath === '/balda');

const browserPath = `${basePath}/backoffice`;
const missingOrigin = process.argv[4] === 'missing';
const publicOrigin = missingOrigin ? '' : origin;
const screenshotRoot = process.env.BALDA_WEBHOOKS_LAYOUT_SCREENSHOTS;

async function screenshot(page, viewport, label) {
  if (!screenshotRoot) return;
  const dir = path.join(screenshotRoot, `${basePath ? 'balda' : 'root'}-${missingOrigin ? 'missing-origin' : 'public-origin'}`,
    viewport.width === 1440 ? 'desktop' : 'mobile');
  await fs.mkdir(dir, { recursive: true });
  const file = path.join(dir, `${label}.png`);
  await page.screenshot({ path: file, fullPage: true });
  console.log(`Screenshot: ${file}`);
}

async function checkOriginMessage(page) {
  assert.equal(await page.getByText('A full public webhook URL requires', { exact: false }).count(),
    missingOrigin ? 1 : 0, 'missing-origin explanation matches listener configuration');
}

async function checkLayout(page, label) {
  assert.equal(await page.locator('main#main-content').count(), 1, `${label} has one main landmark`);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(overflow <= 1, `${label} overflows by ${overflow}px`);
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const routeName = viewport.width === 1440 ? 'orders' : 'mobile_orders';
      const routePath = `${basePath}/webhooks/${routeName}`;
      const context = await browser.newContext({ viewport, javaScriptEnabled: true });
      const page = await context.newPage();
      const errors = [];
      const assets = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('requestfailed', request => errors.push(`${request.url()}: ${request.failure()?.errorText}`));
      page.on('response', response => {
        const pathname = new URL(response.url()).pathname;
        if (pathname.includes('/assets/')) assets.push({ pathname, status: response.status() });
      });

      const root = await context.request.get(`${origin}${browserPath}/`, { maxRedirects: 0 });
      assert.equal(root.status(), 303, 'browser root redirects within the browser mount');
      assert.equal(root.headers().location, `${browserPath}/overview`);
      const login = await page.goto(`${origin}${browserPath}/login`);
      assert.equal(login.status(), 200, 'login page is mounted beneath the browser root');
      assert.equal(new URL(page.url()).pathname, `${browserPath}/login`);
      await checkLayout(page, 'login');
      assert.ok(assets.length > 0, 'browser loaded its static assets');
      assert.ok(assets.every(asset => asset.pathname.startsWith(`${browserPath}/assets/`) && asset.status === 200),
        `assets stay beneath the browser mount: ${JSON.stringify(assets)}`);
      await page.getByLabel(/^Username(?: \(required\))?$/).fill('superuser');
      await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct horse battery staple');
      await Promise.all([
        page.waitForURL(`${origin}${browserPath}/overview`),
        page.getByRole('button', { name: 'Sign in', exact: true }).click(),
      ]);
      await checkLayout(page, 'overview');
      const links = await page.locator('.app-sidebar a[href]').evaluateAll(elements =>
        elements.map(element => new URL(element.href).pathname));
      assert.ok(links.length > 0 && links.every(pathname => pathname.startsWith(`${browserPath}/`)),
        `navigation stays beneath the browser mount: ${JSON.stringify(links)}`);
      for (const endpoint of ['events', 'commands']) {
        const callback = await context.request.get(`${origin}${basePath}/gateway/slack/${endpoint}`);
        assert.equal(callback.status(), 202, `Slack ${endpoint} is mounted on the same listener`);
      }

      const inventory = await page.goto(`${origin}${browserPath}/webhooks`);
      assert.equal(inventory.status(), 200);
      await checkLayout(page, 'webhook inventory');
      await page.getByRole('columnheader', { name: 'Webhook URL', exact: true }).waitFor();
      await checkOriginMessage(page);
      const configuredURL = `${publicOrigin}${basePath}/webhooks/configured`;
      const configuredRow = page.getByRole('region', { name: /Webhook routes table/ }).locator('tbody tr').filter({ hasText: 'configured' });
      await configuredRow.getByText(configuredURL, { exact: true }).waitFor();
      await screenshot(page, viewport, 'list');
      await page.goto(`${origin}${browserPath}/webhooks/configured`);
      await page.getByRole('heading', { name: 'Configuration webhook', exact: true }).waitFor();
      await page.getByText(configuredURL, { exact: true }).first().waitFor();
      await checkOriginMessage(page);
      await checkLayout(page, 'configuration detail');
      await screenshot(page, viewport, 'config-detail');
      await page.goto(`${origin}${browserPath}/webhooks`);
      await page.getByRole('link', { name: 'Add webhook' }).click();
      assert.equal(new URL(page.url()).pathname, `${browserPath}/webhooks`);
      await page.getByRole('heading', { name: 'New webhook route' }).waitFor();
      const name = page.getByLabel('Route name', { exact: true });
      const url = page.getByLabel('Webhook URL', { exact: true });
      assert.equal(await url.isEditable(), false);
      await checkOriginMessage(page);
      for (const invalidName of ['', 'bad/name', 'UpperCase', 'bad name']) {
        await name.fill(invalidName);
        assert.equal(await url.inputValue(), '', `invalid slug ${JSON.stringify(invalidName)} has no callback preview`);
      }
      await name.fill('orders');
      assert.equal(await url.inputValue(), `${publicOrigin}${basePath}/webhooks/orders`);
      await name.fill('other_route');
      assert.equal(await url.inputValue(), `${publicOrigin}${basePath}/webhooks/other_route`);
      await checkLayout(page, 'create form');
      await name.fill(routeName);
      await page.getByLabel('Prompt template').fill('Handle {{.RawBody}}');
      await screenshot(page, viewport, 'create-valid');
      await page.getByRole('button', { name: 'Create webhook' }).click();
      await page.getByRole('heading', { name: 'Webhook secret · shown once' }).waitFor();
      assert.equal(new URL(page.url()).pathname, `${browserPath}/webhooks/${routeName}`);
      await page.reload();
      await page.getByRole('heading', { name: 'Edit webhook route' }).waitFor();
      assert.equal(await page.getByLabel('Webhook URL').inputValue(), `${publicOrigin}${routePath}`);
      await page.getByText(`${publicOrigin}${routePath}`, { exact: true }).first().waitFor();
      await checkLayout(page, 'saved managed detail');
      await checkOriginMessage(page);
      await screenshot(page, viewport, 'saved-managed');

      await Promise.all([
        page.waitForURL(`${origin}${browserPath}/webhooks`),
        page.getByRole('link', { name: 'All webhooks' }).click(),
      ]);
      const route = page.getByRole('region', { name: /Webhook routes table/ }).locator('tbody tr').filter({ hasText: routeName });
      await route.getByText(`${publicOrigin}${routePath}`, { exact: true }).waitFor();
      await checkLayout(page, 'saved route inventory');
      await checkOriginMessage(page);
      assert.deepEqual(errors, [], `browser errors at ${viewport.width}px`);
      await context.close();
    }
    console.log(`Shared HTTP layout passed at ${basePath || '/'} on one local listener with JavaScript enabled`);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
