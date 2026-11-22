import '@fontsource-variable/golos-text';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';
import '@fontsource/ibm-plex-mono/600.css';
import './tokens.css';
import './landing.css';

// Real entries of the ready-made catalog (internal/pairs/pairs.json).
const pairs=[
 {
  "raw": "В идеале — богатым, на берегу океана, где никто не спрашивает, кем я себя вижу через пять лет.",
  "out": "Я стремлюсь к профессиональному и финансовому росту, расширению зоны ответственности и участию в стратегически значимых проектах.",
  "from": "Как есть",
  "to": "Как отправить"
 },
 {
  "raw": "У вас каждый день всё срочно. Может, вы просто планировать нихуя не умеете?",
  "out": "Предлагаю уточнить приоритетность новых задач относительно текущих обязательств, чтобы эффективнее распределить ресурсы.",
  "from": "Как есть",
  "to": "Как отправить"
 },
 {
  "raw": "Мы третью неделю меняем стратегию каждый вторник. Может, попробуем хотя бы одну довести до среды?",
  "out": "Предлагаю зафиксировать текущий стратегический вектор на согласованный период, чтобы команда могла сосредоточиться на реализации.",
  "from": "Как есть",
  "to": "Как отправить"
 },
 {
  "raw": "Нам нужен стрессоустойчивый сотрудник.",
  "out": "Дедлайн был вчера. ТЗ будет завтра.",
  "from": "Корпоративный текст",
  "to": "На человеческом"
 }
];
const $=s=>document.querySelector(s);
document.querySelectorAll('.demo-tabs button').forEach(b=>b.addEventListener('click',()=>{
 document.querySelectorAll('.demo-tabs button').forEach(x=>x.setAttribute('aria-pressed',x===b));
 const p=pairs[b.dataset.i];$('#raw').textContent=p.raw;$('#out').textContent=p.out;$('#from-tag').textContent=p.from;$('#to-tag').textContent=p.to;
}));
$('#copy').addEventListener('click',async e=>{const t=e.currentTarget;try{await navigator.clipboard.writeText($('#out').textContent)}catch{}const o=t.lastChild.textContent;t.lastChild.textContent='Скопировано';setTimeout(()=>t.lastChild.textContent=o,1500)});
const root=document.documentElement,tb=$('#theme');
if(matchMedia('(prefers-color-scheme: dark)').matches){root.dataset.theme='dark';tb.setAttribute('aria-pressed',true);tb.textContent='Светлая тема'}
tb.addEventListener('click',()=>{const d=root.dataset.theme!=='dark';root.dataset.theme=d?'dark':'light';tb.setAttribute('aria-pressed',d);tb.textContent=d?'Светлая тема':'Тёмная тема'});
