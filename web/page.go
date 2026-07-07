package web

// dashboardHTML is the single-page BTST dashboard, styled after Zerodha
// Kite/Console: white, thin borders, dense readable tables, colour only where
// it carries meaning. Polls /api/summary every 20s. Dependency-free (no CDN).
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTST Engine</title>
<style>
  :root{
    --blue:#387ed1;--grn:#4caf50;--red:#df514c;--amb:#de9b00;
    --txt:#444;--mut:#9b9b9b;--faint:#cfcfcf;--bd:#e8e8e8;--bg2:#f9f9f9;
  }
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:#fff;color:var(--txt);
    font:13px/1.5 'Inter','Helvetica Neue',Helvetica,Arial,sans-serif;
    padding-bottom:56px}
  .wrap{max-width:1100px;margin:0 auto;padding:0 20px}

  /* ── top bar ─────────────────────────────────────────── */
  header{position:sticky;top:0;z-index:10;background:#fff;border-bottom:1px solid var(--bd)}
  .bar{display:flex;align-items:center;gap:10px;padding:12px 0;flex-wrap:wrap}
  .logo{font-size:15px;font-weight:600;color:#333}
  .badge{font-size:10px;font-weight:600;padding:2px 8px;border-radius:3px;letter-spacing:.5px}
  .paper{background:#e8f1fb;color:var(--blue)}
  .live{background:#fdeeed;color:var(--red)}
  .spacer{margin-left:auto}
  .muted{color:var(--mut);font-size:11.5px}
  #runbtn{background:var(--blue);color:#fff;border:0;border-radius:3px;
    padding:7px 14px;font-size:12px;font-weight:600;cursor:pointer}
  #runbtn:hover{background:#3272bd}
  #runbtn:disabled{opacity:.5;cursor:wait}

  /* ── stat strip ──────────────────────────────────────── */
  .cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));
    gap:0;margin:18px 0;border:1px solid var(--bd);border-radius:4px;background:#fff}
  .card{padding:13px 16px;border-right:1px solid var(--bd)}
  .card:last-child{border-right:0}
  .card .lbl{color:var(--mut);font-size:10.5px;text-transform:uppercase;letter-spacing:.5px}
  .card .val{font-size:18px;font-weight:500;margin-top:4px;font-variant-numeric:tabular-nums;color:#333}
  .card .val.pos{color:var(--grn)} .card .val.neg{color:var(--red)}
  .card .sub{color:var(--mut);font-size:11px;margin-top:1px}

  /* ── tabs ────────────────────────────────────────────── */
  .tabs{display:flex;gap:22px;border-bottom:1px solid var(--bd);margin:4px 0 0;overflow-x:auto}
  .tab{background:none;border:0;color:var(--mut);font-size:12.5px;font-weight:500;cursor:pointer;
    padding:9px 2px;border-bottom:2px solid transparent;white-space:nowrap}
  .tab:hover{color:var(--txt)}
  .tab.on{color:var(--blue);border-bottom-color:var(--blue);font-weight:600}
  .tab .n{color:var(--faint);font-size:11px;margin-left:4px}
  .tab.on .n{color:var(--blue)}
  section{display:none;padding-top:14px}
  section.on{display:block}

  /* ── tables ──────────────────────────────────────────── */
  .tblwrap{overflow-x:auto}
  table{width:100%;border-collapse:collapse;min-width:640px}
  th,td{text-align:right;padding:9px 12px;border-bottom:1px solid #f1f1f1;
    font-variant-numeric:tabular-nums;white-space:nowrap;font-size:12.5px}
  th:first-child,td:first-child{text-align:left;padding-left:2px}
  th:last-child,td:last-child{padding-right:2px}
  th{color:var(--mut);font-size:10.5px;text-transform:uppercase;letter-spacing:.5px;
    font-weight:500;border-bottom:1px solid var(--bd)}
  tbody tr:hover{background:var(--bg2)}
  .sym{font-weight:600;color:#333}
  .pos{color:var(--grn)} .neg{color:var(--red)}
  .sl{color:var(--red)} .dimtxt{color:var(--mut);font-size:11.5px}
  .chip{display:inline-block;font-size:10px;font-weight:600;padding:1px 7px;border-radius:2px}
  .c-traded{background:#edf7ee;color:var(--grn)}
  .c-carried{background:#e8f1fb;color:var(--blue)}
  .c-dropped{background:#f5f5f5;color:var(--mut)}
  .c-held{background:#fdf6e3;color:var(--amb)}
  .carry{color:var(--blue);font-size:10.5px;font-weight:600;margin-left:5px}
  .empty{color:var(--mut);padding:40px;text-align:center;font-size:12.5px}
  .hsub{color:var(--mut);font-size:10.5px;margin:18px 0 6px;text-transform:uppercase;letter-spacing:.5px;font-weight:500}
  #histdate{background:#fff;color:var(--txt);border:1px solid var(--bd);border-radius:3px;
    padding:6px 10px;font-size:12.5px;margin-bottom:10px}
</style>
</head>
<body>
<header><div class="wrap bar">
  <span class="logo">BTST Engine</span>
  <span id="mode" class="badge paper">…</span>
  <span class="spacer"></span>
  <span class="muted" id="updated"></span>
  <button id="runbtn" onclick="runNow()" title="Manual scan + trade (needs trigger token)">Run scan</button>
</div></header>

<div class="wrap">
  <div class="cards" id="cards"></div>

  <div class="tabs">
    <button class="tab on" data-t="positions" onclick="showTab('positions')">Holdings<span class="n" id="n-pos">0</span></button>
    <button class="tab" data-t="scan" onclick="showTab('scan')">Today's scan<span class="n" id="n-scan">0</span></button>
    <button class="tab" data-t="closed" onclick="showTab('closed')">Closed<span class="n" id="n-closed">0</span></button>
    <button class="tab" data-t="sl" onclick="showTab('sl')">SL trail<span class="n" id="n-sl">0</span></button>
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
  if(!rows||!rows.length) return '<div class="empty">No holdings. The 15:20 IST cycle (or Run scan) fills this.</div>';
  let h='<tr><th>Instrument</th><th>Entry at</th><th>Qty</th><th>Entry</th><th>LTP</th><th>Peak</th><th>Trail SL</th><th>P&L</th><th>%</th></tr>';
  for(const p of rows){
    const carry = p.carry_count>0 ? '<span class="carry">↻'+p.carry_count+'</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+carry+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+f2(p.last_price)+'</td><td class="dimtxt">'+f2(p.peak_price)
      +'</td><td class="sl">'+f2(p.sl_price)
      +'</td><td class="'+cls(p.unreal_pnl||0)+'">'+sign(p.unreal_pnl||0)
      +'</td><td class="'+cls(p.unreal_pct||0)+'">'+sign(p.unreal_pct||0)+'%</td></tr>';
  }
  return wrapT(h);
}

function scanBadge(o){ return '<span class="chip c-'+o+'">'+o+'</span>'; }
function scanTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No scan yet today.</div>';
  let h='<tr><th>Instrument</th><th>Source</th><th>Chg%</th><th>Close</th><th>Status</th><th>Reason</th></tr>';
  for(const r of rows)
    h+='<tr><td class="sym">'+r.symbol+'</td><td class="dimtxt">'+(r.source||'')+'</td><td class="'+cls(r.per_chg||0)+'">'+sign(r.per_chg||0)+'%</td><td>'+f2(r.close)
      +'</td><td>'+scanBadge(r.outcome)+'</td><td class="dimtxt">'+(r.reason||'')+'</td></tr>';
  return wrapT(h);
}

function closedTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No closed trades yet.</div>';
  let h='<tr><th>Instrument</th><th>Entry at</th><th>Exit at</th><th>Entry</th><th>Exit</th><th>Reason</th><th>Gross P&L</th><th>Charges</th><th>Net P&L</th><th>Net %</th></tr>';
  for(const p of rows){
    const rsn = p.exit_reason==='stoploss' ? '<span class="sl">SL hit</span>' : '<span class="dimtxt">square-off</span>';
    h+='<tr><td class="sym">'+p.symbol+'</td><td class="dimtxt">'+(p.entry_at||p.trade_date)
      +'</td><td class="dimtxt">'+(p.exit_at||'')+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+f2(p.exit_price)+'</td><td>'+rsn
      +'</td><td class="'+cls(p.pnl)+'">'+sign(p.pnl)
      +'</td><td class="dimtxt">'+f2(p.charges||0)
      +'</td><td class="'+cls(p.net_pnl||0)+'">'+sign(p.net_pnl||0)
      +'</td><td class="'+cls(p.net_pct||0)+'">'+sign(p.net_pct||0)+'%</td></tr>';
  }
  return wrapT(h);
}

function slTable(rows){
  if(!rows||!rows.length) return '<div class="empty">No trailing-SL adjustments yet. They appear as prices rise.</div>';
  let h='<tr><th>Instrument</th><th>When</th><th>Price</th><th>Old SL</th><th>New SL</th></tr>';
  for(const e of rows)
    h+='<tr><td class="sym">'+e.symbol+'</td><td class="dimtxt">'+e.at.replace('T',' ').slice(0,19)
      +'</td><td>'+f2(e.price)+'</td><td class="dimtxt">'+f2(e.old_sl)+'</td><td class="pos">'+f2(e.new_sl)+'</td></tr>';
  return wrapT(h);
}

async function load(){
  try{
    const s = await (await fetch('/api/summary')).json();
    const m = document.getElementById('mode');
    m.textContent = s.mode; m.className = 'badge ' + (s.mode==='LIVE'?'live':'paper');
    const netTot = (s.net_realized||0)+(s.unrealized_pnl||0);
    document.getElementById('cards').innerHTML =
      card('Holdings', s.open_count, s.carried_count ? s.carried_count+' carried' : '&nbsp;') +
      card('Deployed', inr(s.open_invested), 'of '+inr(s.capital_per_day)+'/day') +
      card('Unrealised', sign(s.unrealized_pnl||0), 'mark-to-market', cls(s.unrealized_pnl||0)) +
      card('Realised (net)', sign(s.net_realized||0), 'gross '+sign(s.realized_pnl||0)+' − '+(s.total_charges||0).toFixed(0)+' chg', cls(s.net_realized||0)) +
      card('Total P&L', sign(netTot), 'net of charges', cls(netTot)) +
      card('Win rate', (s.win_rate||0).toFixed(0)+'%', s.wins+' of '+s.closed_count);
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
  let h='<tr><th>Instrument</th><th>Qty</th><th>Entry</th><th>Exit</th><th>P&L</th><th>Status</th></tr>';
  for(const p of rows){
    const closed = !!p.exit_price;
    const slm = p.exit_reason==='stoploss' ? ' <span class="sl">SL</span>' : '';
    h+='<tr><td class="sym">'+p.symbol+'</td><td>'+p.qty+'</td><td>'+f2(p.entry_price)
      +'</td><td>'+(closed? f2(p.exit_price)+slm : '—')
      +'</td><td class="'+(closed?cls(p.pnl):'')+'">'+(closed? sign(p.pnl):'—')
      +'</td><td>'+(closed?'<span class="dimtxt">closed</span>':'<span style="color:var(--amb)">open</span>')+'</td></tr>';
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
      '<div class="hsub">Trades · net P&L <span class="'+cls(h.net_pnl)+'">'+sign(h.net_pnl)+'</span></div>' + histPositions(h.positions);
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
  finally{ btn.disabled = false; btn.textContent = 'Run scan'; setTimeout(load, 2500); }
}

load(); setInterval(load, 20000);
loadDates(); setInterval(loadDates, 60000);
</script>
</body>
</html>`
