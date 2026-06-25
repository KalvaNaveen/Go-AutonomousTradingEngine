package web

// dashboardHTML is the single-page BTST dashboard. It polls /api/summary every
// 20s and renders stat cards + open/closed tables. Kept dependency-free (no CDN)
// so it works on a locked-down free cloud box.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTST Dashboard</title>
<style>
  :root{--bg:#0d1117;--card:#161b22;--bd:#30363d;--fg:#e6edf3;--mut:#8b949e;--grn:#3fb950;--red:#f85149;--accent:#58a6ff;}
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif;padding:24px;max-width:1100px;margin:0 auto}
  header{display:flex;align-items:center;gap:12px;margin-bottom:20px}
  h1{font-size:20px;font-weight:600}
  .badge{font-size:11px;font-weight:700;padding:3px 9px;border-radius:20px;letter-spacing:.5px}
  .paper{background:#1f6feb33;color:var(--accent);border:1px solid var(--accent)}
  .live{background:#f8514933;color:var(--red);border:1px solid var(--red)}
  .muted{color:var(--mut);font-size:12px;margin-left:auto}
  .cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin-bottom:24px}
  .card{background:var(--card);border:1px solid var(--bd);border-radius:10px;padding:14px 16px}
  .card .lbl{color:var(--mut);font-size:11px;text-transform:uppercase;letter-spacing:.5px}
  .card .val{font-size:22px;font-weight:600;margin-top:4px}
  h2{font-size:14px;margin:18px 0 8px;color:var(--mut);text-transform:uppercase;letter-spacing:.5px}
  table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--bd);border-radius:10px;overflow:hidden}
  th,td{text-align:right;padding:9px 12px;border-bottom:1px solid var(--bd);font-variant-numeric:tabular-nums}
  th:first-child,td:first-child{text-align:left}
  th{color:var(--mut);font-size:11px;text-transform:uppercase;font-weight:600}
  tr:last-child td{border-bottom:none}
  .sym{font-weight:600}
  .pos{color:var(--grn)} .neg{color:var(--red)}
  .sl{color:var(--red);font-size:11px}
  .empty{color:var(--mut);padding:16px;text-align:center}
  #runbtn{background:var(--card);color:var(--accent);border:1px solid var(--accent);border-radius:8px;padding:6px 12px;font-size:12px;font-weight:600;cursor:pointer}
  #runbtn:hover{background:#1f6feb22}
  #histdate{background:var(--card);color:var(--fg);border:1px solid var(--bd);border-radius:8px;padding:6px 10px;font-size:13px}
  .hsub{color:var(--mut);font-size:12px;margin:10px 0 6px;text-transform:uppercase;letter-spacing:.5px}
</style>
</head>
<body>
<header>
  <h1>BTST Engine</h1>
  <span id="mode" class="badge paper">…</span>
  <button id="runbtn" onclick="runNow()" title="Manual scan + trade (needs trigger token)">▶ Run scan now</button>
  <span class="muted" id="updated"></span>
</header>
<div class="cards" id="cards"></div>
<h2 id="scanhdr">Today's Scan</h2>
<div id="scan"></div>
<h2>Open Positions</h2>
<div id="open"></div>
<h2>Closed Trades</h2>
<div id="closed"></div>
<h2>History</h2>
<div style="margin-bottom:10px"><select id="histdate"><option value="">Loading dates…</option></select></div>
<div id="hist"></div>

<script>
const inr = n => '₹' + Math.round(n).toLocaleString('en-IN');
const cls = n => n > 0 ? 'pos' : n < 0 ? 'neg' : '';
const sign = n => (n > 0 ? '+' : '') + n.toFixed(2);

function card(lbl, val, klass='') {
  return '<div class="card"><div class="lbl">'+lbl+'</div><div class="val '+klass+'">'+val+'</div></div>';
}

function openTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No open positions.</div>';
  let h='<table><tr><th>Symbol</th><th>Qty</th><th>Entry</th><th>SL</th><th>Invested</th></tr>';
  for(const p of rows)
    h+='<tr><td class="sym">'+p.symbol+'</td><td>'+p.qty+'</td><td>'+p.entry_price.toFixed(2)
      +'</td><td class="sl">'+p.sl_price.toFixed(2)+'</td><td>'+inr(p.invested)+'</td></tr>';
  return h+'</table>';
}

function scanBadge(o){
  if(o==='traded') return '<span class="pos">● traded</span>';
  if(o==='held')   return '<span style="color:#d29922">● held</span>';
  return '<span style="color:var(--mut)">● dropped</span>';
}
function scanTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No scan yet today. The 15:20 IST scan will appear here.</div>';
  let h='<table><tr><th>Symbol</th><th>Close</th><th>Status</th><th>Reason</th></tr>';
  for(const r of rows)
    h+='<tr><td class="sym">'+r.symbol+'</td><td>'+r.close.toFixed(2)+'</td><td>'
      +scanBadge(r.outcome)+'</td><td style="color:var(--mut)">'+(r.reason||'')+'</td></tr>';
  return h+'</table>';
}

function closedTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No closed trades yet.</div>';
  let h='<table><tr><th>Symbol</th><th>Date</th><th>Entry</th><th>Exit</th><th>P&L</th><th>%</th></tr>';
  for(const p of rows){
    const slm = p.exit_reason==='stoploss' ? ' <span class="sl">⛔SL</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+'</td><td>'+p.trade_date+'</td><td>'+p.entry_price.toFixed(2)
      +'</td><td>'+p.exit_price.toFixed(2)+slm+'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl)
      +'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl_pct)+'%</td></tr>';
  }
  return h+'</table>';
}

async function load(){
  try{
    const s = await (await fetch('/api/summary')).json();
    const m = document.getElementById('mode');
    m.textContent = s.mode; m.className = 'badge ' + (s.mode==='LIVE'?'live':'paper');
    document.getElementById('cards').innerHTML =
      card('Open Positions', s.open_count) +
      card('Deployed (open)', inr(s.open_invested)) +
      card('Closed Trades', s.closed_count) +
      card('Realised P&L', sign(s.realized_pnl), cls(s.realized_pnl)) +
      card('Return', sign(s.return_pct)+'%', cls(s.return_pct)) +
      card('Win Rate', s.win_rate.toFixed(0)+'%');
    document.getElementById('scanhdr').textContent =
      'Today’s Scan' + (s.scan_date
        ? ' — ' + s.scan_date + (s.scan_time ? ' ' + s.scan_time : '')
          + ' IST  (' + s.traded_count + '/' + s.scanned_count + ' traded)'
        : '');
    document.getElementById('scan').innerHTML = scanTable(s.scan);
    document.getElementById('open').innerHTML = openTable(s.open);
    document.getElementById('closed').innerHTML = closedTable(s.closed);
    document.getElementById('updated').textContent =
      'Updated ' + new Date().toLocaleTimeString('en-IN') + ' · ' + s.today;
  }catch(e){
    document.getElementById('updated').textContent = 'Error: ' + e;
  }
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
  finally{ btn.disabled = false; btn.textContent = '▶ Run scan now'; setTimeout(load, 1500); }
}
function histPositions(rows){
  if(!rows||!rows.length) return '<div class="empty">No positions this day.</div>';
  let h='<table><tr><th>Symbol</th><th>Qty</th><th>Entry</th><th>Exit</th><th>P&L</th><th>Status</th></tr>';
  for(const p of rows){
    const closed = !!p.exit_price;
    const slm = p.exit_reason==='stoploss' ? ' <span class="sl">⛔SL</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+'</td><td>'+p.qty+'</td><td>'+p.entry_price.toFixed(2)
      +'</td><td>'+(closed? p.exit_price.toFixed(2)+slm : '—')
      +'</td><td class="'+(closed?cls(p.pnl):'')+'">'+(closed? sign(p.pnl):'—')
      +'</td><td>'+(closed?'<span class="'+cls(p.pnl)+'">closed</span>':'<span style="color:#d29922">open</span>')+'</td></tr>';
  }
  return h+'</table>';
}

async function loadHistory(date){
  const box = document.getElementById('hist');
  if(!date){ box.innerHTML=''; return; }
  try{
    const h = await (await fetch('/api/history?date='+encodeURIComponent(date))).json();
    box.innerHTML =
      '<div class="hsub">Scan'+(h.scan_time?' — '+h.scan_time+' IST':'')+'</div>' + scanTable(h.scan) +
      '<div class="hsub">Trades  ·  Net P&L: <span class="'+cls(h.net_pnl)+'">'+sign(h.net_pnl)+'</span></div>' + histPositions(h.positions);
  }catch(e){ box.innerHTML = '<div class="empty">Error: '+e+'</div>'; }
}

async function loadDates(){
  try{
    const dates = await (await fetch('/api/dates')).json();
    const sel = document.getElementById('histdate');
    if(!dates || !dates.length){ sel.innerHTML='<option value="">No history yet</option>'; return; }
    sel.innerHTML = dates.map(d=>'<option value="'+d+'">'+d+'</option>').join('');
    sel.onchange = () => loadHistory(sel.value);
    loadHistory(dates[0]);
  }catch(e){ document.getElementById('histdate').innerHTML='<option>Error</option>'; }
}

load(); setInterval(load, 20000);
loadDates(); setInterval(loadDates, 60000);
</script>
</body>
</html>`
