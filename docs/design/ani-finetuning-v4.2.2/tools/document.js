(function(){

const input=document.getElementById('search');
const items=[...document.querySelectorAll('.chapter[data-chapter]')];
const navs=[...document.querySelectorAll('.navlink[data-chapter]')];
function filter(){const q=(input?.value||'').trim().toLocaleLowerCase();let n=0;items.forEach(el=>{let show=!q||el.textContent.toLocaleLowerCase().includes(q);el.hidden=!show;if(show)n++;const link=navs.find(a=>a.dataset.chapter===el.dataset.chapter);if(link)link.hidden=!show});const count=document.getElementById('count');if(count)count.textContent=q?`匹配 ${n} / ${items.length} 章`:`${items.length} 章 · 按关键词筛选`;const empty=document.getElementById('empty');if(empty)empty.style.display=n?'none':'block'}
input?.addEventListener('input',filter);filter();
document.getElementById('clear')?.addEventListener('click',()=>{input.value='';filter();input.focus()});
document.getElementById('menu')?.addEventListener('click',()=>document.querySelector('.sidebar')?.classList.toggle('open'));
document.getElementById('print')?.addEventListener('click',()=>window.print());
let font=16;try{font=Number(localStorage.getItem('ani-ft-v4.2-font'))||16}catch(e){};function setFont(){document.documentElement.style.setProperty('--font',font+'px');try{localStorage.setItem('ani-ft-v4.2-font',String(font))}catch(e){}}setFont();
document.getElementById('fontMinus')?.addEventListener('click',()=>{font=Math.max(14,font-1);setFont()});document.getElementById('fontPlus')?.addEventListener('click',()=>{font=Math.min(20,font+1);setFont()});
document.querySelectorAll('.copy').forEach(btn=>btn.addEventListener('click',async()=>{const text=btn.closest('.codewrap').querySelector('code').textContent;let ok=false;try{await navigator.clipboard.writeText(text);ok=true}catch(e){const ta=document.createElement('textarea');ta.value=text;ta.style.position='fixed';ta.style.opacity='0';document.body.appendChild(ta);ta.select();ok=document.execCommand('copy');ta.remove()}btn.textContent=ok?'已复制':'请选择文本复制';setTimeout(()=>btn.textContent='复制',1800)}));
function showAnchor(){if(!location.hash)return;let id;try{id=decodeURIComponent(location.hash.slice(1))}catch(e){return}const el=document.getElementById(id);if(el){const sec=el.closest('.chapter');if(sec?.hidden&&input){input.value='';filter()}setTimeout(()=>el.scrollIntoView({block:'start'}),20)}}window.addEventListener('hashchange',showAnchor);showAnchor();
if('IntersectionObserver' in window){const obs=new IntersectionObserver(entries=>{entries.forEach(e=>{if(e.isIntersecting){navs.forEach(a=>a.classList.toggle('active',a.dataset.chapter===e.target.id))}})},{rootMargin:'-10% 0px -70% 0px'});items.forEach(el=>obs.observe(el))}

})();