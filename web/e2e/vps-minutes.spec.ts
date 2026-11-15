import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled=process.env.CHECKPOINT_VPS_MINUTES==='1';
const cfg=enabled?JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS!,'utf8')):{};
test.skip(!enabled,'Explicit public VPS pilot smoke');
test('public native recording, local minutes, archive and deletion',async({page})=>{
 // Native CPU profiles vary substantially; this is functional acceptance, not
 // a processing latency benchmark. Keep a bounded budget for slower VPS CPUs.
 test.setTimeout(1920000);
 await page.goto(cfg.publicURL);
 const original=await page.evaluate(async c=>{
  const login=await fetch('/api/account/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:c.adminUsername,password:c.adminPassword})});
  if(!login.ok)throw new Error('Test account login failed');
  const account=await login.json();
  await fetch('/api/account/preferences',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({uiLocale:'en'})});
  return account.uiLocale||'auto';
 },cfg);
 let room='';
 try{
  await page.goto(cfg.publicURL + '/rooms');await expect(page.getByRole('heading',{name:'Your rooms',exact:true})).toBeVisible();
  const name='Deployment smoke '+Date.now();
  room=await page.evaluate(async name=>{const r=await fetch('/api/rooms',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name,mode:'meeting'})});if(!r.ok)throw new Error('Room creation failed');return(await r.json()).id;},name);
  await page.reload();
  await page.getByRole('article').filter({has:page.getByRole('heading',{name,exact:true})}).getByRole('button',{name:/Join room/}).click();
  await expect(page.getByRole('button',{name:'Start recording',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'Start recording',exact:true}).click();
  await expect(page.getByText('Audio and minutes are processed locally on this server.')).toBeVisible();
  await page.getByRole('button',{name:'Yes, record & create minutes',exact:true}).click();
  await expect(page.getByRole('button',{name:'Room history',exact:true})).toHaveAttribute('aria-description','Recording · Minutes will be generated');
  await page.getByRole('button',{name:'Microphone',exact:true}).click();
  await page.waitForTimeout(11000);
  await page.getByRole('button',{name:'Stop recording',exact:true}).click();
  await page.getByRole('button',{name:'Microphone',exact:true}).click();
  await page.getByRole('button',{name:'Room history',exact:true}).click();
  const history=page.getByRole('dialog',{name:name+' · History'});
  await expect(history.getByText('Saved recording · Minutes ready',{exact:true})).toBeVisible({timeout:1800000});
  await history.getByRole('button').filter({hasText:'Saved recording'}).click();
  await expect(history.locator('audio')).toBeVisible();
  await expect.poll(()=>history.locator('audio').evaluate((a:HTMLAudioElement)=>a.readyState)).toBeGreaterThan(0);
  await expect(history.locator('.minutes-document')).toContainText('S000001');
  await expect(history.locator('.minutes-document')).toContainText('Краткий итог');
  await history.getByRole('button',{name:'Edit / review',exact:true}).click();
  const editor=history.locator('.minutes-editor textarea');const text=await editor.inputValue();
  // Exercises the reverse proxy beyond its former 8 KiB limit.
  await editor.fill(text+'\n\n'+('Проверка большого протокола. '.repeat(400)));
  await history.getByRole('button',{name:'Save new revision',exact:true}).click();
  await expect(history.locator('.minutes-editor')).toHaveCount(0);
  await history.getByRole('button',{name:'Close dialog',exact:true}).click();
  await page.getByRole('button',{name:'Leave',exact:true}).click();
 }finally{
  await page.evaluate(async({room,original})=>{
   if(room)await fetch('/api/rooms/'+encodeURIComponent(room)+'/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({confirmed:true})});
   await fetch('/api/account/preferences',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({uiLocale:original})});
  },{room,original});
 }
});
