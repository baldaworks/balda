const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { chromium } = require('playwright');
const [origin, base, widthString, source, ref, controller] = process.argv.slice(2);
const width = Number(widthString);
const url = route => `${origin}${base}${route}`;
const detail = '/plugins/installed/runtime-demo';
const market = '/plugins/marketplaces/definitions/new';
const screenshotDir = process.env.BALDA_PLUGINS_RUNTIME_SCREENSHOTS || '/tmp/balda-plugins-screens/application';
async function stage(action) {
  const response = await fetch(`${controller}?action=${encodeURIComponent(action)}`, { method:'POST' });
  assert.equal(response.status, 200, `fixture checkpoint ${action}`);
  return response.json();
}
async function login(page, name) {
  await page.goto(url('/login'));
  await page.getByLabel('Username').fill(name);
  await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct-horse-battery');
  await Promise.all([page.waitForURL(url('/overview')), page.getByRole('button',{name:'Sign in',exact:true}).click()]);
}
async function settle(page, action) {
  const completion = page.evaluate(() => new Promise(resolve => {const timer=setTimeout(()=>resolve(false),15000);document.addEventListener('htmx:afterSettle',()=>{clearTimeout(timer);resolve(true);},{once:true});}));
  await action();
  assert.equal(await completion,true,`HTMX did not settle at ${new URL(page.url()).pathname}`);
  await page.waitForLoadState('networkidle');
}
async function mutate(page, name, route, expected=204, within=page) {
  let response;
  await settle(page, async()=> {
    [response] = await Promise.all([
      page.waitForResponse(r=>r.request().method()==='POST' && new URL(r.url()).pathname===`${base}${route}`),
      within.getByRole('button',{name,exact:true}).click(),
    ]);
    assert.equal(response.status(),expected,`${name} HTTP status`);
    assert.equal(response.headers()['cache-control']?.includes('no-store'),true,`${name} no-store`);
    assert.equal((await response.text()).includes(source),false,`${name} does not reflect transient Source`);
  });
}
async function snapshot(page,name) {
  assert.equal(await page.locator('[data-plugin-source]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'Source absent before screenshot');
  assert.equal(await page.locator('input[type=password]').count(),0,'no credential form in screenshot');
  await page.locator('main').focus();
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  await fs.mkdir(screenshotDir,{recursive:true});
  await page.screenshot({path:path.join(screenshotDir,`${base?'prefixed':'root'}-${width}-${name}.png`),fullPage:true});
}
async function open(page,route) {await page.goto(url(route));await page.locator('main').waitFor();}
async function assertState(expected) {
 const state=await stage('status');
 for(const [key,value] of Object.entries(expected))assert.equal(state[key],value,`durable ${key}`);
 if('enabled' in expected || expected.installed===false){
 const selected=expected.installed!==false && expected.enabled;
 assert.equal(state.catalogSelected,selected,'real catalog selection follows browser action');
 for(const key of ['catalogCommands','catalogSkills','catalogMCPServers'])assert.equal(state[key],selected?1:0,`real ${key}`);
 if(selected)assert.equal(state.catalogRevision,state.revision,'real catalog uses exact accepted revision');
 }
 return state;
}
async function install(page,name) {
  await open(page,`/plugins/install?marketplace=new&plugin=${name}`);
  assert.equal(await page.locator('input[name=candidate_token]').inputValue()!=='',true,'review token exists');
  await mutate(page,'Install plugin','/plugins/install');
  await page.waitForURL(url(`/plugins/installed/${name}`));
  await page.getByRole('heading',{name:'Plugin detail',exact:true}).waitFor();
}
(async()=>{
 console.log(`START Plugins application ${new Date().toISOString()} mount=${base||'/'} viewport=${width} source=${ref?'git':'local'}`);
 const browser=await chromium.launch({headless:true});
 try {
  const context=await browser.newContext({viewport:{width,height:width===390?844:900}});
  const page=await context.newPage();
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await page.goto(url('/plugins'));
  await page.getByRole('link',{name:'Sign in again',exact:true}).click();
  await page.waitForURL(url('/login'));
  assert.equal(new URL(page.url()).pathname,`${base}/login`,'anonymous redirected to normal login');
  await login(page,'operator');
  assert.equal(await page.locator('[data-nav-link]').filter({hasText:/^Plugins$/}).count(),0,'operator has no Plugins navigation');
  assert.equal((await page.goto(url('/plugins'))).status(),403,'operator cannot open Plugins');
  await page.goto(url('/overview'));
  const operatorCSRF=await page.locator('input[name=csrf_token]').first().inputValue();
  assert.equal((await page.request.post(url('/plugins/marketplaces'),{headers:{Origin:origin},form:{csrf_token:operatorCSRF,name:'operator-denied',source},maxRedirects:0})).status(),403,'authenticated operator POST denied');
  await Promise.all([page.waitForURL(url('/login')),page.getByRole('button',{name:'Sign out',exact:true}).click()]);
  await login(page,'administrator');
  await open(page,'/plugins');
  await snapshot(page,'empty');console.log('checkpoint: normal authenticated admin/operator access');
  const oldAccess=(await context.cookies()).find(c=>c.name==='balda_access').value;
  await context.clearCookies({name:'balda_access'});
  const refreshed=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname===base+'/auth/session/refresh');
  await page.goto(url('/plugins'));
  const refreshResponse=await refreshed;
  await page.waitForLoadState('networkidle');
  console.log('refresh evidence:',JSON.stringify({status:refreshResponse.status(),path:new URL(page.url()).pathname,cookies:(await context.cookies()).map(c=>c.name)}));
  assert.equal(new URL(page.url()).pathname,base+'/plugins','ordinary refresh returns requested Plugins page');
  await page.getByRole('heading',{name:'Installed plugins',exact:true}).waitFor();
  const newAccess=(await context.cookies()).find(c=>c.name==='balda_access').value;
  assert.notEqual(newAccess,oldAccess,'ordinary refresh rotates access credential');
  assert.equal(new URL(page.url()).pathname.includes('step-up'),false,'refresh does not require passkey');
  await settle(page,()=>page.getByRole('link',{name:'Marketplaces',exact:true}).click());
  assert.deepEqual((await page.locator('.app-sidebar [data-nav-link][aria-current=page]').allTextContents()).map(s=>s.trim()),['Plugins'],'settled nested navigation selects Plugins');
  await settle(page,()=>page.getByRole('link',{name:'Add marketplace',exact:true}).click());
  const sourceSecrets=['source-secret-canary','query-secret-canary','fragment-secret-canary'];
  const credentialSource='https://synthetic-user:source-secret-canary@plugins.example.test/repo.git?token=query-secret-canary#fragment-secret-canary';
  await page.getByLabel(/^Name(?: \(required\))?$/).fill('redaction');
  await page.getByLabel(/^Source(?: \(required\))?$/).fill(credentialSource);
  await mutate(page,'Add marketplace','/plugins/marketplaces');
  await page.waitForURL(url('/plugins/marketplaces/definitions/redaction'));
  for(const secret of sourceSecrets) assert.equal((await page.content()).includes(secret),false,'accepted definition does not reflect source secret');
  await snapshot(page,'safe-remote-definition');
  await page.goBack();await page.waitForLoadState('networkidle');
  assert.equal(await page.locator('[data-plugin-source]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'history never restores credential-bearing Source');
  assert.equal(await page.evaluate(secrets=>Object.values(sessionStorage).every(v=>secrets.every(secret=>!v.includes(secret))),sourceSecrets),true,'HTMX history excludes source secrets');
  await open(page,'/plugins/marketplaces/definitions/redaction');
  await page.getByLabel(/^Confirm removing this marketplace\.(?: \(required\))?$/).check();
  await mutate(page,'Remove marketplace','/plugins/marketplaces/definitions/redaction/remove');
  await page.waitForURL(url('/plugins/marketplaces'));
  await settle(page,()=>page.getByRole('link',{name:'Add marketplace',exact:true}).click());

  await page.getByLabel(/^Name(?: \(required\))?$/).fill('..');
  await page.getByLabel(/^Source(?: \(required\))?$/).fill(source);
  if(ref){await page.getByLabel('Ref',{exact:true}).fill(ref);await page.getByLabel('Sparse',{exact:true}).fill('.agents/plugins\nplugins');}
  await page.locator('form[data-plugin-marketplace-create]').evaluate(form=>form.noValidate=true);
  await mutate(page,'Add marketplace','/plugins/marketplaces',400);
  assert.equal(await page.getByLabel(/^Source(?: \(required\))?$/).inputValue(),'','invalid submission clears Source');
  await page.getByLabel(/^Name(?: \(required\))?$/).fill('new');
  await page.getByLabel(/^Source(?: \(required\))?$/).fill(source);
  if(ref){await page.getByLabel('Ref',{exact:true}).fill(ref);await page.getByLabel('Sparse',{exact:true}).fill('.agents/plugins\nplugins');}
  await mutate(page,'Add marketplace','/plugins/marketplaces');
  await page.waitForURL(url(market));
  await page.getByRole('heading',{name:'Marketplace definition',exact:true}).waitFor();
  await snapshot(page,'marketplace-new');
  await page.goBack();await page.locator('main').waitFor();
  assert.equal(await page.locator('[data-plugin-source]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'browser history clears Source');
  await open(page,market);
  await mutate(page,'Refresh marketplace',`${market}/refresh`);
  await page.getByRole('heading',{name:'Discovered plugins',exact:true}).waitFor();
  await snapshot(page,'discovered');console.log('checkpoint: marketplace Add/Refresh and Source clearing');
  await install(page,'runtime-demo');
  await assertState({installed:true,version:'1.0.0',enabled:true,revisionCount:1,marketplace:'new'});
  await stage('pin');
  assert.equal(await page.getByText('<img src=x onerror=window.pluginInjected=true>',{exact:true}).count(),1,'command description is visible escaped text');
  assert.equal(await page.locator('main img').count(),0,'escaped description cannot create element');
  assert.equal(await page.evaluate(()=>window.pluginInjected===true),false);
  await snapshot(page,'installed-v1');console.log('checkpoint: browser install and real session pin');
  for(const name of ['install','marketplaces']) {
    await install(page,name);
    assert.equal(new URL(page.url()).pathname,`${base}/plugins/installed/${name}`,'valid colliding name resolves exact detail');
    await page.getByLabel(new RegExp(`^Confirm removing ${name}\\.(?: \\(required\\))?$`)).check();
    await mutate(page,'Remove plugin',`/plugins/installed/${name}/remove`);
    await page.waitForURL(url('/plugins'));
  }
  await open(page,detail);
  const csrf=await page.locator('input[name=csrf_token]').first().inputValue();
  const expected=await page.locator('input[name=expected_state]').first().inputValue();
  assert.equal((await page.request.post(url(`${detail}/disable`),{headers:{Origin:origin},form:{csrf_token:'bad',expected_state:expected,confirm:'yes'},maxRedirects:0})).status(),403,'CSRF rejection');
  assert.equal((await page.request.post(url(`${detail}/disable`),{headers:{Origin:'https://foreign.example.test'},form:{csrf_token:csrf,expected_state:expected,confirm:'yes'},maxRedirects:0})).status(),403,'cross-origin rejection');
  assert.equal((await page.request.post(url(`${detail}/disable`),{headers:{Origin:origin},form:{csrf_token:csrf,expected_state:expected},maxRedirects:0})).status(),400,'missing confirmation rejected');
  await assertState({enabled:true,revisionCount:1});
  const disableForm=page.locator(`form[action="${base}${detail}/disable"]`);
  await disableForm.getByRole('link',{name:'Cancel',exact:true}).click();await page.waitForLoadState('networkidle');
  await assertState({enabled:true});
  await stage('chat-disable');
  await page.getByLabel(/^Confirm disabling runtime-demo for new sessions\.(?: \(required\))?$/).check();
  await mutate(page,'Disable plugin',`${detail}/disable`,409);
  await page.getByRole('link',{name:'Reopen plugin',exact:true}).click();await page.waitForLoadState('networkidle');
  await page.getByRole('button',{name:'Enable plugin',exact:true}).waitFor();
  await snapshot(page,'conflict-reopened-disabled');
  await mutate(page,'Enable plugin',`${detail}/enable`);
  await stage('version2');
  await open(page,market);await mutate(page,'Refresh marketplace',`${market}/refresh`);
  await open(page,`${detail}/upgrade`);await snapshot(page,'upgrade-review');
  const lockedMarketplace=await page.locator('dt').filter({hasText:/^Marketplace$/}).last().locator('xpath=following-sibling::dd[1]').textContent();
  assert.equal(lockedMarketplace.trim(),'new','upgrade uses configured locked marketplace');
  await mutate(page,'Upgrade plugin',`${detail}/upgrade`);
  await assertState({version:'2.0.0',enabled:true,revisionCount:2});
  await stage('audit-fail');
  await page.getByLabel(/^Confirm disabling runtime-demo for new sessions\.(?: \(required\))?$/).check();
  await mutate(page,'Disable plugin',`${detail}/disable`,503);
  await snapshot(page,'audit-failure');await stage('audit-clear');
  await open(page,detail);await assertState({version:'2.0.0',enabled:true,revisionCount:2,pending:0});
  await stage('complete-fail');
  await page.getByLabel(/^Confirm disabling runtime-demo for new sessions\.(?: \(required\))?$/).check();
  await mutate(page,'Disable plugin',`${detail}/disable`,503);
  await page.getByText('Accepted; recovery pending',{exact:false}).waitFor();
  await assertState({enabled:false,pending:1});
  assert.equal(await page.getByRole('button',{name:'Enable plugin',exact:true}).count(),0,'accepted pending has no mutation replay');
  await snapshot(page,'accepted-pending');
  await stage('recover');await open(page,detail);await assertState({enabled:false,pending:0});
  const rollback=page.locator(`form[action="${base}${detail}/rollback"]`).first();
  const retained=await rollback.locator('input[name=revision]').inputValue();assert.notEqual(retained,'');
  await rollback.getByRole('checkbox').check();await mutate(page,'Rollback',`${detail}/rollback`,204,rollback);
  await assertState({version:'1.0.0',enabled:false,revisionCount:2});
  await mutate(page,'Enable plugin',`${detail}/enable`);
  await stage('malformed');await open(page,market);
  const discoveryReviews=page.locator('.plugins-discovery-table').getByRole('link',{name:'Review install',exact:true});
  assert.equal(await discoveryReviews.count(),2,'successful discovery offers review for both uninstalled candidates');
  await mutate(page,'Refresh marketplace',`${market}/refresh`,503);
  assert.equal(await discoveryReviews.count(),0,'failed refresh cannot review stale candidates');
  await snapshot(page,'failed-refresh');
  await stage('restore-source');await open(page,market);await mutate(page,'Refresh marketplace',`${market}/refresh`);
  assert.equal(await discoveryReviews.count(),2,'successful refresh restores review for both uninstalled candidates');
  await open(page,detail);await stage('catalog-block');
  await page.getByLabel(/^Confirm disabling runtime-demo for new sessions\.(?: \(required\))?$/).check();
  const blockedMutation=mutate(page,'Disable plugin',`${detail}/disable`,403);
  // The controller waits for actual entry to the production catalog gate.
  await stage('revoke-blocked');await blockedMutation;
  await assertState({enabled:true,pending:0});
  await snapshot(page,'revoked');
  await login(page,'administrator');await open(page,detail);
  await page.getByLabel(/^Confirm removing runtime-demo\.(?: \(required\))?$/).check();await mutate(page,'Remove plugin',`${detail}/remove`);
  await page.waitForURL(url('/plugins'));await assertState({installed:false,revisionCount:2,pending:0});
  await open(page,market);await page.getByLabel(/^Confirm removing this marketplace\.(?: \(required\))?$/).check();await mutate(page,'Remove marketplace',`${market}/remove`);
  await page.waitForURL(url('/plugins/marketplaces'));await snapshot(page,'final-marketplaces');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'no viewport overflow');
  assert.deepEqual(errors,[],'no browser errors');
  await context.close();
  console.log(`END Plugins application ${new Date().toISOString()} mount=${base||'/'} viewport=${width}: authenticated real-owner lifecycle, fences, pinned state and failure outcomes passed`);
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
