const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');

const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_PROVIDERS_LAYOUT_SCREENSHOTS || '/tmp/balda-providers-screens/layout';
const states = [
  ['providers', 200],
  ['providers-empty', 200],
  ['providers/new', 200],
  ['providers/managed', 200],
  ['providers/configured', 200],
  ['providers-conflict', 409],
];
const viewports = [
  {width: 1440, height: 900},
  {width: 1024, height: 768},
  {width: 768, height: 1024},
  {width: 390, height: 844},
];
const forbidden = ['managed-secret', 'configured-secret', 'synthetic-api-key'];

(async () => {
  const browser = await chromium.launch({headless: true});
  try {
    for (const viewport of viewports) {
      const page = await browser.newPage({viewport});
      const failures = [];
      page.on('pageerror', error => failures.push(error.message));
      page.on('requestfailed', request => failures.push(request.url()));
      page.on('response', response => {
        if (response.url().includes('/assets/') && response.status() >= 400) failures.push(response.url());
      });
      const directory = path.join(screenshots, base ? 'prefixed' : 'root', `${viewport.width}x${viewport.height}`);
      fs.mkdirSync(directory, {recursive: true});
      for (const [state, status] of states) {
        const label = `${base || '/'} ${viewport.width} ${state}`;
        const response = await page.goto(`${origin}${prefix}/${state}`);
        assert.equal(response.status(), status, `${label} status`);
        const main = page.locator('main#main-content');
        await main.waitFor();
        assert.equal(await main.count(), 1, `${label} one main`);
        assert.equal(await main.getAttribute('hx-history'), 'false', `${label} history`);
        assert.match(response.headers()['cache-control'], /no-store/, `${label} no-store`);
        assert.deepEqual((await page.locator('.app-sidebar [aria-current="page"]').allTextContents()).map(text => text.trim()), ['Providers'], `${label} active navigation`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), `${label} document overflow`);
        const rendered = await main.innerText();
        for (const secret of forbidden) assert.ok(!rendered.includes(secret), `${label} secret-free`);

        if (await page.locator('table').count()) {
          if (viewport.width === 390) assert.ok(await page.locator('.table-responsive').evaluate(element => element.scrollWidth > element.clientWidth), `${label} table scroll contained`);
        }
        const fragment = await page.request.get(`${origin}${prefix}/${state}`, {headers: {'HX-Request': 'true', 'HX-Target': 'main-content'}});
        assert.equal(fragment.status(), status, `${label} fragment status`);
        const html = await fragment.text();
        assert.equal((html.match(/id="main-content"/g) || []).length, 1, `${label} fragment main`);
        assert.ok(!/<html|<body|class="app-sidebar"/.test(html), `${label} fragment shell`);
        for (const secret of forbidden) assert.ok(!html.includes(secret), `${label} fragment secret-free`);
        const history = await page.request.get(`${origin}${prefix}/${state}`, {headers: {'HX-Request': 'true', 'HX-Target': 'main-content', 'HX-History-Restore-Request': 'true'}});
        assert.equal(history.status(), status, `${label} history status`);
        assert.ok((await history.text()).includes('<!doctype html>'), `${label} history full document`);

        if (state === 'providers/new') {
          const id = page.getByLabel('Provider ID', {exact: true});
          await id.focus();
          assert.notEqual(await id.evaluate(element => getComputedStyle(element).boxShadow), 'none', `${label} focus`);
          await assertPlaceholderDistinct(page.getByLabel('Hosted timeout', {exact: true}), label);
          assert.equal(await page.getByLabel('Replacement API key', {exact: true}).inputValue(), '', `${label} empty api key`);
          assert.equal(await page.getByLabel('Replacement environment', {exact: true}).inputValue(), '', `${label} empty environment`);
        }
        if (state === 'providers/managed') {
          assert.equal(await page.getByLabel('Replacement API key', {exact: true}).inputValue(), '', `${label} managed api key blank`);
          assert.equal(await page.getByLabel('Replacement environment', {exact: true}).inputValue(), '', `${label} managed environment blank`);
          await page.getByLabel(/^Confirm disabling/).check();
          await page.getByLabel(/^Confirm deleting/).check();
        }
        if (state === 'providers/configured') {
          assert.equal(await page.getByRole('button', {name: /Save|Delete|Disable|Enable/}).count(), 0, `${label} configured read-only`);
        }
        await page.screenshot({path: path.join(directory, `${state.replaceAll('/', '-')}.png`), fullPage: true});
        assert.deepEqual(failures, [], `${label} browser errors`);
      }
      await page.goto(`${origin}${prefix}/providers`);
      if (viewport.width < 1024) {
        await page.getByRole('button', {name: 'Toggle navigation', exact: true}).click();
        await page.locator('.app-sidebar').waitFor();
        await page.keyboard.press('Escape');
      }
      assert.deepEqual(failures, [], 'navigation failures');
      console.log(`PASS ${base || '/'} ${viewport.width}x${viewport.height}: ${states.length} states, fragments, history, focus, screenshots`);
      await page.close();
    }
  } finally {
    await browser.close();
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
