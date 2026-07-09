package web

// dashboardHTML is the single-page BTST dashboard. Design follows the dataviz
// method: system sans everywhere, one hero figure, thin marks (2px line, 10%
// area wash, hairline solid grid), crosshair + tooltip on the equity curve,
// text in ink tokens with P&L on delta tokens, and a selected (not auto-
// flipped) dark theme. Polls /api/summary every 20s. Dependency-free.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTST Engine</title>
<style>
  :root{
    --bg:#f9f9f7; --card:#fcfcfb; --bd:rgba(11,11,11,.10); --grid:#e1e0d9;
    --ink:#0b0b0b; --ink2:#52514e; --mut:#898781;
    --acc:#2a78d6; --accink:#ffffff;
    --gd:#0ca30c; --bad:#d03b3b;           /* mark fills (chart, dots)   */
    --gdtx:#006300; --badtx:#d03b3b;       /* delta text tokens          */
    --wash:rgba(11,11,11,.045);            /* one step off the surface   */
    --shadow:0 1px 2px rgba(11,11,11,.04),0 4px 16px rgba(11,11,11,.05);
  }
  :root[data-theme=dark]{
    --bg:#0d0d0d; --card:#1a1a19; --bd:rgba(255,255,255,.10); --grid:#2c2c2a;
    --ink:#ffffff; --ink2:#c3c2b7; --mut:#898781;
    --acc:#3987e5; --accink:#ffffff;
    --gd:#0ca30c; --bad:#d03b3b;
    --gdtx:#0ca30c; --badtx:#e66767;
    --wash:rgba(255,255,255,.06);
    --shadow:0 1px 2px rgba(0,0,0,.25),0 6px 20px rgba(0,0,0,.25);
  }
  *{box-sizing:border-box;margin:0;padding:0}
  html{color-scheme:light} :root[data-theme=dark]{color-scheme:dark}
  body{background:var(--bg);color:var(--ink2);
    font:14px/1.5 system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;
    padding-bottom:64px;-webkit-font-smoothing:antialiased}
  .wrap{max-width:1060px;margin:0 auto;padding:0 20px}
  button{font:inherit}

  /* ── nav ─────────────────────────────────────────────── */
  nav{position:sticky;top:0;z-index:20;border-bottom:1px solid var(--bd);
    background:color-mix(in srgb,var(--bg) 82%,transparent);
    backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px)}
  .bar{display:flex;align-items:center;gap:10px;padding:12px 0;flex-wrap:wrap}
  .logo{display:flex;align-items:center;gap:9px;font-size:15px;font-weight:650;color:var(--ink);letter-spacing:-.01em}
  .logomark{width:26px;height:26px;border-radius:8px;background:var(--acc);color:var(--accink);
    display:inline-flex;align-items:center;justify-content:center;font-size:12px;font-weight:700}
  .badge{font-size:10px;font-weight:700;padding:3px 9px;border-radius:999px;letter-spacing:.08em}
  .paper{background:color-mix(in srgb,var(--acc) 12%,transparent);color:var(--acc)}
  .live{background:color-mix(in srgb,var(--bad) 12%,transparent);color:var(--badtx)}
  .spacer{margin-left:auto}
  .muted{color:var(--mut);font-size:12px;font-variant-numeric:tabular-nums}
  .ghost{background:none;border:1px solid var(--bd);border-radius:9px;color:var(--ink2);
    width:32px;height:32px;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;
    font-size:14px;transition:background .12s}
  .ghost:hover{background:var(--wash)}
  #runbtn{background:var(--acc);color:var(--accink);border:0;border-radius:9px;
    padding:8px 16px;font-size:13px;font-weight:600;cursor:pointer;transition:filter .12s}
  #runbtn:hover{filter:brightness(1.08)}
  #runbtn:disabled{opacity:.5;cursor:wait}

  /* ── hero ────────────────────────────────────────────── */
  .hero{background:var(--card);border:1px solid var(--bd);border-radius:16px;
    box-shadow:var(--shadow);margin:22px 0 14px;padding:22px 22px 14px}
  .lbl{color:var(--mut);font-size:12px;font-weight:500}
  .heroline{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin-top:2px}
  .heroval{font-size:44px;font-weight:650;color:var(--ink);letter-spacing:-.02em;line-height:1.15}
  .heroval.pos{color:var(--gdtx)} .heroval.neg{color:var(--badtx)}
  .heroval .cur{font-size:24px;font-weight:500;opacity:.5;margin-right:1px}
  .delta{display:inline-flex;align-items:center;gap:4px;font-size:12.5px;font-weight:650;
    padding:4px 10px;border-radius:999px}
  .kv{display:flex;gap:0;flex-wrap:wrap;margin:12px 0 4px}
  .kv span{font-size:12px;color:var(--mut);padding:2px 14px;border-left:1px solid var(--bd)}
  .kv span:first-child{padding-left:0;border-left:0}
  .kv b{display:block;font-size:13.5px;font-weight:600;color:var(--ink);margin-top:1px;
    font-variant-numeric:tabular-nums}
  .kv b.pos{color:var(--gdtx)} .kv b.neg{color:var(--badtx)}

  /* ── chart ───────────────────────────────────────────── */
  #chartbox{position:relative;margin-top:10px}
  #chart{width:100%;height:210px;display:block}
  .chartempty{color:var(--mut);font-size:12.5px;text-align:center;padding:44px 0}
  #tip{position:absolute;pointer-events:none;background:var(--ink);color:var(--bg);
    font-size:11.5px;padding:6px 10px;border-radius:8px;white-space:nowrap;line-height:1.45;
    transform:translate(-50%,-118%);display:none;z-index:5;font-variant-numeric:tabular-nums}
  #tip b{font-weight:650}

  /* ── stat tiles ──────────────────────────────────────── */
  .stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px;margin:0 0 18px}
  .stat{background:var(--card);border:1px solid var(--bd);border-radius:14px;
    box-shadow:var(--shadow);padding:14px 16px}
  .stat .val{font-size:21px;font-weight:650;color:var(--ink);margin-top:2px;letter-spacing:-.01em}
  .stat .sub{color:var(--mut);font-size:11.5px;margin-top:1px}

  /* ── segmented tabs ──────────────────────────────────── */
  .tabs{display:inline-flex;gap:2px;background:var(--wash);border:1px solid var(--bd);
    border-radius:12px;padding:3px;margin:0 0 14px;max-width:100%;overflow-x:auto}
  .tab{border:0;background:transparent;color:var(--mut);font-size:12.5px;font-weight:600;
    cursor:pointer;padding:7px 14px;border-radius:9px;white-space:nowrap;transition:color .12s}
  .tab:hover{color:var(--ink)}
  .tab.on{background:var(--card);color:var(--ink);box-shadow:0 1px 3px rgba(0,0,0,.12)}
  .tab .n{opacity:.5;margin-left:5px;font-weight:500;font-variant-numeric:tabular-nums}
  section{display:none} section.on{display:block}

  /* ── panels & tables ─────────────────────────────────── */
  .panel{background:var(--card);border:1px solid var(--bd);border-radius:14px;
    box-shadow:var(--shadow);overflow:hidden}
  .tblwrap{overflow-x:auto}
  table{width:100%;border-collapse:collapse;min-width:720px}
  th,td{text-align:right;padding:11px 14px;font-variant-numeric:tabular-nums;
    white-space:nowrap;font-size:13px;border-bottom:1px solid var(--grid)}
  th:first-child,td:first-child{text-align:left;padding-left:18px}
  th:last-child,td:last-child{padding-right:16px}
  th{color:var(--mut);font-size:10.5px;text-transform:uppercase;letter-spacing:.07em;
    font-weight:600;border-bottom:1px solid var(--bd)}
  tbody tr:last-child td{border-bottom:none}
  tbody tr:hover td{background:var(--wash)}
  .inst{display:flex;align-items:center;gap:10px}
  .av{width:30px;height:30px;border-radius:9px;display:inline-flex;align-items:center;
    justify-content:center;font-size:11px;font-weight:700;flex:none}
  .sym{font-weight:600;color:var(--ink);font-size:13px}
  .symsub{color:var(--mut);font-size:10.5px;margin-top:1px}
  .pos{color:var(--gdtx)} .neg{color:var(--badtx)}
  .sl{color:var(--badtx)} .dimtxt{color:var(--mut);font-size:12.5px}
  .pill{display:inline-block;font-size:11px;font-weight:650;padding:2.5px 9px;border-radius:999px}
  .pg{background:color-mix(in srgb,var(--gd) 13%,transparent);color:var(--gdtx)}
  .pr{background:color-mix(in srgb,var(--bad) 12%,transparent);color:var(--badtx)}
  .pn{background:var(--wash);color:var(--mut)}
  .chip{display:inline-block;font-size:10.5px;font-weight:650;padding:2px 9px;border-radius:999px}
  .c-traded{background:color-mix(in srgb,var(--gd) 13%,transparent);color:var(--gdtx)}
  .c-carried{background:color-mix(in srgb,var(--acc) 13%,transparent);color:var(--acc)}
  .c-dropped{background:var(--wash);color:var(--mut)}
  .c-held{background:color-mix(in srgb,#eda100 16%,transparent);color:#a06c00}
  :root[data-theme=dark] .c-held{color:#e5b558}
  .carry{color:var(--acc);font-size:10px;font-weight:700;margin-left:6px;
    background:color-mix(in srgb,var(--acc) 13%,transparent);border-radius:999px;padding:1px 6px}
  .empty{color:var(--mut);padding:46px;text-align:center;font-size:13px}
  .hsub{color:var(--mut);font-size:11px;margin:18px 2px 8px;text-transform:uppercase;
    letter-spacing:.07em;font-weight:600}
  #histdate{background:var(--card);color:var(--ink);border:1px solid var(--bd);border-radius:9px;
    padding:8px 12px;font-size:13px;margin-bottom:12px}
  .del{background:none;border:0;color:var(--mut);cursor:pointer;font-size:13px;
    padding:2px 7px;border-radius:7px;line-height:1;transition:all .12s}
  .del:hover{color:var(--badtx);background:color-mix(in srgb,var(--bad) 12%,transparent)}
  @media (hover:hover){
    tbody .del{opacity:0}
    tbody tr:hover .del,.del:focus-visible{opacity:1}
  }
  #deldate{background:none;border:1px solid var(--bd);color:var(--badtx);border-radius:9px;
    padding:8px 14px;font-size:12.5px;font-weight:600;cursor:pointer;margin-left:8px;transition:background .12s}
  #deldate:hover{background:color-mix(in srgb,var(--bad) 10%,transparent)}
</style>
</head>
<body>
<nav><div class="wrap bar">
  <span class="logo"><span class="logomark">B</span>BTST Engine</span>
  <span id="mode" class="badge paper">…</span>
  <span class="spacer"></span>
  <span class="muted" id="updated"></span>
  <button class="ghost" id="themebtn" onclick="flipTheme()" title="Toggle light/dark">◐</button>
  <button id="runbtn" onclick="runNow()" title="Manual scan + trade (needs trigger token)">▶ Run scan</button>
</div></nav>

<div class="wrap">
  <div class="hero">
    <div class="lbl">Total P&L, net of charges</div>
    <div class="heroline">
      <span class="heroval" id="heropnl">—</span>
      <span id="heropill"></span>
    </div>
    <div class="kv" id="herochips"></div>
    <div id="chartbox"><div class="chartempty">Equity curve appears once trades start closing</div></div>
  </div>

  <div class="stats" id="stats"></div>

  <div class="tabs">
    <button class="tab on" data-t="positions" onclick="showTab('positions')">Holdings<span class="n" id="n-pos">0</span></button>
    <button class="tab" data-t="scan" onclick="showTab('scan')">Today's scan<span class="n" id="n-scan">0</span></button>
    <button class="tab" data-t="closed" onclick="showTab('closed')">Closed<span class="n" id="n-closed">0</span></button>
    <button class="tab" data-t="sl" onclick="showTab('sl')">SL trail<span class="n" id="n-sl">0</span></button>
    <button class="tab" data-t="hist" onclick="showTab('hist')">History</button>
  </div>

  <section id="s-positions" class="on"><div class="panel" id="open"></div></section>
  <section id="s-scan"><div class="hsub" id="scanmeta"></div><div class="panel" id="scan"></div></section>
  <section id="s-closed"><div class="panel" id="closed"></div></section>
  <section id="s-sl"><div class="panel" id="slev"></div></section>
  <section id="s-hist">
    <select id="histdate"><option value="">Loading dates…</option></select>
    <button id="deldate" onclick="delDate()" title="Delete every scan row and trade for the selected date">🗑 Delete day</button>
    <div id="hist"></div>
  </section>
</div>

<script>
/* ── theme (selected, persisted; defaults to the OS preference) ── */
(function(){
  var t = localStorage.getItem('btst_theme');
  if(!t) t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
})();
function flipTheme(){
  var el = document.documentElement;
  el.dataset.theme = el.dataset.theme === 'dark' ? 'light' : 'dark';
  localStorage.setItem('btst_theme', el.dataset.theme);
  load(); // re-render (avatars pick theme-stepped hues)
}

const inr = n => '₹' + Math.round(Math.abs(n)).toLocaleString('en-IN');
const sinr = n => (n<0?'−':'+') + inr(n);
const cls = n => n > 0 ? 'pos' : n < 0 ? 'neg' : '';
const sign = n => (n > 0 ? '+' : '') + n.toFixed(2);
const f2 = n => (n == null ? 0 : n).toFixed(2);
function fmtC(n){ // compact ₹ for axis ticks
  const a=Math.abs(n), s=n<0?'−':'';
  if(a>=1e7) return s+'₹'+(a/1e7).toFixed(1).replace(/\.0$/,'')+'Cr';
  if(a>=1e5) return s+'₹'+(a/1e5).toFixed(1).replace(/\.0$/,'')+'L';
  if(a>=1e3) return s+'₹'+(a/1e3).toFixed(1).replace(/\.0$/,'')+'K';
  return s+'₹'+Math.round(a);
}

/* Ticker avatars: the validated 8-slot categorical palette, stepped per theme.
   Decorative identicons — the symbol text always sits beside them. */
const AVL = ['#2a78d6','#1baf7a','#eda100','#008300','#4a3aa7','#e34948','#e87ba4','#eb6834'];
const AVD = ['#3987e5','#199e70','#c98500','#008300','#9085e9','#e66767','#d55181','#d95926'];
function av(sym){
  let h=0; for(let i=0;i<sym.length;i++) h=(h*31+sym.charCodeAt(i))>>>0;
  const c=(document.documentElement.dataset.theme==='dark'?AVD:AVL)[h%8];
  return '<span class="av" style="background:color-mix(in srgb,'+c+' 15%,transparent);color:'+c+'">'+sym.slice(0,2)+'</span>';
}
function pill(v){ return '<span class="pill '+(v>0?'pg':v<0?'pr':'pn')+'">'+sign(v)+'%</span>'; }
function inst(sym, sub, extra){
  return '<div class="inst">'+av(sym)+'<div><div class="sym">'+sym+(extra||'')+'</div>'
       + (sub?'<div class="symsub">'+sub+'</div>':'')+'</div></div>';
}

function showTab(t){
  document.querySelectorAll('.tab').forEach(b=>b.classList.toggle('on', b.dataset.t===t));
  document.querySelectorAll('section').forEach(s=>s.classList.toggle('on', s.id==='s-'+t));
}

function stat(lbl, val, sub){
  return '<div class="stat"><div class="lbl">'+lbl+'</div><div class="val">'+val+'</div>'
       + (sub?'<div class="sub">'+sub+'</div>':'')+'</div>';
}
function wrapT(inner){ return '<div class="tblwrap"><table>'+inner+'</table></div>'; }

/* ── equity curve: 2px line, 10% wash, hairline grid, crosshair tooltip ── */
let _pts = null;
function niceTicks(mn, mx, want){
  const span = mx-mn || 1, raw = span/want,
        mag = Math.pow(10, Math.floor(Math.log10(raw))), n = raw/mag,
        step = mag * (n>5 ? 10 : n>2 ? 5 : n>1 ? 2 : 1);
  const out = [];
  for(let v = Math.floor(mn/step)*step; v <= mx+step*0.501; v += step) out.push(v);
  return out;
}
function chart(daily){
  if(!daily || daily.length < 2){ _pts = null;
    return '<div class="chartempty">Equity curve appears once a few days of trades close</div>'; }
  let cum = 0;
  const vals = daily.map(d => (cum += d.net));
  // viewBox width tracks the real container so text renders undistorted.
  const W = Math.max(320, document.getElementById('chartbox').clientWidth || 860);
  const n = vals.length, H = 210, L = 54, R = 14, T = 12, B = 24;
  const ticks = niceTicks(Math.min(0,...vals), Math.max(0,...vals), 4);
  const mn = ticks[0], mx = ticks[ticks.length-1];
  const X = i => L + i*(W-L-R)/(n-1);
  const Y = v => T + (mx-v)*(H-T-B)/(mx-mn);
  const xs = vals.map((_,i)=>X(i)), ys = vals.map(Y);
  _pts = {xs, ys, daily, vals, W, H, T, B};

  const up = vals[n-1] >= 0, col = up ? 'var(--gd)' : 'var(--bad)';
  let line = '';
  for(let i=0;i<n;i++) line += (i?' L':'M')+xs[i].toFixed(1)+','+ys[i].toFixed(1);
  const y0 = Math.min(H-B, Math.max(T, Y(0)));
  const area = line+' L'+xs[n-1].toFixed(1)+','+y0+' L'+xs[0].toFixed(1)+','+y0+' Z';

  let grid = '';
  for(const t of ticks){
    const y = Y(t).toFixed(1);
    grid += '<line x1="'+L+'" y1="'+y+'" x2="'+(W-R)+'" y2="'+y+'" stroke="var(--grid)" stroke-width="1"/>'
          + '<text x="'+(L-8)+'" y="'+y+'" text-anchor="end" dominant-baseline="middle" '
          + 'fill="var(--mut)" font-size="10.5" style="font-variant-numeric:tabular-nums">'+fmtC(t)+'</text>';
  }
  const dts = [0, Math.floor((n-1)/2), n-1];
  let xlab = '';
  for(const i of dts){
    const anc = i===0 ? 'start' : i===n-1 ? 'end' : 'middle';
    xlab += '<text x="'+xs[i].toFixed(1)+'" y="'+(H-7)+'" text-anchor="'+anc
          + '" fill="var(--mut)" font-size="10.5">'+daily[i].date.slice(5)+'</text>';
  }
  return '<svg id="chart" viewBox="0 0 '+W+' '+H+'">'
    + '<defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">'
    + '<stop offset="0%" style="stop-color:'+col+';stop-opacity:.12"/>'
    + '<stop offset="100%" style="stop-color:'+col+';stop-opacity:0"/></linearGradient></defs>'
    + grid + xlab
    + '<path d="'+area+'" fill="url(#g)"/>'
    + '<path d="'+line+'" fill="none" stroke="'+col+'" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>'
    + '<line id="ch" y1="'+T+'" y2="'+(H-B)+'" stroke="var(--mut)" stroke-width="1" style="display:none"/>'
    + '<circle cx="'+xs[n-1].toFixed(1)+'" cy="'+ys[n-1].toFixed(1)+'" r="4" fill="'+col+'" stroke="var(--card)" stroke-width="2"/>'
    + '<circle id="hovdot" r="4.5" fill="'+col+'" stroke="var(--card)" stroke-width="2" style="display:none"/>'
    + '</svg><div id="tip"></div>';
}

// Crosshair finds the X: snap to the nearest day, show date + cumulative + day net.
function wireChartHover(){
  const svg = document.getElementById('chart');
  if(!svg || !_pts) return;
  const tip = document.getElementById('tip'), dot = document.getElementById('hovdot'),
        ch = document.getElementById('ch');
  function at(clientX){
    const r = svg.getBoundingClientRect();
    const fx = (clientX - r.left) / r.width * _pts.W;
    let best = 0, bd = 1e9;
    for(let i=0;i<_pts.xs.length;i++){
      const d = Math.abs(_pts.xs[i]-fx);
      if(d < bd){ bd = d; best = i; }
    }
    ch.style.display=''; ch.setAttribute('x1',_pts.xs[best]); ch.setAttribute('x2',_pts.xs[best]);
    dot.style.display=''; dot.setAttribute('cx',_pts.xs[best]); dot.setAttribute('cy',_pts.ys[best]);
    tip.style.display='block';
    tip.style.left = (_pts.xs[best]/_pts.W*r.width)+'px';
    tip.style.top = (_pts.ys[best]/_pts.H*r.height)+'px';
    const d = _pts.daily[best];
    tip.innerHTML = d.date.slice(5)+' · <b>'+sinr(_pts.vals[best])+'</b> total'
                  + '<br>'+sinr(d.net)+' this day';
  }
  svg.addEventListener('pointermove', e => at(e.clientX));
  svg.addEventListener('pointerleave', function(){
    tip.style.display='none'; dot.style.display='none'; ch.style.display='none';
  });
}

// Count-up animation for the hero number (runs once per page load).
let _heroAnimated = false;
function animateHero(el, target){
  if(_heroAnimated){ el.innerHTML = fmtHero(target); return; }
  _heroAnimated = true;
  const t0 = performance.now(), dur = 800;
  function step(t){
    const k = Math.min(1, (t-t0)/dur), e = 1 - Math.pow(1-k, 3);
    el.innerHTML = fmtHero(target*e);
    if(k < 1) requestAnimationFrame(step);
  }
  requestAnimationFrame(step);
}
function fmtHero(n){
  const neg = n < 0;
  return (neg?'−':'') + '<span class="cur">₹</span>'
       + Math.round(Math.abs(n)).toLocaleString('en-IN');
}
function delta(v){
  const up = v >= 0;
  return '<span class="delta '+(up?'pg':'pr')+'">'+(up?'▲':'▼')+' '+Math.abs(v).toFixed(2)+'%</span>';
}
function kv(lbl, val, klass){
  return '<span>'+lbl+'<b class="'+(klass||'')+'">'+val+'</b></span>';
}

function openTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No holdings yet — the 15:20 IST cycle (or ▶ Run scan) fills this.</div>';
  let h='<tr><th>Instrument</th><th>Bought at</th><th>Qty</th><th>Entry</th><th>LTP</th><th>Peak</th><th>Trail SL</th><th>P&L</th><th>%</th><th></th></tr>';
  for(const p of rows){
    const carry = p.carry_count>0 ? '<span class="carry">↻'+p.carry_count+'</span>' : '';
    h+='<tr><td>'+inst(p.symbol, '', carry)+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td><b>'+f2(p.last_price)+'</b></td><td class="dimtxt">'+f2(p.peak_price)
      +'</td><td class="sl">'+f2(p.sl_price)
      +'</td><td class="'+cls(p.unreal_pnl||0)+'"><b>'+sign(p.unreal_pnl||0)+'</b></td><td>'+pill(p.unreal_pct||0)
      +'</td><td>'+delBtn(p)+'</td></tr>';
  }
  return wrapT(h);
}

function scanBadge(o){ return '<span class="chip c-'+o+'">'+o+'</span>'; }
function scanTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No scan yet today.</div>';
  let h='<tr><th>Instrument</th><th>Chg%</th><th>Close</th><th>Status</th><th>Reason</th></tr>';
  for(const r of rows)
    h+='<tr><td>'+inst(r.symbol, r.source||'')+'</td><td>'+pill(r.per_chg||0)+'</td><td>'+f2(r.close)
      +'</td><td>'+scanBadge(r.outcome)+'</td><td class="dimtxt">'+(r.reason||'')+'</td></tr>';
  return wrapT(h);
}

// A trailing stop that fired above breakeven locked in profit — label it as
// such (green) instead of the loss-implying red "SL hit".
function exitLabel(p){
  if(p.exit_reason!=='stoploss') return '<span class="dimtxt">square-off</span>';
  return (p.net_pnl||0) > 0 ? '<span class="pos">Trail profit</span>' : '<span class="sl">SL hit</span>';
}

function closedTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No closed trades yet.</div>';
  let h='<tr><th>Instrument</th><th>Bought at</th><th>Sold at</th><th>Entry</th><th>Exit</th><th>Reason</th><th>Gross</th><th>Charges</th><th>Net P&L</th><th>Net %</th><th></th></tr>';
  for(const p of rows){
    const rsn = exitLabel(p);
    h+='<tr><td>'+inst(p.symbol)+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td class="dimtxt">'+(p.exit_at||'')
      +'</td><td>'+f2(p.entry_price)+'</td><td>'+f2(p.exit_price)+'</td><td>'+rsn
      +'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl)
      +'</td><td class="dimtxt">₹'+f2(p.charges||0)
      +'</td><td class="'+cls(p.net_pnl||0)+'"><b>'+sign(p.net_pnl||0)+'</b></td><td>'+pill(p.net_pct||0)
      +'</td><td>'+delBtn(p)+'</td></tr>';
  }
  return wrapT(h);
}

function slTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No trailing-SL adjustments yet — they appear as prices rise.</div>';
  let h='<tr><th>Instrument</th><th>When</th><th>Price</th><th>Old SL</th><th>New SL</th></tr>';
  for(const e of rows)
    h+='<tr><td>'+inst(e.symbol)+'</td><td class="dimtxt">'+e.at.replace('T',' ').slice(0,19)
      +'</td><td>'+f2(e.price)+'</td><td class="dimtxt">'+f2(e.old_sl)+'</td><td class="pos"><b>'+f2(e.new_sl)+'</b></td></tr>';
  return wrapT(h);
}

async function load(){
  try{
    const s = await (await fetch('/api/summary')).json();
    const m = document.getElementById('mode');
    m.textContent = s.mode; m.className = 'badge ' + (s.mode==='LIVE'?'live':'paper');

    const netTot = (s.net_realized||0)+(s.unrealized_pnl||0);
    const hero = document.getElementById('heropnl');
    hero.className = 'heroval ' + cls(netTot);
    animateHero(hero, netTot);
    const invested = s.open_invested||0;
    document.getElementById('heropill').innerHTML = invested>0 ? delta(netTot/invested*100) : '';
    document.getElementById('herochips').innerHTML =
      kv('Realised', sinr(s.net_realized||0), cls(s.net_realized||0))
     +kv('Unrealised', sinr(s.unrealized_pnl||0), cls(s.unrealized_pnl||0))
     +kv('Charges', inr(s.total_charges||0))
     +kv('Win rate', (s.win_rate||0).toFixed(0)+'%');
    _daily = s.daily;
    document.getElementById('chartbox').innerHTML = chart(s.daily);
    wireChartHover();

    document.getElementById('stats').innerHTML =
      stat('Holdings', s.open_count, s.carried_count ? s.carried_count+' carried over' : 'positions') +
      stat('Deployed', inr(s.open_invested), 'of '+inr(s.capital_per_day)+' / day') +
      stat('Closed trades', s.closed_count, s.wins+' winners') +
      stat('Today’s scan', s.scanned_count||0, (s.traded_count||0)+' traded'+(s.scan_time?' at '+s.scan_time:''));

    document.getElementById('open').innerHTML = openTable(s.open);
    document.getElementById('scan').innerHTML = scanTable(s.scan);
    document.getElementById('closed').innerHTML = closedTable(s.closed);
    document.getElementById('slev').innerHTML = slTable(s.sl_events);
    document.getElementById('scanmeta').textContent = s.scan_date
      ? s.scan_date + (s.scan_time?' at '+s.scan_time+' IST':'') + ' · ' + s.traded_count + '/' + s.scanned_count + ' traded'
      : '';
    document.getElementById('n-pos').textContent = s.open_count||0;
    document.getElementById('n-scan').textContent = s.scanned_count||0;
    document.getElementById('n-closed').textContent = s.closed_count||0;
    document.getElementById('n-sl').textContent = (s.sl_events||[]).length;
    document.getElementById('updated').textContent =
      new Date().toLocaleTimeString('en-IN',{hour12:false}) + ' · ' + s.today;
  }catch(e){
    document.getElementById('updated').textContent = 'Error: ' + e;
  }
}

function histPositions(rows){
  if(!rows||!rows.length) return '<div class="empty">No positions this day.</div>';
  let h='<tr><th>Instrument</th><th>Qty</th><th>Entry</th><th>Exit</th><th>Net P&L</th><th>Status</th><th></th></tr>';
  for(const p of rows){
    const closed = !!p.exit_price;
    const slm = p.exit_reason!=='stoploss' ? ''
      : (p.net_pnl||0) > 0 ? ' <span class="pos">TP</span>' : ' <span class="sl">SL</span>';
    h+='<tr><td>'+inst(p.symbol, p.entry_at||'')+'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+(closed? f2(p.exit_price)+slm : '—')
      +'</td><td class="'+(closed?cls(p.net_pnl||p.pnl):'')+'">'+(closed? sign(p.net_pnl!=null?p.net_pnl:p.pnl):'—')
      +'</td><td>'+(closed?'<span class="pill pn">closed</span>':'<span class="pill c-held">open</span>')
      +'</td><td>'+delBtn(p)+'</td></tr>';
  }
  return wrapT(h);
}

async function loadHistory(date){
  const box = document.getElementById('hist');
  if(!date){ box.innerHTML=''; return; }
  try{
    const h = await (await fetch('/api/history?date='+encodeURIComponent(date))).json();
    box.innerHTML =
      '<div class="hsub">Scan'+(h.scan_time?' — '+h.scan_time+' IST':'')+'</div><div class="panel">' + scanTable(h.scan) + '</div>'
     +'<div class="hsub">Trades · net P&L <span class="'+cls(h.net_pnl)+'">'+sign(h.net_pnl)+'</span></div><div class="panel">' + histPositions(h.positions) + '</div>';
  }catch(e){ box.innerHTML = '<div class="empty">Error: '+e+'</div>'; }
}

async function loadDates(){
  try{
    const dates = await (await fetch('/api/dates')).json();
    const sel = document.getElementById('histdate');
    if(!dates || !dates.length){ sel.innerHTML='<option value="">No history yet</option>'; return; }
    const cur = sel.value;
    sel.innerHTML = dates.map(d=>'<option value="'+d+'">'+d+'</option>').join('');
    if(cur && dates.includes(cur)) sel.value = cur;
    sel.onchange = () => loadHistory(sel.value);
    if(!cur) loadHistory(dates[0]);
  }catch(e){ document.getElementById('histdate').innerHTML='<option>Error</option>'; }
}

function delBtn(p){
  return '<button class="del" title="Delete this record" onclick="delRec('+p.id+',\''+p.symbol+'\')">✕</button>';
}

// POSTs /api/delete with the stored trigger token; on 403 asks for the token
// once and retries. Refreshes every view afterwards.
async function apiDelete(qs, label){
  if(!confirm('Delete '+label+'? This cannot be undone.')) return;
  let t = localStorage.getItem('btst_token') || '';
  const call = tok => fetch('/api/delete?'+qs+'&token='+encodeURIComponent(tok), {method:'POST'});
  try{
    let r = await call(t);
    if(r.status === 403){
      t = prompt('Trigger token (BTST_TRIGGER_TOKEN):', t);
      if(!t) return;
      localStorage.setItem('btst_token', t);
      r = await call(t);
    }
    if(!r.ok){ alert('Failed: ' + r.status + ' ' + await r.text()); return; }
  }catch(e){ alert('Error: ' + e); return; }
  load(); loadDates();
  const sel = document.getElementById('histdate');
  if(sel.value) loadHistory(sel.value);
}
function delRec(id, sym){ apiDelete('id='+id, sym+' (record #'+id+')'); }
function delDate(){
  const d = document.getElementById('histdate').value;
  if(!d){ alert('Pick a date first.'); return; }
  apiDelete('date='+encodeURIComponent(d), 'ALL trades and scan rows for '+d);
}

async function runNow(){
  let t = localStorage.getItem('btst_token') || '';
  t = prompt('Trigger token (BTST_TRIGGER_TOKEN):', t);
  if(!t) return;
  localStorage.setItem('btst_token', t);
  const btn = document.getElementById('runbtn');
  btn.disabled = true; btn.textContent = 'Running…';
  try{
    const r = await fetch('/api/run?force=1&token=' + encodeURIComponent(t));
    const msg = await r.text();
    alert(r.ok ? msg : ('Failed: ' + r.status + ' ' + msg));
  }catch(e){ alert('Error: ' + e); }
  finally{ btn.disabled = false; btn.textContent = '▶ Run scan'; setTimeout(load, 2500); }
}

let _daily = null, _rsz = null;
window.addEventListener('resize', function(){
  clearTimeout(_rsz);
  _rsz = setTimeout(function(){
    if(_daily){ document.getElementById('chartbox').innerHTML = chart(_daily); wireChartHover(); }
  }, 150);
});

load(); setInterval(load, 20000);
loadDates(); setInterval(loadDates, 60000);
</script>
</body>
</html>`
