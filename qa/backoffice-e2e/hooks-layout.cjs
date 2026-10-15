const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');
const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_HOOKS_LAYOUT_SCREENSHOTS || '/tmp/balda-hooks-screens/layout';
const states = [['hooks', 200], ['hooks-empty', 200], ['hooks-long', 200], ['hooks/new', 200], ['hooks/guard', 200], ['hooks-invalid', 400], ['hooks-conflict', 409], ['hooks-unavailable', 503]];
const viewports = [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 768, height: 1024 }, { width: 390, height: 844 }];
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of viewports) {
      const page = await browser.newPage({ viewport });
      const failures = [];
      page.on('pageerror', e => failures.push(e.message));
      page.on('requestfailed', r => failures.push(r.url()));
      page.on('response', r => { if (r.url().includes('/assets/') && r.status() >= 400) failures.push(r.url()); });
      const directory = path.join(screenshots, base ? 'prefixed' : 'root', `${viewport.width}x${viewport.height}`);
      fs.mkdirSync(directory, { recursive: true });
      for (const [state, status] of states) {
        const label = `${base || '/'} ${viewport.width} ${state}`;
        const response = await page.goto(`${origin}${prefix}/${state}`);
        assert.equal(response.status(), status, `${label} status`);
        assert.equal(await page.locator('main#main-content').count(), 1, `${label} one main`);
        assert.equal(await page.locator('main').getAttribute('hx-history'), 'false', `${label} private history`);
        assert.match(response.headers()['cache-control'], /no-store/, `${label} private response`);
        assert.deepEqual((await page.locator('.app-sidebar [aria-current="page"]').allTextContents()).map(s => s.trim()), ['Agent hooks'], `${label} active navigation`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), `${label} document overflow`);
        const fragment = await page.request.get(`${origin}${prefix}/${state}`, { headers: { 'HX-Request': 'true', 'HX-Target': 'main-content' } });
        assert.equal(fragment.status(), status, `${label} fragment status`);
        const html = await fragment.text();
        assert.equal((html.match(/id="main-content"/g) || []).length, 1, `${label} fragment main`);
        assert.ok(!/<html|<body|class="app-sidebar"/.test(html), `${label} fragment shell`);
        if (await page.locator('#hook-command').count()) {
          await assertPlaceholderDistinct(page.locator('#hook-command'), label);
          for (const text of ['Name', 'Event', 'Executable', 'Arguments', 'Timeout in seconds', 'Enable this hook']) assert.equal(await page.getByLabel(text).count(), 1, `${label} ${text} label`);
          await page.getByLabel(/^Executable/).focus();
          assert.notEqual(await page.getByLabel(/^Executable/).evaluate(e => getComputedStyle(e).boxShadow), 'none', `${label} visible focus`);
        }
        await page.screenshot({ path: path.join(directory, `${state.replaceAll('/', '-')}.png`), fullPage: true });
        assert.deepEqual(failures, [], `${label} browser failures`);
      }
      await page.goto(`${origin}${prefix}/hooks`);
      await page.getByRole('link', { name: 'Add hook', exact: true }).click();
      await page.getByRole('heading', { name: 'Add agent hook', exact: true }).waitFor();
      await page.waitForFunction(() => document.activeElement?.id === 'main-content');
      await page.getByRole('link', { name: 'Cancel', exact: true }).click();
      await page.getByRole('link', { name: 'Edit guard', exact: true }).click();
      await page.getByRole('heading', { name: 'Edit agent hook', exact: true }).waitFor();
      assert.deepEqual((await page.locator('.app-sidebar [aria-current="page"]').allTextContents()).map(s => s.trim()), ['Agent hooks'], 'nested navigation');
      await page.getByLabel('Confirm deleting this hook.', { exact: true }).check();
      assert.deepEqual(failures, [], 'navigation failures');
      console.log(`PASS ${base || '/'} ${viewport.width}x${viewport.height}: ${states.length} states, fragments, navigation, keyboard, screenshots`);
      await page.close();
    }
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
