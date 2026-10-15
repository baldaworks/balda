const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { assertPlaceholderDistinct } = require('./form-color.cjs');
const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const screenshots = process.env.BALDA_SKILLS_LAYOUT_SCREENSHOTS || '/tmp/balda-skills-screens/layout';
const states = [['skills',200], ['skills-pending-inventory',200], ['skills-empty',200], ['skills-long',200], ['skills/install',200], ['skills-review',200], ['skills-update',200], ['skills/managed/managed-synthetic',200], ['skills-unmanaged',200], ['skills-plugin',200], ['skills-disabled',200], ['skills-detail-long',200], ['skills-invalid',400], ['skills-conflict',409], ['skills-pending',503], ['skills-unavailable',503]];
const viewports = [{width:1440,height:900},{width:1024,height:768},{width:768,height:1024},{width:390,height:844}];
async function settleClick(page, locator) {
 await Promise.all([page.evaluate(() => new Promise(resolve => document.body.addEventListener('htmx:afterSettle', () => resolve(), {once:true}))),locator.click()]);
 await page.locator('main#main-content').focus();
 await page.evaluate(() => window.scrollTo(0,0));
}
(async () => {
 const browser = await chromium.launch({headless:true});
 try {
  for (const viewport of viewports) {
   const page = await browser.newPage({viewport});
   const failures=[];
   page.on('pageerror',e=>failures.push(e.message));
   page.on('requestfailed',r=>failures.push(r.url()));
   page.on('response',r=>{if(r.url().includes('/assets/') && r.status()>=400) failures.push(r.url());});
   const directory=path.join(screenshots,base?'prefixed':'root',`${viewport.width}x${viewport.height}`);fs.mkdirSync(directory,{recursive:true});
   for(const [state,status] of states) {
    const label=`${base||'/'} ${viewport.width} ${state}`;
    const response=await page.goto(`${origin}${prefix}/${state}`);
    assert.equal(response.status(),status,`${label} status`);
    await page.locator('main#main-content').waitFor();
    assert.equal(await page.locator('main#main-content').count(),1,`${label} one main`);
    assert.equal(await page.locator('main').getAttribute('hx-history'),'false',`${label} history`);
    assert.match(response.headers()['cache-control'],/no-store/,`${label} no-store`);
    assert.deepEqual((await page.locator('.app-sidebar [aria-current="page"]').allTextContents()).map(s=>s.trim()),['Skills'],`${label} active navigation`);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=document.documentElement.clientWidth+1),`${label} document overflow`);
    if (await page.locator('.skills-table').count()) {
      const readable=await page.locator('.skills-table').evaluate(table => [...table.tBodies[0].rows].every(row => {
       if(row.cells.length!==4) return true;
       return [2,3].every(index => {
        const cell=row.cells[index], text=index===3?cell.querySelector('a')?.firstChild:cell.firstChild;
        if(!text) return true;
        const range=document.createRange();range.selectNodeContents(text);
        const bounds=cell.getBoundingClientRect();
        return [...range.getClientRects()].every(rect=>rect.left>=bounds.left-1 && rect.right<=bounds.right+1) && (index!==3 || range.getBoundingClientRect().height<parseFloat(getComputedStyle(cell).lineHeight)*1.5);
       });
      }));
      assert.ok(readable,`${label} State and Actions text remain within their cells`);
      if (viewport.width===390) assert.ok(await page.locator('.table-responsive').evaluate(e=>e.scrollWidth>e.clientWidth),`${label} table scroll contained`);
    }
    const fragment=await page.request.get(`${origin}${prefix}/${state}`,{headers:{'HX-Request':'true','HX-Target':'main-content'}});
    assert.equal(fragment.status(),status,`${label} fragment status`);
    const html=await fragment.text();assert.equal((html.match(/id="main-content"/g)||[]).length,1,`${label} fragment main`);assert.ok(!/<html|<body|class="app-sidebar"/.test(html),`${label} fragment shell`);
    const history=await page.request.get(`${origin}${prefix}/${state}`,{headers:{'HX-Request':'true','HX-Target':'main-content','HX-History-Restore-Request':'true'}});
    assert.ok((await history.text()).includes('<!doctype html>'),`${label} history full document`);
    if(await page.locator('#skill-source').count()) {
     assert.equal(await page.getByLabel(/^Source/).inputValue(),'');
     for(const id of ['skill-source','skill-ref','skill-path']) await assertPlaceholderDistinct(page.locator(`#${id}`),label);
     await page.getByLabel(/^Source/).focus();assert.notEqual(await page.getByLabel(/^Source/).evaluate(e=>getComputedStyle(e).boxShadow),'none',`${label} focus`);
    }
    if(state==='skills-detail-long') {assert.equal(await page.locator('main script').count(),0,`${label} escaped descriptions`);assert.ok((await page.locator('main').innerText()).includes('<script>synthetic escaped content</script>'));}
    if(state==='skills-pending') {assert.equal(await page.getByRole('button',{name:'Recover publication',exact:true}).count(),1);assert.equal(await page.getByRole('button',{name:'Review update',exact:true}).count(),0);}
    await page.screenshot({path:path.join(directory,`${state.replaceAll('/','-')}.png`),fullPage:true});
    if (state==='skills-pending-inventory' && viewport.width===390) {
     await page.locator('.table-responsive').evaluate(e=>{e.scrollLeft=e.scrollWidth;});
     await page.screenshot({path:path.join(directory,'skills-pending-inventory-scroll-right.png'),fullPage:true});
     await page.locator('.table-responsive').evaluate(e=>{e.scrollLeft=0;});
    }
    assert.deepEqual(failures,[],`${label} browser errors`);
   }
   await page.goto(`${origin}${prefix}/skills`);
   assert.equal(await page.getByRole('link',{name:'Inspect review from user-skill/managed-synthetic',exact:true}).count(),1,'first managed identity');
   assert.equal(await page.getByRole('link',{name:'Inspect review from user-skill/managed-other-synthetic',exact:true}).count(),1,'duplicate managed identity');
   await settleClick(page,page.getByRole('link',{name:'Install skill',exact:true}));
   await page.getByRole('heading',{name:'Install skill',exact:true}).waitFor();
   await page.getByLabel(/^Source/).fill('synthetic-private-source');
   await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pagehide')));
   assert.equal(await page.getByLabel(/^Source/).inputValue(),'','pagehide clears transient source');
   await settleClick(page,page.getByRole('link',{name:'Cancel',exact:true}));
   await settleClick(page,page.getByRole('link',{name:'Inspect review from user-skill/managed-synthetic',exact:true}));
   assert.equal(await page.getByRole('heading',{name:'Skill details',exact:true}).count(),1);
   await page.getByLabel(/^Confirm disabling this skill for new sessions/).check();
   await page.getByLabel(/^Confirm removing this managed skill/).check();
   await page.evaluate(url=>new Promise(resolve=>{document.body.addEventListener('htmx:afterSettle',()=>resolve(),{once:true});htmx.ajax('GET',url,{target:'#main-content',swap:'outerHTML'});}),`${prefix}/skills-conflict`);
   assert.equal(await page.locator('main#main-content').count(),1,'actual 409 HTMX one main');
   assert.equal(await page.getByRole('alert').filter({hasText:'Skills changed'}).count(),1,'actual 409 feedback');
   await settleClick(page,page.getByRole('link',{name:'Reopen skill',exact:true}));
   assert.equal(await page.getByRole('heading',{name:'Skill details',exact:true}).count(),1,'exact reopen');
   assert.deepEqual((await page.locator('.app-sidebar [aria-current="page"]').allTextContents()).map(s=>s.trim()),['Skills'],'nested active navigation');
   if(viewport.width<1024) {await page.getByRole('button',{name:'Toggle navigation',exact:true}).click();await page.locator('.app-sidebar').waitFor();await page.keyboard.press('Escape');}
   assert.deepEqual(failures,[],'navigation failures');
   console.log(`PASS ${base||'/'} ${viewport.width}x${viewport.height}: ${states.length} states, fragments, history, settled HTMX errors, focus, screenshots`);
   await page.close();
  }
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
