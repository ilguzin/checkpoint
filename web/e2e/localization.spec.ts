import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled=process.env.CHECKPOINT_M6C_BROWSER==='1';
const cfg=enabled?JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS||'../.dev/m6c/browser/access.json','utf8')):{};
test.skip(!enabled,'Requires isolated stand');
for(const [languages,expected]of [[['ru-RU','en-US'],'ru'],[['de-DE','ru-RU','en-US'],'ru'],[['en-GB','ru-RU'],'en'],[['de-DE'],'en']] as const){
 test('auto language '+languages.join(','),async({page})=>{
  await page.addInitScript(l=>Object.defineProperty(navigator,'languages',{get:()=>l}),languages);
  await page.goto(cfg.publicURL);await expect(page.locator('html')).toHaveAttribute('lang',expected);
  await expect(page.getByRole('heading',{name:expected==='ru'?'Войти в комнату':'Join a room',exact:true})).toBeVisible();
 });
}
test('guest language, denied storage, languagechange and mobile layout',async({page})=>{
 await page.setViewportSize({width:320,height:900});
 await page.addInitScript(()=>{Object.defineProperty(navigator,'languages',{configurable:true,get:()=>['en-US']});Object.defineProperty(window,'localStorage',{get:()=>{throw new DOMException('denied','SecurityError');}});});
 await page.goto(cfg.publicURL);await page.getByRole('button',{name:'User settings',exact:true}).click();await page.getByLabel('Interface language').selectOption('ru');
 await expect(page.locator('html')).toHaveAttribute('lang','ru');
 await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.getByLabel('Язык интерфейса').selectOption('auto');
 await page.evaluate(()=>{Object.defineProperty(navigator,'languages',{get:()=>['ru-RU']});window.dispatchEvent(new Event('languagechange'));});
 await expect(page.locator('html')).toHaveAttribute('lang','ru');
 await page.getByRole('button',{name:'← Назад',exact:true}).click();await page.getByRole('button',{name:'Проверить устройства перед входом'}).click();
 await expect(page.getByRole('dialog')).toBeVisible();await expect(page.getByRole('button',{name:'Использовать эти устройства'})).toBeVisible();
 await page.screenshot({path:'../.dev/m6d/russian-mobile.png',fullPage:true});
});
test('account language persists and settings remain unavailable until leaving the call',async({page,browser})=>{
 await page.addInitScript(()=>{
  localStorage.setItem('checkpoint.uiLocale','ru');
  const PC=window.RTCPeerConnection;
  Object.defineProperty(window,'__pcCount',{value:0,writable:true});
  window.RTCPeerConnection=new Proxy(PC,{construct(target,args){(window as any).__pcCount++;return Reflect.construct(target,args);}});
 });
 await page.goto(cfg.publicURL);
 await page.evaluate(async c=>{await fetch('/api/account/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:c.adminUsername,password:c.adminPassword})});await fetch('/api/account/preferences',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({uiLocale:'en'})});},cfg);
 await page.goto(cfg.publicURL + '/rooms');await expect(page.locator('html')).toHaveAttribute('lang','en');await expect(page.getByRole('heading',{name:'Your rooms'})).toBeVisible();
 await page.getByRole('button',{name:'User settings',exact:true}).click();await page.getByLabel('Interface language').first().selectOption('ru');await expect(page.locator('html')).toHaveAttribute('lang','ru');await page.getByRole('button',{name:'← Назад',exact:true}).click();
 const second=await browser.newContext({locale:'en-US'}),other=await second.newPage();await other.goto(cfg.publicURL);
 await other.evaluate(async c=>{await fetch('/api/account/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:c.adminUsername,password:c.adminPassword})});},cfg);await other.reload();await expect(other.locator('html')).toHaveAttribute('lang','ru');await second.close();
 await page.getByRole('button',{name:'Настройки пользователя',exact:true}).click();await page.getByLabel('Язык интерфейса').first().selectOption('en');await page.getByRole('button',{name:'← Back',exact:true}).click();
 const name='Locale test '+Date.now();await page.getByRole('button',{name:'Create a room',exact:true}).click();await page.getByLabel('Room name',{exact:true}).fill(name);await page.getByRole('button',{name:'Create room',exact:true}).click();
 await page.getByRole('article').filter({has:page.getByRole('heading',{name,exact:true})}).getByRole('button',{name:/Join room/}).click();
 await expect(page.getByRole('button',{name:'Start recording',exact:true})).toBeEnabled();
 await page.getByRole('button',{name:'Start recording',exact:true}).click();await page.getByRole('button',{name:'No, record only',exact:true}).click();await expect(page.getByRole('button',{name:'Room history',exact:true})).toHaveAttribute('aria-description','Recording only');
 const pcs=await page.evaluate(()=>(window as any).__pcCount);
 const secret=await page.evaluate(()=>sessionStorage.getItem('checkpoint.session'));
 const run=await page.evaluate(async secret=>(await(await fetch('/api/recording',{headers:{Authorization:'Bearer '+secret}})).json()).run.id,secret);
 await page.getByRole('button',{name:'Chat',exact:true}).click();await page.getByRole('textbox',{name:'Message',exact:true}).fill('Keep this original draft · сохранить');
 await expect(page.getByRole('button',{name:'User settings',exact:true})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Sign out',exact:true})).toHaveCount(0);
 await expect(page.locator('html')).toHaveAttribute('lang','en');
 await expect(page.getByRole('button',{name:'Room history',exact:true})).toHaveAttribute('aria-description','Recording only');
 expect(await page.evaluate(()=>(window as any).__pcCount)).toBe(pcs);
 expect(await page.evaluate(()=>sessionStorage.getItem('checkpoint.session'))).toBe(secret);
 expect(await page.evaluate(async secret=>(await(await fetch('/api/recording',{headers:{Authorization:'Bearer '+secret}})).json()).run.id,secret)).toBe(run);
 await expect(page.getByRole('textbox',{name:'Message',exact:true})).toHaveValue('Keep this original draft · сохранить');

 await page.getByRole('button',{name:'Stop recording',exact:true}).click();
 await page.getByRole('button',{name:'Leave',exact:true}).click();await expect(page.getByRole('heading',{name:'Your rooms'})).toBeVisible();
 await page.getByRole('button',{name:'User settings',exact:true}).click();await page.getByLabel('Interface language').selectOption('ru');await expect(page.locator('html')).toHaveAttribute('lang','ru');await page.getByLabel('Язык интерфейса').selectOption('auto');await page.getByRole('button',{name:'← Back',exact:true}).click();
 await page.getByRole('button',{name:'Sign out',exact:true}).click();await expect(page.locator('html')).toHaveAttribute('lang','ru');
});
test('Observer independently switches language',async({page})=>{
 const observer=cfg.observerURL || cfg.publicURL.replace(':19890',':19892');
 await page.goto(observer);await page.getByLabel('Interface language').selectOption('ru');
 await expect(page.getByRole('button',{name:'Обзор',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'История',exact:true}).click();await expect(page.getByRole('heading',{name:'Что происходило.'})).toBeVisible();
 await page.getByLabel('Язык интерфейса').selectOption('en');await expect(page.getByRole('heading',{name:'See what happened.'})).toBeVisible();
});
