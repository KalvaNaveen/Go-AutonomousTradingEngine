package web

// dashboardHTML is the single-page BTST dashboard. It polls /api/summary every
// 20s and renders stat cards + tabbed tables (Positions / Scan / Closed / SL
// trail / History). Kept dependency-free (no CDN) so it works on a locked-down
// free cloud box.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTST Engine</title>
<style>
  :root{
    --bg:#0b0e14;--panel:#11151f;--panel2:#161b28;--bd:#232a3b;
    --fg:#e8ecf4;--mut:#8b95a9;--dim:#5c6478;
    --grn:#34d399;--red:#f87171;--amb:#fbbf24;--acc:#60a5fa;--vio:#a78bfa;
  }
  *{box-sizing:border-box;margin:0;padding:0}
  html{scrollbar-color:var(--bd) transparent}
  body{background:var(--bg);color:var(--fg);
    font:14px/1.55 -apple-system,'Segoe UI',Roboto,'Helvetica Neue',sans-serif;
    padding:0 0 48px;min-height:100vh}
  .wrap{max-width:1180px;margin:0 auto;padding:0 20px}

  /* ── top bar ─────────────────────────────────────────── */
  header{position:sticky;top:0;z-index:10;background:rgba(11,14,20,.85);
    backdrop-filter:blur(10px);border-bottom:1px solid var(--bd)}
  .bar{display:flex;align-items:center;gap:12px;padding:14px 0;flex-wrap:wrap}
  .logo{display:flex;align-items:center;gap:10px;font-size:17px;font-weight:700;letter-spacing:.2px}
  .dot{width:9px;height:9px;border-radius:50%;background:var(--grn);box-shadow:0 0 8px var(--grn);
    animation:pulse 2.4s infinite}
  @keyframes pulse{0%,100%{opacity:1}50%{opacity:.35}}
  .badge{font-size:10.5px;font-weight:800;padding:3px 10px;border-radius:999px;letter-spacing:.8px}
  .paper{background:#1f6feb26;color:var(--acc);border:1px solid #2b5aa833}
  .live{background:#f8717126;color:var(--red);border:1px solid #a8323233}
  .spacer{margin-left:auto}
  .muted{color:var(--mut);font-size:12px}
  #runbtn{background:linear-gradient(135deg,#1d4ed8,#2563eb);color:#fff;border:0;
    border-radius:8px;padding:8px 16px;font-size:12.5px;font-weight:700;cursor:pointer;
    box-shadow:0 2px 10px #2563eb44;transition:transform .1s}
  #runbtn:hover{transform:translateY(-1px)}
  #runbtn:disabled{opacity:.5;cursor:wait;transform:none}

  /* ── stat cards ──────────────────────────────────────── */
  .cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(148px,1fr));gap:12px;margin:20px 0}
  .card{background:linear-gradient(180deg,var(--panel2),var(--panel));border:1px solid var(--bd);
    border-radius:14px;padding:14px 16px;transition:border-color .15s}
  .card:hover{border-color:#31405e}
  .card .lbl{color:var(--mut);font-size:10.5px;text-transform:uppercase;letter-spacing:.8px;font-weight:600}
  .card .val{font-size:21px;font-weight:700;margin-top:5px;font-variant-numeric:tabular-nums}
  .card .sub{color:var(--dim);font-size:11px;margin-top:2px}

  /* ── tabs ────────────────────────────────────────────── */
  .tabs{display:flex;gap:4px;border-bottom:1px solid var(--bd);margin:6px 0 16px;overflow-x:auto}
  .tab{background:none;border:0;color:var(--mut);font-size:13px;font-weight:600;cursor:pointer;
    padding:9px 14px;border-bottom:2px solid transparent;white-space:nowrap}
  .tab:hover{color:var(--fg)}
  .tab.on{color:var(--acc);border-bottom-color:var(--acc)}
  .tab .n{background:var(--panel2);border:1px solid var(--bd);border-radius:999px;
    font-size:10.5px;padding:1px 7px;margin-left:6px;color:var(--mut)}

  /* ── tables ──────────────────────────────────────────── */
  .tblwrap{overflow-x:auto;border:1px solid var(--bd);border-radius:12px;background:var(--panel)}
  table{width:100%;border-collapse:collapse;min-width:640px}
  th,td{text-align:right;padding:10px 14px;border-bottom:1px solid var(--bd);
    font-variant-numeric:tabular-nums;white-space:nowrap}
  th:first-child,td:first-child{text-align:left}
  th{color:var(--dim);font-size:10.5px;text-transform:uppercase;letter-spacing:.7px;font-weight:700;
    background:var(--panel2);position:sticky;top:0}
  tr:last-child td{border-bottom:none}
  tbody tr:hover{background:#ffffff06}
  .sym{font-weight:700;letter-spacing:.2px}
  .pos{color:var(--grn)} .neg{color:var(--red)}
  .sl{color:var(--red)} .peak{color:var(--vio)}
  .chip{display:inline-block;font-size:10px;font-weight:700;padding:2px 8px;border-radius:999px;
    letter-spacing:.3px}
  .c-traded{background:#34d39922;color:var(--grn)}
  .c-carried{background:#a78bfa22;color:var(--vio)}
  .c-dropped{background:#8b95a922;color:var(--mut)}
  .c-held{background:#fbbf2422;color:var(--amb)}
  .src{color:var(--dim);font-size:11px}
  .carry{background:#a78bfa22;color:var(--vio);border-radius:999px;font-size:10px;
    font-weight:800;padding:1px 7px;margin-left:6px;vertical-align:1px}
  .empty{color:var(--mut);padding:34px;text-align:center;font-size:13px}
  .hsub{color:var(--mut);font-size:11px;margin:16px 0 8px;text-transform:uppercase;letter-spacing:.7px;font-weight:700}
  #histdate{background:var(--panel2);color:var(--fg);border:1px solid var(--bd);border-radius:8px;
    padding:7px 12px;font-size:13px;margin-bottom:12px}
  section{display:none}
  section.on{display:block}
</style>
</head>
<body>
<header><div class="wrap bar">
  <span class="logo"><span class="dot" id="livedot"></span>BTST Engine</span>
  <span id="mode" class="badge paper">…</span>
  <span class="spacer"></span>
  <span class="muted" id="updated"></span>
  <button id="runbtn" onclick="runNow()" title="Manual scan + trade (needs trigger token)">▶ Run now</button>
</div></header>

<div class="wrap">
  <div class="cards" id="cards"></div>

  <div class="tabs">
    <button class="tab on" data-t="positions" onclick="showTab('positions')">Holdings<span class="n" id="n-pos">0</span></button>
    <button class="tab" data-t="scan" onclick="showTab('scan')">Today's Scan<span class="n" id="n-scan">0</span></button>
    <button class="tab" data-t="closed" onclick="showTab('closed')">Closed<span class="n" id="n-closed">0</span></button>
    <button class="tab" data-t="sl" onclick="showTab('sl')">SL Trail<span class="n" id="n-sl">0</span></button>
    <button class="tab" data-t="hist" onclick="showTab('hist')">History</button>
  </div>

  <section id="s-positions" class="on"><div id="open"></div></section>
  <section id="s-scan"><div class="hsub" id="scanmeta"></div><div id="scan"></div></section>
  <section id="s-closed"><div id="closed"></div></section>
  <section id="s-sl"><div id="slev"></div></section>
  <section id="s-hist">
    <select id="histdate"><option value="">Loading dates…</option></select>
    <div id="hist"></div>
  </section>
</div>

<script>
const inr = n => '₹' + Math.round(n).toLocaleString('en-IN');
const cls = n => n > 0 ? 'pos' : n < 0 ? 'neg' : '';
const sign = n => (n > 0 ? '+' : '') + n.toFixed(2);
const f2 = n => (n == null ? 0 : n).toFixed(2);

function showTab(t){
  document.querySelectorAll('.tab').forEach(b=>b.classList.toggle('on', b.dataset.t===t));
  document.querySelectorAll('section').forEach(s=>s.classList.toggle('on', s.id==='s-'+t));
}

function card(lbl, val, sub, klass){
  return '<div class="card"><div class="lbl">'+lbl+'</div><div class="val '+(klass||'')+'">'+val+'</div>'
       + (sub?'<div class="sub">'+sub+'</div>':'') + '</div>';
}
function wrapT(inner){ return '<div class="tblwrap"><table>'+inner+'</table></div>'; }

function openTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No holdings. The 15:20 IST cycle (or ▶ Run now) fills this.</div>';
  let h='<tr><th>Symbol</th><th>Qty</th><th>Entry</th><th>Last</th><th>Peak</th><th>Trail SL</th><th>Unreal P&L</th><th>%</th><th>Since</th></tr>';
  for(const p of rows){
    const carry = p.carry_count>0 ? '<span class="carry">↻'+p.carry_count+'</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+carry+'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+f2(p.last_price)+'</td><td class="peak">'+f2(p.peak_price)
      +'</td><td class="sl">'+f2(p.sl_price)
      +'</td><td class="'+cls(p.unreal_pnl||0)+'">'+sign(p.unreal_pnl||0)
      +'</td><td class="'+cls(p.unreal_pct||0)+'">'+sign(p.unreal_pct||0)+'%</td><td class="src">'+p.trade_date+'</td></tr>';
  }
  return wrapT(h);
}

function scanBadge(o){ return '<span class="chip c-'+o+'">'+o+'</span>'; }
function scanTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No scan yet today.</div>';
  let h='<tr><th>Symbol</th><th>Source</th><th>Close</th><th>Status</th><th>Reason</th></tr>';
  for(const r of rows)
    h+='<tr><td class="sym">'+r.symbol+'</td><td class="src">'+(r.source||'')+'</td><td>'+f2(r.close)
      +'</td><td>'+scanBadge(r.outcome)+'</td><td class="src">'+(r.reason||'')+'</td></tr>';
  return wrapT(h);
}

function closedTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No closed trades yet.</div>';
  let h='<tr><th>Symbol</th><th>Date</th><th>Entry</th><th>Exit</th><th>Reason</th><th>P&L</th><th>%</th></tr>';
  for(const p of rows){
    const rsn = p.exit_reason==='stoploss' ? '<span class="sl">⛔ SL</span>' : '<span class="src">square-off</span>';
    h+='<tr><td class="sym">'+p.symbol+'</td><td class="src">'+p.trade_date+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+f2(p.exit_price)+'</td><td>'+rsn
      +'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl)+'</td><td class="'+cls(p.pnl_pct)+'">'+sign(p.pnl_pct)+'%</td></tr>';
  }
  return wrapT(h);
}

function slTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No trailing-SL adjustments yet. They appear as prices rise.</div>';
  let h='<tr><th>Symbol</th><th>When</th><th>Price</th><th>Old SL</th><th>New SL</th></tr>';
  for(const e of rows)
    h+='<tr><td class="sym">'+e.symbol+'</td><td class="src">'+e.at.replace('T',' ').slice(0,19)
      +'</td><td>'+f2(e.price)+'</td><td class="src">'+f2(e.old_sl)+'</td><td class="pos">'+f2(e.new_sl)+'</td></tr>';
  return wrapT(h);
}

async function load(){
  try{
    const s = await (await fetch('/api/summary')).json();
    const m = document.getElementById('mode');
    m.textContent = s.mode; m.className = 'badge ' + (s.mode==='LIVE'?'live':'paper');
    const totPnl = (s.realized_pnl||0)+(s.unrealized_pnl||0);
    document.getElementById('cards').innerHTML =
      card('Holdings', s.open_count, s.carried_count ? s.carried_count+' carried' : '') +
      card('Deployed', inr(s.open_invested), 'of '+inr(s.capital_per_day)+'/day') +
      card('Unrealised', sign(s.unrealized_pnl||0), 'mark-to-market', cls(s.unrealized_pnl||0)) +
      card('Realised', sign(s.realized_pnl||0), s.closed_count+' closed', cls(s.realized_pnl||0)) +
      card('Total P&L', sign(totPnl), '', cls(totPnl)) +
      card('Win rate', (s.win_rate||0).toFixed(0)+'%', s.wins+' / '+s.closed_count+' wins');
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
      'Updated ' + new Date().toLocaleTimeString('en-IN',{hour12:false}) + ' · ' + s.today;
  }catch(e){
    document.getElementById('updated').textContent = 'Error: ' + e;
  }
}

function histPositions(rows){
  if(!rows||!rows.length) return '<div class="empty">No positions this day.</div>';
  let h='<tr><th>Symbol</th><th>Qty</th><th>Entry</th><th>Exit</th><th>P&L</th><th>Status</th></tr>';
  for(const p of rows){
    const closed = !!p.exit_price;
    const slm = p.exit_reason==='stoploss' ? ' <span class="sl">⛔</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+(closed? f2(p.exit_price)+slm : '—')
      +'</td><td class="'+(closed?cls(p.pnl):'')+'">'+(closed? sign(p.pnl):'—')
      +'</td><td>'+(closed?'<span class="'+cls(p.pnl)+'">closed</span>':'<span style="color:var(--amb)">open</span>')+'</td></tr>';
  }
  return wrapT(h);
}

async function loadHistory(date){
  const box = document.getElementById('hist');
  if(!date){ box.innerHTML=''; return; }
  try{
    const h = await (await fetch('/api/history?date='+encodeURIComponent(date))).json();
    box.innerHTML =
      '<div class="hsub">Scan'+(h.scan_time?' — '+h.scan_time+' IST':'')+'</div>' + scanTable(h.scan) +
      '<div class="hsub">Trades · Net P&L <span class="'+cls(h.net_pnl)+'">'+sign(h.net_pnl)+'</span></div>' + histPositions(h.positions);
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
  btn.disabled = true; btn.textContent = '⏳ running…';
  try{
    const r = await fetch('/api/run?force=1&token=' + encodeURIComponent(t));
    const msg = await r.text();
    alert(r.ok ? msg : ('Failed: ' + r.status + ' ' + msg));
  }catch(e){ alert('Error: ' + e); }
  finally{ btn.disabled = false; btn.textContent = '▶ Run now'; setTimeout(load, 2500); }
}

load(); setInterval(load, 20000);
loadDates(); setInterval(loadDates, 60000);
</script>
</body>
</html>`
