const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');
const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_CHAT_ARCHIVE_LAYOUT_SCREENSHOTS || '/tmp/balda-chat-archive-layout';
const states = [
 ['chat-sessions', 200], ['chat-sessions?cursor=synthetic-older&q=Before&channel=telegram&status=ended', 200],
 ['chat-sessions?q=missing', 200], ['chat-sessions-empty', 200],
 ...['before', 'after', 'closed', 'group', 'partial', 'pending', 'uncertain', 'unknown', 'empty', 'long'].map(state => [`chat-sessions/${state}`, 200]),
 ['chat-sessions/older?list_cursor=synthetic-older&q=Before&channel=telegram&status=ended', 200],
 ['chat-sessions/older?cursor=synthetic-messages&list_cursor=synthetic-older&q=Before&channel=telegram&status=ended', 200],
 ['chat-sessions-invalid', 400], ['chat-sessions-forbidden', 403], ['chat-sessions-missing', 404], ['chat-sessions-unavailable', 503],
];
const viewports = [{width:1440,height:900}, {width:1024,height:768}, {width:768,height:1024}, {width:390,height:844}];
const current = async page => (await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(s => s.trim());
async function shell(page, label) {
 assert.equal(await page.locator('main#main-content').count(), 1, `${label} single main`);
 assert.equal(await page.locator('main').getAttribute('hx-history'), 'false', `${label} private history`);
 assert.deepEqual(await current(page), ['Chat sessions'], `${label} active sidebar parent`);
 assert.equal(await page.locator('.app-sidebar .nav-section').filter({has:page.locator('[aria-current="page"]')}).locator('.nav-section-title').textContent(), 'Workspace', `${label} group`);
 assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), `${label} document overflow`);
 const topbar = await page.locator('.app-header').boundingBox();
 assert.ok(topbar && topbar.y >= -1 && topbar.x >= -1 && topbar.x + topbar.width <= page.viewportSize().width + 1, `${label} topbar alignment`);
}
(async () => {
 const browser = await chromium.launch({headless:true});
 try {
  for (const viewport of viewports) {
   const page = await browser.newPage({viewport});
   page.setDefaultTimeout(10000);
   const failures = [];
   page.on('pageerror', e => failures.push(e.message));
   page.on('console', m => { if (m.type() === 'error' && !m.text().includes('Failed to load resource')) failures.push(m.text()); });
   page.on('requestfailed', r => failures.push(`${r.url()}: ${r.failure()?.errorText}`));
   page.on('response', r => {if(r.url().includes('/assets/') && r.status() >= 400) failures.push(`asset ${r.url()} ${r.status()}`);});
   const directory = path.join(screenshots, base ? 'balda-backoffice' : 'root', `${viewport.width}x${viewport.height}`);
   fs.mkdirSync(directory,{recursive:true});
   for (const [state,status] of states) {
    const label = `${base || '/'} ${viewport.width} ${state}`;
    const response = await page.goto(`${origin}${prefix}/${state}`);
    assert.equal(response.status(),status,`${label} status`);
    assert.match(response.headers()['cache-control'],/no-store/,`${label} no-store`);
    await shell(page,label);
    assert.equal(await page.evaluate(() => window.syntheticArchiveExecuted),undefined,`${label} harmless text`);
    for (const asset of await page.locator('script[src],link[rel="stylesheet"]').evaluateAll(es => es.map(e => e.getAttribute('src') || e.getAttribute('href')))) assert.ok(asset.startsWith(`${base}/assets/`),`${label} asset ${asset}`);
    for (const target of await page.locator('main a[href],main form[action]').evaluateAll(es => es.map(e => e.getAttribute('href') || e.getAttribute('action')))) assert.ok(target.startsWith(`${prefix}/`),`${label} QA target ${target}`);
    const fragment = await page.request.get(`${origin}${prefix}/${state}`,{headers:{'HX-Request':'true','HX-Target':'main-content'}});
    assert.equal(fragment.status(),status,`${label} fragment status`);
    const html = await fragment.text();
    assert.equal((html.match(/id="main-content"/g)||[]).length,1,`${label} fragment main`);
    assert.ok(!/<html|<body|class="app-sidebar"/.test(html),`${label} fragment shell`);
    assert.match(fragment.headers()['cache-control'],/no-store/,`${label} fragment no-store`);
    if (await page.locator('#archive-query').count()) {
     await assertPlaceholderDistinct(page.locator('#archive-query'),label);
     assert.ok(await page.locator('main form').evaluate(form => [...form.querySelectorAll('input,select,button,a')].every(e => e.getBoundingClientRect().right <= form.getBoundingClientRect().right)),`${label} filter controls contained`);
     await page.locator('#archive-query').focus();
     assert.notEqual(await page.locator('#archive-query').evaluate(e => getComputedStyle(e).boxShadow),'none',`${label} visible focus`);
     await page.keyboard.press('Tab');
     assert.equal(await page.locator('#archive-channel').evaluate(e => e === document.activeElement),true,`${label} keyboard filter order`);
    }
    if (state === 'chat-sessions') assert.ok(await page.getByRole('columnheader').evaluateAll(es => es.every(e => e.scrollWidth <= e.clientWidth)),`${label} table headings contained`);
    if (state === 'chat-sessions' && viewport.width < 992) {
     const table = page.locator('.table-responsive');
     assert.ok(await table.evaluate(e => e.scrollWidth > e.clientWidth),`${label} contained table scroll`);
     await table.evaluate(e => {e.scrollLeft = 100;});
     assert.ok(await table.evaluate(e => e.scrollLeft > 0),`${label} table scrollable`);
     await table.evaluate(e => {e.scrollLeft = 0;});
    }
    if (state === 'chat-sessions/long') {
     const text = page.locator('.archive-text').first();
     assert.equal(await text.evaluate(e => getComputedStyle(e).whiteSpace),'pre-wrap',`${label} multiline`);
     assert.ok(await text.evaluate(e => e.scrollWidth <= e.clientWidth+1),`${label} long text contained`);
     assert.ok((await text.textContent()).includes('<script>'),`${label} escaped script visible`);
     assert.equal(await page.locator('.archive-attachment a').count(),0,`${label} attachment label`);
     await page.evaluate(() => window.scrollTo(0,document.body.scrollHeight));
     await shell(page,`${label} scrolled`);
     await page.evaluate(() => window.scrollTo(0,0));
    }
    if (state === 'chat-sessions/group') {
     assert.ok((await page.locator('main').textContent()).includes('Multiple or unknown speakers'),`${label} group ownership`);
     assert.deepEqual(await page.locator('.archive-message strong').allTextContents(),['Synthetic speaker one','Balda','Synthetic speaker two'],`${label} distinct speakers`);
    }
    await page.screenshot({path:path.join(directory,`${state.replaceAll('/','-').replaceAll('?','-').replaceAll('&','-').replaceAll('=','-')}.png`),fullPage:true});
   }
   await page.goto(`${origin}${prefix}/chat-sessions?q=Before&channel=telegram&status=ended`);
   await page.getByRole('link',{name:'Older sessions',exact:true}).click();
   await page.getByRole('link',{name:'Older lifetime',exact:true}).waitFor();
   assert.equal(new URL(page.url()).searchParams.get('cursor'),'synthetic-older','older inventory cursor');
   await page.getByRole('link',{name:'Older lifetime',exact:true}).click();
   await page.getByRole('heading',{name:'Session chat',exact:true}).waitFor();
   await page.waitForFunction(() => document.activeElement?.id === 'main-content');
   assert.equal(new URL(page.url()).searchParams.get('list_cursor'),'synthetic-older','list position in detail');
   await page.getByRole('link',{name:'Older messages',exact:true}).click();
   await page.getByText('Older synthetic message',{exact:true}).waitFor();
   await shell(page,'older HTMX transcript');
   await page.screenshot({path:path.join(directory,'older-messages-htmx.png'),fullPage:true});
   await page.getByRole('link',{name:'Back to Chat sessions',exact:true}).click();
   await page.getByRole('link',{name:'Older lifetime',exact:true}).waitFor();
   const back = new URL(page.url());
   for (const [key,value] of Object.entries({cursor:'synthetic-older',q:'Before',channel:'telegram',status:'ended'})) assert.equal(back.searchParams.get(key),value,`back preserves ${key}`);
   await page.goBack();
   await page.getByText('Older synthetic message',{exact:true}).waitFor();
   await shell(page,'history transcript');
   await page.goForward();
   await page.getByRole('link',{name:'Older lifetime',exact:true}).waitFor();
   const toggle = page.getByRole('button',{name:'Toggle navigation'});
   if (viewport.width < 992) {
    await toggle.click();
    assert.equal(await page.locator('.app-main').evaluate(e=>e.inert),true,'mobile overlay protects main');
    const links = page.locator('.sidebar-menu a'); await links.last().focus(); await page.keyboard.press('Tab');
    assert.equal(await links.first().evaluate(e=>e===document.activeElement),true,'sidebar keyboard focus trap');
    await page.keyboard.press('Escape');
    assert.equal(await toggle.getAttribute('aria-expanded'),'false','Escape closes sidebar');
    assert.equal(await toggle.evaluate(e=>e===document.activeElement),true,'sidebar focus restored');
   }
   await shell(page,'after navigation');
   assert.deepEqual(failures,[],'browser and assets');
   await page.close();
   // Native navigation uses the same production template without HTMX/JavaScript.
   const native = await browser.newPage({viewport,javaScriptEnabled:false});
   await native.goto(`${origin}${prefix}/chat-sessions`);
   await native.getByRole('link',{name:'Before reset',exact:true}).click();
   await native.getByRole('link',{name:'Next session',exact:true}).click();
   await native.getByRole('link',{name:'Previous session',exact:true}).click();
   await native.getByRole('link',{name:'Back to Chat sessions',exact:true}).click();
   await shell(native,'native navigation');
   await native.close();
   console.log(`PASS ${base || '/'} ${viewport.width}x${viewport.height}: ${states.length} states, native/HTMX/history/pagination/focus/sidebar/assets; screenshots ${directory}`);
  }
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
