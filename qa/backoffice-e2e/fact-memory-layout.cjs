const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');

const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_FACT_MEMORY_LAYOUT_SCREENSHOTS || '/tmp/balda-fact-memory-screens/layout';
const states = [
  ['memory', 200], ['memory-long', 200], ['memory-search', 200],
  ['memory-no-results', 200], ['memory-empty', 200], ['memory-disabled', 200],
  ['memory/new', 200], ['memory/1', 200], ['memory-delete', 200],
  ['memory-invalid', 400], ['memory-forbidden', 403], ['memory-missing', 404],
  ['memory-conflict', 409], ['memory-unavailable', 503],
];
const viewports = [{ width: 1440, height: 900 }, { width: 1024, height: 768 },
  { width: 768, height: 1024 }, { width: 390, height: 844 }];

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of viewports) {
      const page = await browser.newPage({ viewport, javaScriptEnabled: true });
      page.setDefaultTimeout(10000);
      const failures = [];
      page.on('pageerror', error => failures.push(error.message));
      page.on('requestfailed', request => failures.push(`${request.url()}: ${request.failure()?.errorText}`));
      page.on('response', response => {
        if (response.url().includes('/assets/') && response.status() >= 400) failures.push(`asset ${response.url()}: ${response.status()}`);
      });
      const directory = path.join(screenshots, base ? 'balda-backoffice' : 'root', `${viewport.width}x${viewport.height}`);
      fs.mkdirSync(directory, { recursive: true });
      for (const [state, status] of states) {
        const label = `${base || '/'} ${viewport.width} ${state}`;
        const response = await page.goto(`${origin}${prefix}/${state}`);
        assert.equal(response.status(), status, `${label} status`);
        assert.equal(await page.locator('main#main-content').count(), 1, `${label} single main`);
        assert.equal(await page.locator('main').getAttribute('hx-history'), 'false', `${label} private history`);
        assert.match(response.headers()['cache-control'], /no-store/, `${label} no-store`);
        assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Fact memory'], `${label} navigation`);
        if (await page.locator('.section-kicker').count()) assert.equal(await page.locator('.section-kicker').textContent(), 'Operations', `${label} section`);
        assert.equal(await page.locator('.app-sidebar .nav-section').filter({ has: page.locator('[aria-current="page"]') }).locator('.nav-section-title').textContent(), 'Operations', `${label} navigation group`);
        assert.equal(await page.evaluate(() => window.syntheticFactExecuted), undefined, `${label} escaped data`);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        if (overflow > 1) {
          await page.screenshot({ path: path.join(directory, `${state.replaceAll('/', '-')}-overflow.png`), fullPage: true });
          console.log(await page.locator('main *').evaluateAll(elements => elements.filter(element => element.getBoundingClientRect().right > innerWidth + 1).map(element => `${element.tagName}.${element.className}: ${element.getBoundingClientRect().width}`).slice(0, 20)));
        }
        assert.ok(overflow <= 1, `${label} document overflow ${overflow}`);
        for (const asset of await page.locator('script[src],link[rel="stylesheet"]').evaluateAll(elements => elements.map(element => element.getAttribute('src') || element.getAttribute('href')))) {
          assert.ok(asset.startsWith(`${base}/assets/`), `${label} canonical asset ${asset}`);
        }
        for (const target of await page.locator('main a[href],main form[action]').evaluateAll(elements => elements.map(element => element.getAttribute('href') || element.getAttribute('action')))) {
          assert.ok(target.startsWith(`${prefix}/`), `${label} canonical target ${target}`);
        }
        const fragment = await page.request.get(`${origin}${prefix}/${state}`, { headers: { 'HX-Request': 'true', 'HX-Target': 'main-content' } });
        assert.equal(fragment.status(), status, `${label} fragment status`);
        const html = await fragment.text();
        assert.equal((html.match(/id="main-content"/g) || []).length, 1, `${label} fragment main`);
        assert.ok(!/<html|<body|class="app-sidebar"/.test(html), `${label} fragment shell`);
        if (await page.locator('#fact-query').count()) {
          assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Fact', 'Actions'], `${label} columns`);
          await assertPlaceholderDistinct(page.locator('#fact-query'), label);
          await page.locator('#fact-query').focus();
          assert.equal(await page.locator('#fact-query').evaluate(element => element === document.activeElement), true, `${label} search focus`);
        }
        if (await page.locator('#fact-text').count()) {
          assert.equal(await page.getByLabel(/^Fact(?: \(required\))?$/).count(), 1, `${label} Fact label`);
          await assertPlaceholderDistinct(page.locator('#fact-text'), label);
          await page.locator('#fact-text').focus();
          assert.notEqual(await page.locator('#fact-text').evaluate(element => getComputedStyle(element).boxShadow), 'none', `${label} visible focus`);
        }
        if (state === 'memory-long') {
          const cell = page.locator('.fact-text-value').last();
          const geometry = await cell.evaluate(element => ({ whiteSpace: getComputedStyle(element).whiteSpace,
            wrap: getComputedStyle(element).overflowWrap, width: element.clientWidth, scroll: element.scrollWidth, height: element.clientHeight }));
          assert.equal(geometry.whiteSpace, 'pre-wrap', `${label} multiline`);
          assert.equal(geometry.wrap, 'anywhere', `${label} long wrapping`);
          assert.ok(geometry.scroll <= geometry.width + 1, `${label} cell clipping`);
          assert.ok(geometry.height > 80, `${label} readable wrapped content`);
        }
        if (state === 'memory') assert.ok(await page.locator('main').textContent().then(text => text.includes('<script>window.syntheticFactExecuted = true</script>')), `${label} escaped fact visible`);
        if (state === 'memory-disabled') assert.equal(await page.locator('main input,main textarea,main button,main form').count(), 0, `${label} disabled controls`);
        if (state === 'memory-delete') await page.getByLabel('Confirm deleting this fact.').check();
        if (state === 'memory-conflict') assert.equal(await page.locator('input[name="expected_version"]').first().inputValue(), '3', `${label} stale version preserved`);
        await page.screenshot({ path: path.join(directory, `${state.replaceAll('/', '-')}.png`), fullPage: true });
        assert.deepEqual(failures, [], `${label} browser and asset failures`);
      }
      await page.goto(`${origin}${prefix}/memory`);
      await page.getByRole('link', { name: 'Add fact', exact: true }).click();
      await page.getByRole('heading', { name: 'Add fact', exact: true }).waitFor();
      assert.equal(await page.locator('main').evaluate(element => element === document.activeElement), true, 'HTMX navigation focuses main');
      assert.equal(await page.locator('main#main-content').count(), 1, 'HTMX retains single main');
      await page.getByLabel(/^Fact(?: \(required\))?$/).focus();
      await page.keyboard.press('Tab');
      assert.equal(await page.getByRole('button', { name: 'Create fact' }).evaluate(element => element === document.activeElement), true, 'editor keyboard reaches create');
      await page.getByRole('link', { name: 'Cancel', exact: true }).click();
      await page.getByRole('link', { name: 'Edit fact 1', exact: true }).click();
      await page.getByRole('heading', { name: 'Edit fact', exact: true }).waitFor();
      await page.waitForFunction(() => document.activeElement?.id === 'main-content');
      assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Fact memory'], 'nested HTMX editor keeps active Fact memory navigation');
      await page.evaluate(conflictPath => window.htmx.ajax('GET', conflictPath, { target: '#main-content', swap: 'outerHTML' }), `${prefix}/memory-conflict`);
      await page.getByRole('link', { name: 'Reopen this fact', exact: true }).waitFor();
      assert.equal(await page.locator('input[name="expected_version"]').first().inputValue(), '3', 'HTMX conflict keeps stale version');
      assert.equal(await page.getByLabel(/^Fact(?: \(required\))?$/).inputValue(), 'Prefers concise replies.\nSynthetic second line.', 'HTMX conflict keeps submitted fact');
      await page.screenshot({ path: path.join(directory, 'memory-conflict-htmx.png'), fullPage: true });
      await page.getByRole('link', { name: 'Reopen this fact', exact: true }).click();
      await page.getByRole('heading', { name: 'Edit fact', exact: true }).waitFor();
      await page.getByRole('link', { name: 'Reopen this fact', exact: true }).waitFor({ state: 'detached' });
      await page.waitForFunction(() => document.activeElement?.id === 'main-content');
      assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Fact memory'], 'reopen retains active navigation');
      const toggle = page.getByRole('button', { name: 'Toggle navigation' });
      if (viewport.width < 992) {
        await toggle.click();
        assert.equal(await page.locator('.app-main').evaluate(element => element.inert), true, 'open mobile navigation protects content');
        const links = page.locator('.sidebar-menu a');
        await links.last().focus();
        await page.keyboard.press('Tab');
        assert.equal(await links.first().evaluate(element => element === document.activeElement), true, 'sidebar keyboard remains reachable');
        await page.keyboard.press('Escape');
        assert.equal(await toggle.getAttribute('aria-expanded'), 'false', 'Escape closes sidebar');
        assert.equal(await toggle.evaluate(element => element === document.activeElement), true, 'sidebar returns focus');
      }
      assert.deepEqual(failures, [], 'navigation has no browser failures');
      console.log(`PASS ${base || '/'} ${viewport.width}x${viewport.height}: ${states.length} states, HTMX, keyboard, contrast, assets, screenshots ${directory}`);
      await page.close();
    }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
