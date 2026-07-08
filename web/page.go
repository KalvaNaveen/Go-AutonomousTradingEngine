package web

// dashboardHTML is the single-page BTST dashboard, designed after modern retail
// trading apps (Groww / Robinhood): hero P&L with an SVG equity curve, ticker
// avatars, red/green pills, soft cards, pill tabs. Polls /api/summary every 20s.
// Dependency-free — the chart is hand-rolled SVG, no CDN.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTST Engine</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Manrope:wght@400;500;600;700;800&family=Space+Grotesk:wght@500;600;700&display=swap" rel="stylesheet">
<style>
  :root{
    --acc:#5367ff;--grn:#00b386;--red:#eb5b3c;--amb:#f5a623;
    --txt:#44475b;--head:#1b1e2e;--mut:#7c7e8c;--faint:#b6b8c3;
    --bg:#f7f8fc;--card:#ffffff;--bd:#ebedf5;
    --shadow:0 2px 10px rgba(20,24,60,.06);
  }
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:var(--bg);color:var(--txt);
    font:14px/1.5 'Manrope',-apple-system,'Segoe UI',Roboto,'Helvetica Neue',sans-serif;
    padding-bottom:60px}
  .wrap{max-width:1080px;margin:0 auto;padding:0 20px}

  /* ── nav ─────────────────────────────────────────────── */
  nav{background:var(--card);border-bottom:1px solid var(--bd);position:sticky;top:0;z-index:20}
  .bar{display:flex;align-items:center;gap:12px;padding:14px 0;flex-wrap:wrap}
  .logo{display:flex;align-items:center;gap:9px;font-size:16.5px;font-weight:700;color:var(--head)}
  .logomark{width:28px;height:28px;border-radius:8px;background:linear-gradient(135deg,var(--acc),#7a8cff);
    color:#fff;display:inline-flex;align-items:center;justify-content:center;font-size:13px;font-weight:800}
  .badge{font-size:10px;font-weight:700;padding:3px 10px;border-radius:999px;letter-spacing:.6px}
  .paper{background:#eef1ff;color:var(--acc)}
  .live{background:#fdeeea;color:var(--red)}
  .spacer{margin-left:auto}
  .muted{color:var(--mut);font-size:12px}
  #runbtn{background:var(--acc);color:#fff;border:0;border-radius:10px;
    padding:9px 18px;font-size:13px;font-weight:700;cursor:pointer;
    box-shadow:0 4px 12px #5367ff33;transition:all .15s}
  #runbtn:hover{background:#4356e0;transform:translateY(-1px)}
  #runbtn:disabled{opacity:.5;cursor:wait;transform:none}

  /* ── hero ────────────────────────────────────────────── */
  .hero{background:var(--card);border:1px solid var(--bd);border-radius:16px;
    box-shadow:var(--shadow);margin:20px 0;padding:22px 24px 8px;overflow:hidden}
  .hero .lbl{color:var(--mut);font-size:12px;font-weight:600}
  .heroline{display:flex;align-items:baseline;gap:14px;flex-wrap:wrap;margin-top:4px}
  .heroval{font-family:'Space Grotesk','Manrope',sans-serif;font-size:46px;font-weight:700;
    color:var(--head);font-variant-numeric:tabular-nums;letter-spacing:-1.5px;line-height:1.1}
  .heroval.pos{color:var(--grn)} .heroval.neg{color:var(--red)}
  .heroval .cur{font-size:26px;font-weight:600;opacity:.55;margin-right:2px;letter-spacing:0}
  .delta{display:inline-flex;align-items:center;gap:4px;font-size:13px;font-weight:800;
    padding:5px 12px;border-radius:999px;font-family:'Space Grotesk',sans-serif}
  .stat .val{font-family:'Space Grotesk','Manrope',sans-serif}
  #tip{position:absolute;pointer-events:none;background:var(--head);color:#fff;
    font-size:11px;font-weight:600;padding:5px 10px;border-radius:8px;white-space:nowrap;
    transform:translate(-50%,-130%);display:none;z-index:5;font-variant-numeric:tabular-nums}
  #chartbox{position:relative}
  .chips{display:flex;gap:8px;flex-wrap:wrap;margin:10px 0 4px}
  .chipk{background:var(--bg);border:1px solid var(--bd);border-radius:999px;
    padding:4px 12px;font-size:11.5px;color:var(--mut)}
  .chipk b{color:var(--txt);font-weight:600;font-variant-numeric:tabular-nums}
  .chipk b.pos{color:var(--grn)} .chipk b.neg{color:var(--red)}
  #chart{width:100%;height:150px;display:block;margin-top:6px}
  .chartempty{color:var(--faint);font-size:12.5px;text-align:center;padding:38px 0}

  /* ── quick stats ─────────────────────────────────────── */
  .stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin:0 0 20px}
  .stat{background:var(--card);border:1px solid var(--bd);border-radius:14px;
    box-shadow:var(--shadow);padding:14px 16px}
  .stat .lbl{color:var(--mut);font-size:11px;font-weight:600}
  .stat .val{font-size:19px;font-weight:700;color:var(--head);margin-top:3px;font-variant-numeric:tabular-nums}
  .stat .sub{color:var(--faint);font-size:11px;margin-top:1px}

  /* ── pill tabs ───────────────────────────────────────── */
  .tabs{display:flex;gap:8px;margin:0 0 14px;overflow-x:auto;padding-bottom:2px}
  .tab{background:var(--card);border:1px solid var(--bd);color:var(--mut);
    font-size:12.5px;font-weight:600;cursor:pointer;padding:8px 16px;border-radius:999px;
    white-space:nowrap;transition:all .12s}
  .tab:hover{color:var(--head);border-color:#d5d9ea}
  .tab.on{background:var(--head);border-color:var(--head);color:#fff}
  .tab .n{opacity:.55;margin-left:5px;font-weight:500}
  section{display:none}
  section.on{display:block}

  /* ── panels & tables ─────────────────────────────────── */
  .panel{background:var(--card);border:1px solid var(--bd);border-radius:16px;
    box-shadow:var(--shadow);overflow:hidden}
  .tblwrap{overflow-x:auto}
  table{width:100%;border-collapse:collapse;min-width:700px}
  th,td{text-align:right;padding:12px 14px;font-variant-numeric:tabular-nums;
    white-space:nowrap;font-size:13px;border-bottom:1px solid #f3f4fa}
  th:first-child,td:first-child{text-align:left;padding-left:18px}
  th:last-child,td:last-child{padding-right:18px}
  th{color:var(--mut);font-size:10.5px;text-transform:uppercase;letter-spacing:.6px;
    font-weight:600;background:#fafbff}
  tbody tr:last-child td{border-bottom:none}
  tbody tr{transition:background .1s}
  tbody tr:hover{background:#fafbff}
  .inst{display:flex;align-items:center;gap:10px}
  .av{width:30px;height:30px;border-radius:9px;display:inline-flex;align-items:center;
    justify-content:center;font-size:11px;font-weight:800;flex:none}
  .sym{font-weight:600;color:var(--head);font-size:13px}
  .symsub{color:var(--faint);font-size:10.5px;margin-top:1px}
  .pos{color:var(--grn)} .neg{color:var(--red)}
  .sl{color:var(--red)} .dimtxt{color:var(--mut);font-size:12px}
  .pill{display:inline-block;font-size:11px;font-weight:700;padding:2.5px 9px;border-radius:999px}
  .pg{background:#e5f7f1;color:var(--grn)} .pr{background:#fdeeea;color:var(--red)}
  .pn{background:#f1f2f7;color:var(--mut)}
  .chip{display:inline-block;font-size:10.5px;font-weight:700;padding:2px 9px;border-radius:999px}
  .c-traded{background:#e5f7f1;color:var(--grn)}
  .c-carried{background:#eef1ff;color:var(--acc)}
  .c-dropped{background:#f1f2f7;color:var(--mut)}
  .c-held{background:#fdf3df;color:var(--amb)}
  .carry{color:var(--acc);font-size:10px;font-weight:800;margin-left:6px;
    background:#eef1ff;border-radius:999px;padding:1px 6px}
  .empty{color:var(--mut);padding:44px;text-align:center;font-size:13px}
  .hsub{color:var(--mut);font-size:11px;margin:18px 2px 8px;text-transform:uppercase;
    letter-spacing:.6px;font-weight:600}
  #histdate{background:var(--card);color:var(--txt);border:1px solid var(--bd);border-radius:10px;
    padding:8px 12px;font-size:13px;margin-bottom:12px;box-shadow:var(--shadow)}
</style>
</head>
<body>
<nav><div class="wrap bar">
  <span class="logo"><span class="logomark">B</span>BTST Engine</span>
  <span id="mode" class="badge paper">…</span>
  <span class="spacer"></span>
  <span class="muted" id="updated"></span>
  <button id="runbtn" onclick="runNow()" title="Manual scan + trade (needs trigger token)">▶ Run scan</button>
</div></nav>

<div class="wrap">
  <div class="hero">
    <div class="lbl">Total P&L (net of charges)</div>
    <div class="heroline">
      <span class="heroval" id="heropnl">—</span>
      <span id="heropill"></span>
    </div>
    <div class="chips" id="herochips"></div>
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
    <div id="hist"></div>
  </section>
</div>

<script>
const inr = n => '₹' + Math.round(Math.abs(n)).toLocaleString('en-IN');
const sinr = n => (n<0?'−':'+') + inr(n);
const cls = n => n > 0 ? 'pos' : n < 0 ? 'neg' : '';
const sign = n => (n > 0 ? '+' : '') + n.toFixed(2);
const f2 = n => (n == null ? 0 : n).toFixed(2);
const AVC = ['#5367ff','#00b386','#eb5b3c','#f5a623','#9b59b6','#17a2b8','#e91e63','#2e7d32'];
function av(sym){
  let h=0; for(let i=0;i<sym.length;i++) h=(h*31+sym.charCodeAt(i))>>>0;
  const c=AVC[h%AVC.length];
  return '<span class="av" style="background:'+c+'1c;color:'+c+'">'+sym.slice(0,2)+'</span>';
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

/* ── equity curve (hand-rolled SVG) ── */
let _chartPts = null; // {xs, ys, dates, vals} for the hover tooltip
function chart(daily){
  if(!daily || daily.length < 2){ _chartPts = null;
    return '<div class="chartempty">Equity curve appears once a few days of trades close</div>'; }
  let cum = 0;
  const vals = daily.map(d => (cum += d.net));
  const n = vals.length, W = 720, H = 150, P = 10;
  let mn = Math.min(0, ...vals), mx = Math.max(0, ...vals);
  if(mx === mn){ mx = mn + 1; }
  const X = i => P + i*(W-2*P)/(n-1);
  const Y = v => P + (mx-v)*(H-2*P)/(mx-mn);
  const xs = [], ys = [];
  for(let i=0;i<n;i++){ xs.push(X(i)); ys.push(Y(vals[i])); }
  _chartPts = {xs:xs, ys:ys, dates:daily.map(d=>d.date), vals:vals, W:W, H:H};

  // Smooth quadratic curve through midpoints (Dribbble-style soft line).
  let line = 'M'+xs[0].toFixed(1)+','+ys[0].toFixed(1);
  for(let i=1;i<n-1;i++){
    const xc=(xs[i]+xs[i+1])/2, yc=(ys[i]+ys[i+1])/2;
    line += ' Q'+xs[i].toFixed(1)+','+ys[i].toFixed(1)+' '+xc.toFixed(1)+','+yc.toFixed(1);
  }
  line += ' L'+xs[n-1].toFixed(1)+','+ys[n-1].toFixed(1);
  const up = vals[n-1] >= 0, col = up ? '#00b386' : '#eb5b3c';
  const area = line + ' L'+xs[n-1].toFixed(1)+','+(H-P)+' L'+xs[0].toFixed(1)+','+(H-P)+' Z';
  const zero = (mn<0 && mx>0)
    ? '<line x1="'+P+'" y1="'+Y(0).toFixed(1)+'" x2="'+(W-P)+'" y2="'+Y(0).toFixed(1)+'" stroke="#d5d9ea" stroke-dasharray="4 4"/>' : '';
  return '<svg id="chart" viewBox="0 0 '+W+' '+H+'" preserveAspectRatio="none">'
    + '<defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">'
    + '<stop offset="0%" stop-color="'+col+'" stop-opacity=".22"/>'
    + '<stop offset="100%" stop-color="'+col+'" stop-opacity="0"/></linearGradient></defs>'
    + zero
    + '<path d="'+area+'" fill="url(#g)"/>'
    + '<path d="'+line+'" fill="none" stroke="'+col+'" stroke-width="2.4" stroke-linejoin="round" stroke-linecap="round"/>'
    + '<circle cx="'+xs[n-1].toFixed(1)+'" cy="'+ys[n-1].toFixed(1)+'" r="4" fill="'+col+'">'
    + '<animate attributeName="r" values="4;5.5;4" dur="2s" repeatCount="indefinite"/></circle>'
    + '<circle id="hovdot" r="4" fill="'+col+'" stroke="#fff" stroke-width="1.5" style="display:none"/>'
    + '</svg><div id="tip"></div>';
}

// Hover tooltip: nearest point → date + cumulative value.
function wireChartHover(){
  const svg = document.getElementById('chart');
  if(!svg || !_chartPts) return;
  const tip = document.getElementById('tip'), dot = document.getElementById('hovdot');
  svg.addEventListener('mousemove', function(e){
    const r = svg.getBoundingClientRect();
    const fx = (e.clientX - r.left) / r.width * _chartPts.W;
    let best = 0, bd = 1e9;
    for(let i=0;i<_chartPts.xs.length;i++){
      const d = Math.abs(_chartPts.xs[i]-fx);
      if(d < bd){ bd = d; best = i; }
    }
    dot.style.display=''; dot.setAttribute('cx', _chartPts.xs[best]); dot.setAttribute('cy', _chartPts.ys[best]);
    tip.style.display='block';
    tip.style.left = (_chartPts.xs[best]/_chartPts.W*r.width)+'px';
    tip.style.top = (_chartPts.ys[best]/_chartPts.H*r.height)+'px';
    tip.textContent = _chartPts.dates[best].slice(5) + ' · ' + sinr(_chartPts.vals[best]);
  });
  svg.addEventListener('mouseleave', function(){ tip.style.display='none'; dot.style.display='none'; });
}

// Count-up animation for the hero number (runs once per page load).
let _heroAnimated = false;
function animateHero(el, target){
  if(_heroAnimated){ el.innerHTML = fmtHero(target); return; }
  _heroAnimated = true;
  const t0 = performance.now(), dur = 850;
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

function openTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No holdings yet — the 15:20 IST cycle (or ▶ Run scan) fills this.</div>';
  let h='<tr><th>Instrument</th><th>Bought at</th><th>Qty</th><th>Entry</th><th>LTP</th><th>Peak</th><th>Trail SL</th><th>P&L</th><th>%</th></tr>';
  for(const p of rows){
    const carry = p.carry_count>0 ? '<span class="carry">↻'+p.carry_count+'</span>' : '';
    h+='<tr><td>'+inst(p.symbol, '', carry)+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td><b>'+f2(p.last_price)+'</b></td><td class="dimtxt">'+f2(p.peak_price)
      +'</td><td class="sl">'+f2(p.sl_price)
      +'</td><td class="'+cls(p.unreal_pnl||0)+'"><b>'+sign(p.unreal_pnl||0)+'</b></td><td>'+pill(p.unreal_pct||0)+'</td></tr>';
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

function closedTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No closed trades yet.</div>';
  let h='<tr><th>Instrument</th><th>Bought at</th><th>Sold at</th><th>Entry</th><th>Exit</th><th>Reason</th><th>Gross</th><th>Charges</th><th>Net P&L</th><th>Net %</th></tr>';
  for(const p of rows){
    const rsn = p.exit_reason==='stoploss' ? '<span class="sl">SL hit</span>' : '<span class="dimtxt">square-off</span>';
    h+='<tr><td>'+inst(p.symbol)+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td class="dimtxt">'+(p.exit_at||'')
      +'</td><td>'+f2(p.entry_price)+'</td><td>'+f2(p.exit_price)+'</td><td>'+rsn
      +'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl)
      +'</td><td class="dimtxt">₹'+f2(p.charges||0)
      +'</td><td class="'+cls(p.net_pnl||0)+'"><b>'+sign(p.net_pnl||0)+'</b></td><td>'+pill(p.net_pct||0)+'</td></tr>';
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
      '<span class="chipk">Realised <b class="'+cls(s.net_realized||0)+'">'+sinr(s.net_realized||0)+'</b></span>'
     +'<span class="chipk">Unrealised <b class="'+cls(s.unrealized_pnl||0)+'">'+sinr(s.unrealized_pnl||0)+'</b></span>'
     +'<span class="chipk">Charges <b>'+inr(s.total_charges||0)+'</b></span>'
     +'<span class="chipk">Win rate <b>'+(s.win_rate||0).toFixed(0)+'%</b></span>';
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
  let h='<tr><th>Instrument</th><th>Qty</th><th>Entry</th><th>Exit</th><th>Net P&L</th><th>Status</th></tr>';
  for(const p of rows){
    const closed = !!p.exit_price;
    const slm = p.exit_reason==='stoploss' ? ' <span class="sl">SL</span>' : '';
    h+='<tr><td>'+inst(p.symbol, p.entry_at||'')+'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+(closed? f2(p.exit_price)+slm : '—')
      +'</td><td class="'+(closed?cls(p.net_pnl||p.pnl):'')+'">'+(closed? sign(p.net_pnl!=null?p.net_pnl:p.pnl):'—')
      +'</td><td>'+(closed?'<span class="pill pn">closed</span>':'<span class="pill" style="background:#fdf3df;color:var(--amb)">open</span>')+'</td></tr>';
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

load(); setInterval(load, 20000);
loadDates(); setInterval(loadDates, 60000);
</script>
</body>
</html>`
