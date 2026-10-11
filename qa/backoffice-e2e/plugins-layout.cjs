const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');

const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_PLUGINS_LAYOUT_SCREENSHOTS || '/tmp/balda-plugins-screens/layout';
const states = [
  ['plugins', 200], ['plugins-installed', 200], ['plugins/marketplaces', 200],
  ['plugins-marketplaces-empty', 200], ['plugins/marketplaces/new', 200],
  ['plugins/marketplaces/definitions/new', 200], ['plugins-marketplace-failed', 200],
  ['plugins-marketplace-long', 200], ['plugins-marketplace-invalid', 400],
  ['plugins-marketplace-conflict', 409], ['plugins-marketplace-unavailable', 503],
  ['plugins/installed/demo', 200], ['plugins/install', 200],
  ['plugins/installed/demo/upgrade', 200], ['plugins-install-empty', 200],
  ['plugins-disabled', 200], ['plugins-degraded', 200], ['plugins-pending-remove', 503],
  ['plugins-detail-long', 200], ['plugins-conflict', 409], ['plugins-unavailable', 503],
];
const viewports = [{ width: 1440, height: 900 }, { width: 1024, height: 768 },
  { width: 768, height: 1024 }, { width: 390, height: 844 }];

function labelled(page, name) {
  const label = name === 'Name' ? /^Name(?: \(required\))?$/ : name === 'Source' ? /^Source(?: \(required\))?$/ : name;
  return page.getByLabel(label, { exact: true });
}

(async () => {
  console.log(`START synthetic Plugins layout ${new Date().toISOString()} mount=${base || '/'}`);
  const browser = await chromium.launch({ headless: true });
  try {
    const probe = await browser.newPage({ viewport: {width:390,height:844} });
    const defects=[];
    await probe.goto(`${origin}${prefix}/plugins-installed`);
    const navigationSettled=probe.evaluate(()=>new Promise(resolve=>document.addEventListener('htmx:afterSettle',()=>resolve(),{once:true})));
    await probe.getByRole('link',{name:'Marketplaces',exact:true}).click();
    await navigationSettled;
    const activeNavigation=(await probe.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text=>text.trim());
    if(JSON.stringify(activeNavigation)!==JSON.stringify(['Plugins']))defects.push(`nested HTMX settled navigation=${JSON.stringify(activeNavigation)}, expected Plugins`);

    for (const scene of ['plugins/marketplaces','plugins/marketplaces/definitions/new']) {
      await probe.goto(`${origin}${prefix}/${scene}`);
      const whiteSpace=await probe.locator('main .preserve-lines').evaluate(element=>getComputedStyle(element).whiteSpace);
      if (whiteSpace!=='pre-wrap') defects.push(`${scene}: Sparse whitespace=${whiteSpace}, expected pre-wrap`);
    }
    for (const [scene,region,column,min] of [
      ['plugins/marketplaces','Marketplaces table, horizontally scrollable',1,192],
      ['plugins/marketplaces','Marketplaces table, horizontally scrollable',2,80],
      ['plugins/marketplaces','Marketplaces table, horizontally scrollable',3,160],
      ['plugins/marketplaces','Marketplaces table, horizontally scrollable',4,224],
      ['plugins/marketplaces/definitions/new','Discovered plugins table, horizontally scrollable',0,80],
      ['plugins/marketplaces/definitions/new','Discovered plugins table, horizontally scrollable',4,144],
      ['plugins/installed/demo','Retained revisions table, horizontally scrollable',1,160],
      ['plugins/installed/demo','Retained revisions table, horizontally scrollable',3,128],
      ['plugins/installed/demo','Retained revisions table, horizontally scrollable',4,220],
    ]) {
      await probe.goto(`${origin}${prefix}/${scene}`);
      const width=await probe.getByRole('region',{name:region,exact:true}).locator('tbody tr').first().locator('td').nth(column).evaluate(element=>element.clientWidth);
      if(width+1<min)defects.push(`${scene}: column${column} width=${width}, readable minimum=${min}`);
    }
    await probe.goto(`${origin}${prefix}/plugins-disabled`);
    const disabledHealth=await probe.locator('main').innerText();
    if(disabledHealth.includes('MCP attachments ready'))defects.push('plugins-disabled: unknown disabled revision still shows ready attachment counts');
    await probe.close();
    assert.deepEqual(defects,[], 'Sparse multiline and narrow-table readability');
    for (const viewport of viewports) {
      const page = await browser.newPage({ viewport, javaScriptEnabled: true });
      page.setDefaultTimeout(10000);
      const failures = [];
      page.on('pageerror', error => failures.push(error.message));
      page.on('console', message => { if (message.type() === 'error' && !/^Failed to load resource: the server responded with a status of (400|409|503)/.test(message.text())) failures.push(`console: ${message.text()}`); });
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
        assert.match(response.headers()['cache-control'], /no-store/, `${label} no-store`);
        assert.equal(await page.locator('main#main-content').count(), 1, `${label} single main`);
        assert.equal(await page.locator('main').getAttribute('hx-history'), 'false', `${label} private history`);
        assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Plugins'], `${label} active navigation`);
        assert.equal(await page.locator('.app-sidebar .nav-section').filter({ has: page.locator('[aria-current="page"]') }).locator('.nav-section-title').textContent(), 'Operations', `${label} navigation group`);
        assert.equal(await page.getByRole('navigation', { name: 'Plugins views' }).getByRole('link').count(), 2, `${label} local tabs`);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        await page.screenshot({ path: path.join(directory, `${state.replaceAll('/', '-')}${overflow > 1 ? '-overflow' : ''}.png`), fullPage: true });
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
        if (state === 'plugins-installed') assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Name', 'Version', 'Marketplace', 'Enabled', 'Actions'], `${label} installed columns`);
        if (['plugins/marketplaces','plugins/marketplaces/definitions/new'].includes(state)) {
          const lines=await page.locator('main .preserve-lines').evaluate(element=>{
            const text=element.firstChild;const split=text.textContent.indexOf('\n');const first=document.createRange();const second=document.createRange();
            first.setStart(text,0);first.setEnd(text,split);second.setStart(text,split+1);second.setEnd(text,text.textContent.length);
            return {whiteSpace:getComputedStyle(element).whiteSpace,first:first.getClientRects()[0].top,second:second.getClientRects()[0].top,lineHeight:parseFloat(getComputedStyle(element).lineHeight)};
          });
          assert.equal(lines.whiteSpace,'pre-wrap',`${label} Sparse multiline`);
          assert.ok(lines.second>=lines.first+lines.lineHeight-1,`${label} Sparse entries occupy separate lines`);
        }
        if (state === 'plugins/marketplaces') {
          assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Name', 'Source', 'Ref', 'Sparse', 'Discovery', 'Available plugins', 'Last successful refresh', 'Actions'], `${label} marketplace columns`);
          if(viewport.width===390){
            const region=page.getByRole('region',{name:'Marketplaces table, horizontally scrollable',exact:true});
            await region.focus();await page.keyboard.press('Tab');await page.keyboard.press('Tab');
            assert.equal(await region.getByRole('link',{name:'Open',exact:true}).evaluate(element=>element===document.activeElement),true,`${label} keyboard reaches marketplace action`);
            assert.ok(await region.evaluate(element=>element.scrollLeft>0),`${label} keyboard scrolls marketplace actions`);
            await page.screenshot({path:path.join(directory,'plugins-marketplace-inventory-actions-right.png'),fullPage:true});
          }
        }
        if (state === 'plugins/marketplaces/definitions/new') {
          assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Name', 'Version', 'Description', 'Installed', 'Actions'], `${label} discovery columns`);
          assert.equal(await page.getByRole('link', { name: 'Open installed', exact: true }).count(), 1, `${label} installed discovery action`);
          assert.equal(await page.getByRole('link', { name: 'Review install', exact: true }).count(), 1, `${label} available discovery action`);
          if(viewport.width===390){
            const region=page.getByRole('region',{name:'Discovered plugins table, horizontally scrollable',exact:true});
            await region.focus();await page.keyboard.press('Tab');
            assert.equal(await region.getByRole('link',{name:'Review install',exact:true}).evaluate(element=>element===document.activeElement),true,`${label} keyboard reaches discovered review`);
            const bounds=await region.evaluate(element=>{const link=element.querySelector('a').getBoundingClientRect();const wrapper=element.getBoundingClientRect();return {left:link.left,right:link.right,wrapperLeft:wrapper.left,wrapperRight:wrapper.right,scroll:element.scrollLeft};});
            assert.ok(bounds.scroll>0&&bounds.left>=bounds.wrapperLeft-1&&bounds.right<=bounds.wrapperRight+1,`${label} discovered review reachable inside table scroll`);
            await page.screenshot({path:path.join(directory,'plugins-discovered-actions-right.png'),fullPage:true});
          }
        }
        if (state === 'plugins/marketplaces/new') {
          for (const field of ['Name', 'Source']) await assertPlaceholderDistinct(labelled(page, field), `${label} ${field}`);
          assert.equal(await page.locator('[data-plugin-git-fields]').isVisible(), false, `${label} initial Git fields`);
          await labelled(page, 'Source').fill('https://example.test/synthetic.git');
          assert.equal(await labelled(page, 'Ref').isVisible(), true, `${label} Git fields`);
          for (const field of ['Ref', 'Sparse']) await assertPlaceholderDistinct(labelled(page, field), `${label} ${field}`);
          await page.screenshot({path:path.join(directory,'plugins-marketplace-git-fields.png'),fullPage:true});
          await labelled(page, 'Name').focus();
          await page.keyboard.press('Tab');
          assert.equal(await labelled(page, 'Source').evaluate(element => element === document.activeElement), true, `${label} keyboard Source`);
          assert.notEqual(await labelled(page, 'Source').evaluate(element => getComputedStyle(element).boxShadow), 'none', `${label} visible focus`);
          await labelled(page, 'Source').fill('');
          assert.equal(await page.locator('[data-plugin-git-fields]').isVisible(), false, `${label} cleared Git fields`);
        }
        if (state === 'plugins-disabled') assert.ok(!(await page.locator('main').innerText()).includes('MCP attachments ready'), `${label} disabled unknown does not inherit ready counts`);
        if (state === 'plugins-marketplace-failed') assert.equal(await page.getByRole('link', { name: 'Review install', exact: true }).count(), 0, `${label} failed refresh has no review`);
        if (state === 'plugins-pending-remove') {
          assert.equal(await page.locator('main form').count(), 0, `${label} removed pending cannot mutate`);
          assert.equal(await page.getByRole('link', { name: 'Reopen plugin', exact: true }).getAttribute('href'), `${prefix}/plugins/installed/demo`, `${label} recovery target`);
        }
        if (state === 'plugins-conflict') {
          assert.equal(await page.locator('main form').count(), 0, `${label} stale no blind replay`);
          assert.equal(await page.getByRole('link', { name: 'Reopen plugin', exact: true }).getAttribute('href'), `${prefix}/plugins/installed/demo`, `${label} stale target`);
        }
        if (state === 'plugins/installed/demo') {
          const confirms = page.locator('input[name="confirm"]');
          assert.equal(await confirms.count(), 3, `${label} disable rollback remove confirmations`);
          for (const checkbox of await confirms.all()) assert.equal(await checkbox.isChecked(), false, `${label} explicit confirmation`);
          assert.ok(await page.getByRole('heading', { name: 'Commands', exact: true }).count(), `${label} retained commands`);
          assert.ok(await page.getByRole('heading', { name: 'Skills', exact: true }).count(), `${label} retained skills`);
          assert.ok(await page.getByRole('heading', { name: 'MCP servers', exact: true }).count(), `${label} retained MCP`);
          if(viewport.width===390){
            const region=page.getByRole('region',{name:'Retained revisions table, horizontally scrollable',exact:true});
            await region.focus();await page.keyboard.press('Tab');
            assert.equal(await region.locator('input[name="confirm"]').evaluate(element=>element===document.activeElement),true,`${label} keyboard reaches rollback confirmation`);
            await page.keyboard.press('Tab');
            assert.equal(await region.getByRole('button',{name:'Rollback',exact:true}).evaluate(element=>element===document.activeElement),true,`${label} keyboard reaches rollback action`);
            const bounds=await region.evaluate(element=>{const button=element.querySelector('button').getBoundingClientRect();const wrapper=element.getBoundingClientRect();return {left:button.left,right:button.right,wrapperLeft:wrapper.left,wrapperRight:wrapper.right,scroll:element.scrollLeft};});
            assert.ok(bounds.scroll>0&&bounds.left>=bounds.wrapperLeft-1&&bounds.right<=bounds.wrapperRight+1,`${label} rollback remains reachable inside table scroll`);
            await page.screenshot({path:path.join(directory,'plugins-retained-actions-right.png'),fullPage:true});
          }
        }
        const revisions=page.getByRole('region',{name:'Retained revisions table, horizontally scrollable',exact:true});
        if(await revisions.count()) {
          const intact=await revisions.locator('tbody tr td:nth-child(4)').evaluateAll(elements=>elements.every(element=>{const range=document.createRange();range.selectNodeContents(element);return range.getClientRects().length===1;}));
          assert.ok(intact,`${label} ordinary retained status words stay intact`);
        }
        const discovered=page.getByRole('region',{name:'Discovered plugins table, horizontally scrollable',exact:true});
        if(await discovered.count()) {
          const intact=await discovered.locator('a').evaluateAll(elements=>elements.every(element=>{
            const walker=document.createTreeWalker(element,NodeFilter.SHOW_TEXT);
            while(walker.nextNode())for(const word of walker.currentNode.textContent.matchAll(/\S+/g)){
              const range=document.createRange();range.setStart(walker.currentNode,word.index);range.setEnd(walker.currentNode,word.index+word[0].length);
              if(range.getClientRects().length!==1)return false;
            }
            return true;
          }));
          assert.ok(intact,`${label} discovery action words stay intact`);
        }
        if (state.includes('long')) {
          const wrapped = await page.locator('main .text-break').evaluateAll(elements => elements.some(element => element.textContent.length > 300 && element.clientHeight > 40 && getComputedStyle(element).overflowWrap !== 'normal'));
          assert.ok(wrapped, `${label} readable long wrapping`);
        }
        assert.deepEqual(failures, [], `${label} browser and assets`);
      }
      await page.goto(`${origin}${prefix}/plugins-installed`);
      const marketplaceSettled=page.evaluate(()=>new Promise(resolve=>document.addEventListener('htmx:afterSettle',()=>resolve(),{once:true})));
      await page.getByRole('link', { name: 'Marketplaces', exact: true }).click();
      await marketplaceSettled;
      await page.getByRole('heading', { name: 'Marketplaces', exact: true }).waitFor();
      await page.waitForFunction(() => document.activeElement?.id === 'main-content');
      const addSettled=page.evaluate(()=>new Promise(resolve=>document.addEventListener('htmx:afterSettle',()=>resolve(),{once:true})));
      await page.getByRole('link', { name: 'Add marketplace', exact: true }).click();
      await addSettled;
      assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text=>text.trim()),['Plugins'],'settled nested editor retains Plugins navigation');
      await page.getByRole('heading', { name: 'Add marketplace', exact: true }).waitFor();
      await page.waitForFunction(() => document.activeElement?.id === 'main-content');
      await labelled(page, 'Source').fill('https://example.test/transient-synthetic.git');
      await page.getByRole('link', { name: 'Cancel', exact: true }).click();
      await page.getByRole('heading', { name: 'Marketplaces', exact: true }).waitFor();
      await page.goBack();
      await page.getByRole('heading', { name: 'Add marketplace', exact: true }).waitFor();
      assert.equal(await labelled(page, 'Source').inputValue(), '', 'history does not retain transient Source');
      assert.equal(await page.locator('[data-plugin-git-fields]').isVisible(), false, 'history clears Git applicability');
      assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()), ['Plugins'], 'history retains Plugins navigation');
      const toggle = page.getByRole('button', { name: 'Toggle navigation' });
      if (viewport.width < 992) {
        await toggle.click();
        assert.equal(await page.locator('.app-main').evaluate(element => element.inert), true, 'mobile navigation protects content');
        const links = page.locator('.sidebar-menu a');
        await links.last().focus();
        await page.keyboard.press('Tab');
        assert.equal(await links.first().evaluate(element => element === document.activeElement), true, 'sidebar keyboard trap');
        await page.keyboard.press('Escape');
        assert.equal(await toggle.getAttribute('aria-expanded'), 'false', 'Escape closes sidebar');
        assert.equal(await toggle.evaluate(element => element === document.activeElement), true, 'sidebar returns focus');
      }
      assert.deepEqual(failures, [], 'navigation browser failures');
      console.log(`PASS ${base || '/'} ${viewport.width}x${viewport.height}: ${states.length} states, full/HTMX/history/keyboard/contrast/assets; screenshots ${directory}`);
      await page.close();
    }
  } finally { await browser.close(); }
  console.log(`END synthetic Plugins layout ${new Date().toISOString()} mount=${base || '/'}`);
})().catch(error => { console.error(error); process.exitCode = 1; });
