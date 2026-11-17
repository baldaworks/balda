const assert = require('node:assert/strict');
const { chromium } = require('playwright');

const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
// Production views only. The component-guide table is a small synthetic style sample,
// not a production data table or a measured horizontal-scroll candidate.
const routes = [
  'mcp', 'skills', 'chat-sessions', 'providers', 'webhooks', 'audit',
  'plugins-installed', 'plugins/marketplaces', 'plugins/marketplaces/definitions/new', 'plugins/installed/demo',
];
const viewports = [
  { width: 1440, height: 900 },
  { width: 1024, height: 768 },
  { width: 768, height: 1024 },
  { width: 390, height: 844 },
];

function contrast(first, second) {
  const luminance = color => {
    const channels = color.match(/[\d.]+/g).slice(0, 3).map(value => Number(value) / 255).map(value =>
      value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
  };
  const [lighter, darker] = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
}

async function checkRegion(page, region, label) {
  const state = await region.evaluate(element => ({
    label: element.getAttribute('aria-label'),
    describedBy: element.getAttribute('aria-describedby'),
    scrollable: element.scrollWidth > element.clientWidth + 1,
    tabIndex: element.getAttribute('tabindex'),
  }));
  assert.ok(state.label, `${label} has a specific accessible label`);

  const hint = page.locator(`#${await region.getAttribute('data-table-scroll-hint')}`);
  assert.equal(await hint.count(), 1, `${label} has one shared scroll cue`);
  if (!state.scrollable) {
    assert.doesNotMatch(state.label, /horizontally scrollable/i, `${label} has no false scroll announcement`);
    assert.equal(state.tabIndex, null, `${label} non-overflowing table is not an extra tab stop`);
    assert.equal(state.describedBy, null, `${label} non-overflowing table has no redundant cue reference`);
    assert.equal(await hint.isHidden(), true, `${label} non-overflowing cue is hidden`);
    return;
  }

  assert.equal(state.tabIndex, '0', `${label} overflowing table is keyboard reachable`);
  assert.equal(state.describedBy, await hint.getAttribute('id'), `${label} cue describes the region`);
  assert.equal(await hint.isVisible(), true, `${label} overflowing cue is visible`);
  assert.equal((await hint.textContent()).trim(), 'Scroll the table sideways to see all columns.', `${label} cue text`);
  const hintColors = await hint.evaluate(element => ({ color: getComputedStyle(element).color, background: getComputedStyle(element).backgroundColor }));
  assert.ok(contrast(hintColors.color, hintColors.background) >= 4.5, `${label} cue text meets contrast`);

  await page.keyboard.press('Tab');
  await region.focus();
  assert.equal(await region.evaluate(element => element === document.activeElement), true, `${label} receives keyboard focus`);
  assert.notEqual(await region.evaluate(element => getComputedStyle(element).outlineStyle), 'none', `${label} focus is visible`);
  const focusColors = await region.evaluate(element => ({ outline: getComputedStyle(element).outlineColor, background: getComputedStyle(element.closest('.card')).backgroundColor }));
  assert.ok(contrast(focusColors.outline, focusColors.background) >= 3, `${label} focus indicator meets contrast`);
  await page.keyboard.press('ArrowRight');
  await page.waitForFunction(element => element.scrollLeft > 0, await region.elementHandle());
  await region.evaluate(element => { element.scrollLeft = element.scrollWidth; });
  const reachesLastColumn = await region.evaluate(element => {
    const edge = element.getBoundingClientRect().right;
    const lastHeader = element.querySelector('thead th:last-child');
    return lastHeader && lastHeader.getBoundingClientRect().right <= edge + 1;
  });
  assert.equal(reachesLastColumn, true, `${label} reaches the rightmost column`);
  await region.evaluate(element => { element.scrollLeft = 0; });
}

async function waitForMeasuredTables(page) {
  await page.waitForFunction(() => [...document.querySelectorAll('[data-table-scroll-region]')].every(element => {
    const scrollable = element.scrollWidth > element.clientWidth + 1;
    return scrollable === (element.getAttribute('tabindex') === '0');
  }));
}

async function checkReplacementAndHistory(page, viewport) {
  await page.goto(`${origin}${prefix}/mcp`);
  await waitForMeasuredTables(page);
  if (viewport.width < 992) await page.getByRole('button', { name: 'Toggle navigation', exact: true }).click();
  const navigationSettled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('link', { name: 'Skills', exact: true }).click();
  await navigationSettled;
  const skillsHeading = page.getByRole('heading', { name: 'Skills', exact: true });
  await skillsHeading.waitFor();
  await waitForMeasuredTables(page);
  await checkRegion(page, page.locator('[data-table-scroll-region]'), `${base || '/'} ${viewport.width} HTMX replacement`);

  await page.goBack();
  await page.getByRole('heading', { name: 'MCP servers', exact: true }).waitFor();
  await waitForMeasuredTables(page);
  await checkRegion(page, page.locator('[data-table-scroll-region]'), `${base || '/'} ${viewport.width} history restore`);

  await page.goForward();
  await page.getByRole('heading', { name: 'Skills', exact: true }).waitFor();
  const form = page.locator('form[data-mutation-form]').first();
  await form.evaluate(element => {
    element.setAttribute('aria-busy', 'true');
    const status = element.querySelector('.mutation-status');
    if (status) status.hidden = false;
    document.dispatchEvent(new CustomEvent('htmx:historyRestore'));
  });
  await waitForMeasuredTables(page);
  assert.equal(await form.getAttribute('aria-busy'), null, 'history restore settles mutation feedback alongside table measurement');
  assert.equal(await form.locator('.mutation-status').isHidden(), true, 'mutation status stays independent from table cue');
  await checkRegion(page, page.locator('[data-table-scroll-region]'), `${base || '/'} ${viewport.width} mutation coexistence`);
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of viewports) {
      const page = await browser.newPage({ viewport });
      const failures = [];
      page.on('pageerror', error => failures.push(error.message));
      page.on('requestfailed', request => failures.push(`${request.url()}: ${request.failure()?.errorText}`));
      for (const route of routes) {
        const label = `${base || '/'} ${viewport.width}x${viewport.height} ${route}`;
        const response = await page.goto(`${origin}${prefix}/${route}`);
        assert.equal(response.status(), 200, `${label} status`);
        const body = await response.text();
        assert.ok(body.includes('data-table-scroll-hint hidden'), `${label} cue is server rendered`);
        const serverRegions = body.match(/<div class="table-responsive"[^>]*data-table-scroll-region[^>]*>/g) || [];
        assert.ok(serverRegions.length, `${label} server renders measured regions`);
        assert.ok(serverRegions.every(region => !region.includes('aria-describedby=') && !region.includes('tabindex=')), `${label} server leaves measured state to JavaScript`);
        const regions = page.locator('[data-table-scroll-region]');
        assert.ok(await regions.count(), `${label} exposes table regions`);
        const hintIDs = await regions.evaluateAll(elements => elements.map(element => element.dataset.tableScrollHint));
        assert.equal(new Set(hintIDs).size, hintIDs.length, `${label} uses a unique stable cue ID for every table`);
        await waitForMeasuredTables(page);
        for (let index = 0; index < await regions.count(); index++) {
          await checkRegion(page, regions.nth(index), `${label} table ${index + 1}`);
        }
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), `${label} has no document overflow`);
        if ((route === 'mcp' || route === 'plugins/marketplaces') && viewport.width === 390) {
          await page.setViewportSize({ width: 1440, height: 900 });
          await waitForMeasuredTables(page);
          await checkRegion(page, page.locator('[data-table-scroll-region]'), `${label} resized wide`);
          await page.setViewportSize(viewport);
          await waitForMeasuredTables(page);
          await checkRegion(page, page.locator('[data-table-scroll-region]'), `${label} resized narrow`);
        }
      }
      await checkReplacementAndHistory(page, viewport);
      assert.deepEqual(failures, [], `${base || '/'} ${viewport.width} browser errors`);
      await page.close();
    }
    console.log(`PASS ${base || '/'}: shared table scrolling at 390/768/1024/1440px`);
  } finally {
    await browser.close();
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
