const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');

const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, '127.0.0.1', 'browser gate requires an isolated loopback server');
const screenshotDir = process.env.BALDA_SCHEDULES_SCREENSHOTS;
if (screenshotDir) fs.mkdirSync(screenshotDir, { recursive: true });

async function screenshot(page, name, viewport) {
  if (!screenshotDir) return;
  const prefix = new URL(baseURL).pathname === '/' ? 'root' : 'base';
  await page.screenshot({ path: path.join(screenshotDir, `${prefix}-${name}-${viewport}.png`), fullPage: true });
}

async function assertNoDocumentOverflow(page, name) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(overflow <= 1, `${name} overflows the document horizontally by ${overflow}px`);
}

async function textLineCount(locator) {
  return locator.evaluate(element => {
    const text = [...element.childNodes].find(node => node.nodeType === Node.TEXT_NODE);
    const range = document.createRange();
    range.selectNodeContents(text);
    return range.getClientRects().length;
  });
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

      const inventory = await page.goto(`${baseURL}/schedules`);
      assert.equal(inventory.status(), 200);
      assert.equal(await page.locator('h1').textContent(), 'Schedules');
      const managedLink = page.getByRole('link', { name: 'daily-summary' });
      const configuredLink = page.getByRole('link', { name: 'config:morning' });
      await managedLink.waitFor();
      await configuredLink.waitFor();
      await assertNoDocumentOverflow(page, `${viewport.name} inventory`);
      await screenshot(page, 'inventory', viewport.name);
      if (viewport.name === 'mobile') {
        const source = page.locator('table tbody tr').first().locator('td').nth(1);
        assert.equal(await textLineCount(source), 1, 'source label wraps on a narrow inventory');
        assert.equal(await textLineCount(configuredLink), 1, 'short schedule ID wraps on a narrow inventory');
        const table = page.getByRole('region', { name: /Schedules table/ });
        assert.ok(await table.evaluate(element => element.scrollWidth > element.clientWidth), 'mobile inventory table does not scroll');
        await table.evaluate(element => { element.scrollLeft = element.scrollWidth - element.clientWidth; });
        await screenshot(page, 'inventory-right', viewport.name);
      }

      await Promise.all([page.waitForURL(`${baseURL}/schedules/daily-summary`), managedLink.click()]);
      await page.getByRole('heading', { name: 'Edit schedule' }).waitFor();
      await page.waitForFunction(() => document.querySelector('.app-sidebar [data-nav-link][href$="/schedules"]')?.getAttribute('aria-current') === 'page');
      await page.getByRole('button', { name: 'Run now' }).click();
      await page.getByText('Queued', { exact: true }).waitFor();
      await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
      await page.getByRole('heading', { name: 'Run detail · Queued' }).waitFor();
      await page.getByText("Summarize yesterday's work", { exact: true }).last().waitFor();
      await page.getByText('No output recorded yet.').waitFor();
      await assertNoDocumentOverflow(page, `${viewport.name} managed detail`);
      await screenshot(page, 'managed', viewport.name);
      if (viewport.name === 'mobile') {
        const table = page.getByRole('region', { name: /Schedule run history/ });
        const trigger = table.locator('tbody td').nth(1);
        assert.equal(await textLineCount(trigger), 1, 'run trigger wraps on a narrow history');
        assert.ok(await table.evaluate(element => element.scrollWidth > element.clientWidth), 'mobile history table does not scroll');
        await table.evaluate(element => { element.scrollLeft = element.scrollWidth - element.clientWidth; });
        await screenshot(page, 'managed-history-right', viewport.name);
      }

      await Promise.all([page.waitForURL(`${baseURL}/schedules`), page.getByRole('link', { name: 'All schedules' }).click()]);
      if (viewport.name === 'desktop') {
        const aliasInventory = await page.goto(`${baseURL}/aliases`);
        assert.equal(aliasInventory.status(), 200);
        await page.getByRole('link', { name: 'Add alias' }).click();
        await page.getByLabel('Alias name').fill('main_chat');
        await page.getByLabel('Public locator').fill('telegram:9001:0');
        await Promise.all([page.waitForURL(`${baseURL}/aliases/main_chat`), page.getByRole('button', { name: 'Create alias' }).click()]);
        await page.goto(`${baseURL}/schedules`);
      }
      await Promise.all([page.waitForURL(`${baseURL}/schedules?new=1`), page.getByRole('link', { name: 'Add schedule' }).click()]);
      assert.equal(await page.locator('h1').textContent(), 'Add schedule');
      await assertPlaceholderDistinct(page.getByLabel('Cron (UTC)'), `${viewport.name} schedule cron`);
      const newID = `weekly-review-${viewport.name}`;
      await page.getByLabel('Schedule ID').fill(newID);
      await page.getByLabel('Cron (UTC)').fill('0 9 * * 1');
      if (viewport.name === 'desktop') {
        await page.getByLabel('Report destination').selectOption('managed_alias');
        await assertPlaceholderDistinct(page.getByLabel('Managed alias'), 'schedule report alias');
        await page.getByLabel('Managed alias').fill('main_chat');
      }
      await page.getByLabel('Content').fill('Prepare weekly review');
      await assertNoDocumentOverflow(page, `${viewport.name} creation form`);
      await screenshot(page, 'create', viewport.name);
      await Promise.all([page.waitForURL(`${baseURL}/schedules/${newID}`), page.getByRole('button', { name: 'Create schedule' }).click()]);
      await page.getByRole('heading', { name: 'Edit schedule' }).waitFor();
      if (viewport.name === 'desktop') {
        assert.equal(await page.getByLabel('Report destination').inputValue(), 'managed_alias');
        assert.equal(await page.getByLabel('Managed alias').inputValue(), 'main_chat');
        await page.getByRole('button', { name: 'Run now' }).click();
        await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
        await page.getByText('telegram:9001:0', { exact: true }).waitFor();
        const firstRunURL = page.url();
        assert.ok(new URL(firstRunURL).searchParams.has('run_id'), 'first run has a durable detail URL');
        await page.goto(`${baseURL}/aliases/main_chat`);
        await page.getByLabel('Public locator').fill('telegram:9002:0');
        await page.getByRole('button', { name: 'Save destination' }).click();
        assert.equal(await page.getByLabel('Public locator').inputValue(), 'telegram:9002:0');
        await page.goto(`${baseURL}/schedules/${newID}`);
        assert.equal(await page.getByLabel('Managed alias').inputValue(), 'main_chat');
        await page.getByRole('button', { name: 'Run now' }).click();
        await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
        await page.getByText('telegram:9002:0', { exact: true }).waitFor();
        await page.goto(firstRunURL);
        await page.getByText('telegram:9001:0', { exact: true }).waitFor();
      }
      await screenshot(page, 'created', viewport.name);

      await Promise.all([page.waitForURL(`${baseURL}/schedules`), page.getByRole('link', { name: 'All schedules' }).click()]);
      await Promise.all([page.waitForURL(`${baseURL}/schedules/config:morning`), page.getByRole('link', { name: 'config:morning' }).click()]);
      await page.getByRole('heading', { name: 'Configuration schedule' }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Save schedule' }).count(), 0);
      await page.getByRole('button', { name: 'Run now' }).click();
      await page.getByText('Queued', { exact: true }).waitFor();
      await assertNoDocumentOverflow(page, `${viewport.name} configured detail`);
      await screenshot(page, 'configured', viewport.name);

      assert.deepEqual(errors, [], `${viewport.name} browser errors`);
      await context.close();
    }
    console.log('Authenticated Schedules create, manual-run and history flow passed at desktop and mobile widths');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
