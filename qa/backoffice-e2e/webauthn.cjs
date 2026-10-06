const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, 'localhost', 'isolated localhost RP required');
const password = 'correct horse battery staple';
const widths = [1440, 1024, 768, 390];
(async () => {
 const browser = await chromium.launch({headless:true});
 try {
  for (const width of widths) {
   const context = await browser.newContext({viewport:{width,height:900}});
   const page = await context.newPage();
   page.setDefaultTimeout(10000); page.setDefaultNavigationTimeout(10000);
   const errors=[]; const cacheFailures=[]; page.on('pageerror', error=>errors.push(error.message));
   page.on('response',response=>{ const pathname=new URL(response.url()).pathname; if(!pathname.includes('/assets/')) {if(response.headers()['cache-control']!=='no-store')cacheFailures.push(pathname);} });
   const client = await context.newCDPSession(page);
   await client.send('WebAuthn.enable');
   const {authenticatorId} = await client.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
   const username=`admin${width}`;
   const login = async () => {
    const response = await page.goto(`${baseURL}/login`);
    assert.equal(response.status(), 200, await page.locator("body").innerText());
    assert.equal(await page.locator("#username").count(), 1, await page.locator("body").innerText());
    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await page.getByRole('button',{name:'Sign in',exact:true}).click();
   };
   const account = async () => { await page.goto(`${baseURL}/account`); await page.getByRole('heading',{name:'Account',exact:true}).waitFor(); };
   const verify = async () => { await page.getByRole('button',{name:'Continue with passkey'}).click(); };
   const assertNative = async () => {
    const form=page.locator('form[data-webauthn]');
    const transaction=await form.locator('[name="transaction"]').inputValue();
    assert.equal((await page.evaluate(()=>localStorage.getItem('htmx-history-cache')||'')).includes(transaction),false,'ceremony excluded from history');
    assert.equal(await form.getAttribute('hx-boost'),'false');
    assert.equal(await form.getAttribute('hx-history'),'false');
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true,`overflow at ${width}`);
    await page.getByRole('button',{name:'Continue with passkey'}).focus();
    assert.equal(await page.locator('[data-webauthn-button]').evaluate(el=>el===document.activeElement),true);
   };
   await login(); await page.waitForURL(`${baseURL}/overview`); await account();
   await page.locator('#enable-mfa-password').fill('incorrect password');
   await page.getByRole('button',{name:'Enable 2FA',exact:true}).click();
   await page.getByRole('alert').filter({hasText:'current password'}).waitFor();
   await page.getByRole('link',{name:'Return to Account',exact:true}).click();
   await page.locator('#enable-mfa-password').fill(password);
   await page.getByRole('button' ,{name:'Enable 2FA',exact:true}).click();
   await assertNative();
   // A cancelled registration never activates the factor.
   await page.evaluate(()=>{navigator.credentials.create=()=>Promise.reject(new DOMException('cancelled','NotAllowedError'));});
   await verify(); await page.getByRole('status').filter({hasText:'cancelled or failed'}).waitFor();
   await page.getByRole('link',{name:'Cancel',exact:true}).click(); await account();
   assert.match(await page.locator('#mfa-heading').locator('..').locator('..').innerText(),/Status: Off/);
   await page.locator('#enable-mfa-password').fill(password); await page.getByRole('button',{name:'Enable 2FA',exact:true}).click();
   await verify(); await page.waitForURL(`${baseURL}/account`);
   const originalKeys=(await client.send('WebAuthn.getCredentials',{authenticatorId})).credentials;
   assert.equal(originalKeys.length,1);
   const originalCredentialId=originalKeys[0].credentialId;
   assert.match(await page.locator('#mfa-heading').locator('..').locator('..').innerText(),/Status: Enabled/);
   // Unsupported and no-script browsers get pending verification, never auth cookies.
   for (const javaScriptEnabled of [false,true]) {
    const restricted = await browser.newContext({javaScriptEnabled});
    if(javaScriptEnabled) await restricted.addInitScript(()=>Object.defineProperty(window,'PublicKeyCredential',{value:undefined}));
    const p=await restricted.newPage(); await p.goto(`${baseURL}/login`);
    await p.locator('#username').fill(username); await p.locator('#password').fill(password); await p.getByRole('button',{name:'Sign in',exact:true}).click();
    await p.locator('form[data-webauthn]').waitFor();
    assert.equal((await restricted.cookies()).some(c=>['balda_access','balda_refresh'].includes(c.name)&&c.value),false);
    assert.equal(await p.getByRole('button',{name:'Continue with passkey'}).isDisabled(),true);
    assert.match(await p.locator('body').innerText(),javaScriptEnabled?/Passkeys are unavailable/:/require JavaScript/);
    await restricted.close();
   }
   // A server-rejected ceremony issues no auth; restart is explicit.
   await login();
   await page.locator('input[name="transaction"]').evaluate(el=>el.value='invalid-transaction');
   await verify(); await page.getByRole('alert').filter({hasText:'verification failed or expired'}).waitFor();
   assert.equal((await context.cookies()).some(c=>['balda_access','balda_refresh'].includes(c.name)&&c.value),false);
   await page.getByRole('link',{name:'Start sign-in again',exact:true}).click();
   // Enrolled password sign-in is pending; cancellation/retry and real assertion work.
   await login(); await assertNative();
   assert.equal((await context.cookies()).some(c=>['balda_access','balda_refresh'].includes(c.name)&&c.value),false);
   await page.evaluate(()=>{ const get=navigator.credentials.get.bind(navigator.credentials); let once=true; navigator.credentials.get=options=>{if(once){once=false;return Promise.reject(new DOMException('cancelled','NotAllowedError'));}return get(options);}; });
   await verify(); await page.getByRole('status').filter({hasText:'cancelled or failed'}).waitFor(); await verify(); await page.waitForURL(`${baseURL}/overview`);
   const refreshBefore=(await context.cookies(`${baseURL}/auth/session/refresh`)).find(c=>c.name==='balda_refresh').value;
   await page.goto(`${baseURL}/access/new`);
   const target=`created-after-refresh-${width}`;
   await page.locator('#create-display-name').fill(target); await page.locator('#create-username').fill(target); await page.locator('#create-password').fill(password);
   let mutationCount=0; const mutationListener=request=>{if(request.method()==='POST'&&request.url()===`${baseURL}/access/users`)mutationCount++;}; page.on('request',mutationListener);
   await page.waitForTimeout(3200);
   const [created]=await Promise.all([page.waitForResponse(r=>r.url()===`${baseURL}/access/users`),page.getByRole('button',{name:'Create user',exact:true}).click()]);
   assert.notEqual(created.status(),403,'valid refreshed session must not require another passkey');
   await page.goto(`${baseURL}/account`);
   assert.equal(await page.getByRole('heading',{name:'Account',exact:true}).count(),1);
   for (const path of ['/access','/audit','/mcp']) {
    const response=await page.goto(`${baseURL}${path}`);
    assert.equal(response.status(),path==='/mcp'?503:200,`refreshed administrator cannot open ${path}`);
    assert.equal(new URL(page.url()).pathname,`${new URL(baseURL).pathname.replace(/\/$/,'')}${path}`);
   }
   assert.equal((await context.cookies(`${baseURL}/auth/session/refresh`)).find(c=>c.name==='balda_refresh').value,refreshBefore);
   assert.equal(mutationCount,1,'valid refreshed action must execute once'); page.off('request',mutationListener);
   await page.goto(`${baseURL}/access`); await page.getByRole('heading',{name:'Access',exact:true}).waitFor(); assert.ok((await page.getByText(target,{exact:true}).count())>0,'created user is absent after refresh');
   await account();
   // Account opts out of HTMX history; back/forward must fetch usable pages.
   if(width<992) await page.getByRole('button',{name:'Toggle navigation',exact:true}).click();
   await page.locator('.app-sidebar [data-nav-link]').filter({hasText:'Overview'}).first().click(); await page.waitForURL(`${baseURL}/overview`);
   await page.goBack(); await page.getByRole('heading',{name:'Account',exact:true}).waitFor();
   await page.goForward(); await page.getByRole('heading',{name:'Overview',exact:true}).waitFor();
   assert.equal(await page.evaluate(()=>document.querySelector('form[data-webauthn]')!==null),false);
   await account(); await page.locator('#replace-mfa-password').fill(password); await page.locator('form[action$="/account/2fa/replace/start"] input[name="confirm"]').check(); await page.getByRole('button',{name:'Replace passkey',exact:true}).click();
   await page.waitForFunction(()=>document.querySelector('form[data-registration="true"]'));
   await verify(); await page.waitForURL(`${baseURL}/account`);
   const keys=(await client.send('WebAuthn.getCredentials',{authenticatorId})).credentials;
   assert.equal(keys.length,2);
   await client.send('WebAuthn.removeCredential',{authenticatorId,credentialId:originalCredentialId});
   await page.locator('#disable-mfa-password').fill(password); await page.locator('form[action$="/account/2fa/disable"] input[name="confirm"]').check(); await page.getByRole('button',{name:'Disable 2FA',exact:true}).click(); await page.waitForURL(`${baseURL}/account`);
   assert.match(await page.locator('#mfa-heading').locator('..').locator('..').innerText(),/Status: Off/);
   await login(); await page.waitForURL(`${baseURL}/overview`);
   assert.equal(errors.length,0,errors.join('\n')); assert.deepEqual(cacheFailures,[],'authentication responses must be no-store');
   await context.close();
   console.log(`WebAuthn enable/login/refresh/replace/disable, cancel, no-JS/unsupported, keyboard ${width}: passed`);
  }
 } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exit(1);});
