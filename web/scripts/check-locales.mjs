import fs from 'node:fs';
import assert from 'node:assert/strict';
import { parse } from '@babel/parser';
import i18next from 'i18next';
const read = name => JSON.parse(fs.readFileSync(new URL('../src/locales/' + name + '.json', import.meta.url)));
const en=read('en'), ru=read('ru'), source=read('source');
assert.deepEqual(Object.keys(en).sort(),Object.keys(ru).sort(),'Locale keys differ');
const params=value=>[...value.matchAll(/{{\s*([\w]+)\s*}}/g)].map(m=>m[1]).sort();
for(const key of Object.keys(en)){
 assert.equal(typeof ru[key],'string',key);
 assert.ok(ru[key].length,key);
 assert.deepEqual(params(en[key]),params(ru[key]),'Interpolation differs: '+key);
}
for(const [text,key]of Object.entries(source))assert.equal(en[key],text,'Source/key mismatch');
const allowed=new Set(['Checkpoint','◈ Checkpoint','Sync up. Move forward.','English']);
let calls=0;
for(const file of fs.readdirSync(new URL('../src/',import.meta.url)).filter(f=>f.endsWith('.tsx'))){
 const ast=parse(fs.readFileSync(new URL('../src/'+file,import.meta.url),'utf8'),{sourceType:'module',plugins:['typescript','jsx']});
 function walk(n){
  if(!n||typeof n!=='object')return;
  if(n.type==='CallExpression'&&n.callee.name==='tr'&&n.arguments[0]?.type==='StringLiteral'){
   assert.ok(source[n.arguments[0].value],file+': missing translation '+n.arguments[0].value);calls++;
  }
  if(n.type==='JSXText'){
   const text=n.value.replace(/\s+/g,' ').trim();
   assert.ok(!/[A-Za-z]/.test(text)||allowed.has(text),file+': untranslated JSX '+text);
  }
  if(n.type==='JSXAttribute'&&['title','aria-label','placeholder','label','description','micLabel','camLabel','joinLabel','userLabel'].includes(n.name.name)&&n.value?.type==='StringLiteral'){
   assert.ok(!/[A-Za-z]/.test(n.value.value)||n.value.value==='XXXX-XXXX-XXXX-XXXX',file+': untranslated attribute '+n.value.value);
  }
  for(const[k,v]of Object.entries(n))if(!['loc','comments','extra'].includes(k)){if(Array.isArray(v))v.forEach(walk);else if(v&&typeof v==='object')walk(v);}
 }
 walk(ast);
}
await i18next.init({lng:'ru',fallbackLng:'en',resources:{en:{translation:en},ru:{translation:ru}}});
for(const [count,word]of [[1,'проект'],[2,'проекта'],[5,'проектов'],[11,'проектов'],[21,'проект']])assert.equal(i18next.t('projects',{count}),count+' '+word);
console.log('Locale catalogs: '+Object.keys(en).length+' keys, '+calls+' static calls, interpolation and RU plural forms verified');
