import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled=process.env.CHECKPOINT_M6C_BROWSER==='1';
const cfg=enabled?JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS||'../.dev/m6c/browser/access.json','utf8')):{};
test.skip(!enabled,'Requires the isolated M6C stand');
for(const width of [320,390,1280]) {
 test(`recording controls, history and deletion at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:900});await page.goto(cfg.publicURL);
  await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.getByLabel('Username',{exact:true}).fill(cfg.adminUsername);await page.getByLabel('Password',{exact:true}).fill(cfg.adminPassword);await page.getByRole('button',{name:'Sign in',exact:true}).click();
  await expect(page.getByRole('heading',{name:'Your rooms',exact:true})).toBeVisible();
  const name=`Recording UI ${width} ${Date.now()}`;await page.getByRole('button',{name:'Create a room',exact:true}).click();await page.getByLabel('Room name',{exact:true}).fill(name);await page.getByRole('button',{name:'Create room',exact:true}).click();
  const card=page.getByRole('article').filter({has:page.getByRole('heading',{name,exact:true})});await card.getByRole('button',{name:'Join room',exact:false}).click();
  const device=page.getByRole('dialog').filter({hasText:'Check devices'});if(await device.count())await device.getByRole('button',{name:/Join/}).click();
  const join=page.getByRole('button',{name:'Join meeting',exact:true});if(await join.count())await join.click();
  await page.getByRole('button',{name:'Microphone',exact:true}).click();await expect(page.getByRole('button',{name:'Microphone',exact:true})).toHaveAttribute('data-lk-enabled','true');
  const startButton=page.getByRole('button',{name:'Start recording',exact:true});
  if(width===320){
   for(const [status,body,title] of [[200,{enabled:false,ready:false,run:null},'Recording is disabled on this server.'],[200,{enabled:true,ready:false,run:null},'Recorder unavailable'],[503,{},'Recording status unavailable']] as const){
    await page.route('**/api/recording',route=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)}));
    await expect(startButton).toBeVisible();await expect(startButton).toBeDisabled();await expect(startButton).toHaveAttribute('title',title);
    await page.unroute('**/api/recording');
   }
  }
  await expect(startButton).toBeEnabled();const idleColor=await startButton.evaluate(el=>getComputedStyle(el).color.match(/\d+/g)!.map(Number));expect(idleColor[1]).toBeGreaterThan(idleColor[0]);await startButton.click();await expect(page.getByRole('dialog',{name:'Recording options'})).toBeVisible();await page.keyboard.press('Escape');await expect(page.getByRole('dialog',{name:'Recording options'})).toHaveCount(0);
  await page.getByRole('button',{name:'Start recording',exact:true}).click();await page.getByRole('button',{name:'No, record only',exact:true}).click();const archive=page.getByRole('button',{name:'Room history',exact:true});await expect(archive).toHaveCount(1);await expect(archive).toHaveAttribute('aria-description','Recording only');const stopButton=page.getByRole('button',{name:'Stop recording',exact:true});const activeColor=await stopButton.evaluate(el=>getComputedStyle(el).backgroundColor.match(/\d+/g)!.map(Number));expect(activeColor[0]).toBeGreaterThan(activeColor[1]);await expect(stopButton.locator('svg circle')).toBeVisible();if(width===390)await page.screenshot({path:'../.dev/active-recording-mobile.png',fullPage:true});await archive.hover();await expect(page.getByRole('tooltip')).toHaveText('Recording only');const tooltipBox=await page.getByRole('tooltip').boundingBox();expect(tooltipBox!.x).toBeGreaterThanOrEqual(0);expect(tooltipBox!.x+tooltipBox!.width).toBeLessThanOrEqual(width);await archive.click();await expect(page.getByRole('dialog',{name:`${name} · History`})).toBeVisible();await page.getByRole('button',{name:'Close dialog',exact:true}).click();await expect(archive).toHaveAttribute('aria-description','Recording only');
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.getByRole('button',{name:'Stop recording',exact:true}).click();await expect(page.getByRole('button',{name:'Start recording',exact:true})).toBeEnabled();await page.getByRole('button',{name:'Room history',exact:true}).click();const history=page.getByRole('dialog',{name:`${name} · History`});await expect(history).toBeVisible();await expect(history.getByText('Saved recording · Recording only',{exact:true})).toBeVisible();await history.getByRole('button').filter({hasText:'Saved recording'}).click();await expect(history.locator('audio')).toBeVisible();await expect.poll(()=>history.locator('audio').evaluate((a:HTMLAudioElement)=>a.readyState)).toBeGreaterThan(0);
  await history.getByRole('button',{name:'Delete recording',exact:true}).click();await expect(history.getByRole('alertdialog')).toBeVisible();await history.getByRole('button',{name:'Keep recording',exact:true}).click();await expect(history.getByRole('alertdialog')).toHaveCount(0);await history.getByRole('button',{name:'Close dialog',exact:true}).click();
  await page.getByRole('button',{name:'Leave',exact:true}).click();await expect(page.getByRole('heading',{name:'Your rooms',exact:true})).toBeVisible();await card.getByRole('button',{name:'Delete room',exact:true}).click();const deletion=page.getByRole('dialog',{name:'Delete room and all its contents?'});await deletion.getByRole('button',{name:'Cancel',exact:true}).click();await expect(card).toBeVisible();await card.getByRole('button',{name:'Delete room',exact:true}).click();await deletion.getByRole('button',{name:'Delete room permanently',exact:true}).click();await expect(card.getByText('Deleting room…',{exact:true})).toBeVisible();
 });
}
