import { useState, useEffect, useRef, useCallback } from 'react';
import './index.css';

// ═══════════════════════════════════════════════════════════
//  FIXED-POSITION TOOLTIP (never clipped by overflow:hidden)
// ═══════════════════════════════════════════════════════════
function Tooltip({ text, children, placement = 'bottom' }) {
  const ref = useRef(null);
  const [visible, setVisible] = useState(false);
  const [coords, setCoords] = useState({ top: 0, left: 0 });

  const show = () => {
    if (!ref.current || !text) return;
    const r = ref.current.getBoundingClientRect();
    const tipW = 240;
    let left = r.left + r.width / 2 - tipW / 2;
    let top = placement === 'right' ? r.top : r.bottom + 6;
    if (placement === 'right') left = r.right + 8;
    // clamp to viewport
    if (left < 8) left = 8;
    if (left + tipW > window.innerWidth - 8) left = window.innerWidth - tipW - 8;
    setCoords({ top, left });
    setVisible(true);
  };

  return (
    <>
      <span ref={ref} onMouseEnter={show} onMouseLeave={() => setVisible(false)}
        style={{ display: 'inline-flex', alignItems: 'center' }}>
        {children}
      </span>
      {visible && (
        <div style={{
          position: 'fixed', top: coords.top, left: coords.left,
          zIndex: 99999, width: 240,
          background: '#0d1424', border: '1px solid var(--border-bright)',
          borderRadius: 8, padding: '8px 12px',
          fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.6,
          boxShadow: '0 8px 28px rgba(0,0,0,0.55)',
          pointerEvents: 'none', whiteSpace: 'normal',
        }}>
          {text}
        </div>
      )}
    </>
  );
}

// ═══════════════════════════════════════════════════════════
//  CUSTOM SELECT (fixed-position, never clipped by overflow)
// ═══════════════════════════════════════════════════════════
const SELECT_OPTIONS = {
  strategy: [
    { value: 'ALL',           label: 'All Strategies' },
    { value: 'VCP_BREAKOUT',  label: 'VCP Breakout' },
  ],
};

function Select({ value, onChange, options }) {
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState({ top: 0, left: 0, width: 140 });
  const triggerRef = useRef(null);

  const toggle = () => {
    if (!triggerRef.current) return;
    const r = triggerRef.current.getBoundingClientRect();
    setCoords({ top: r.bottom + 2, left: r.left, width: r.width });
    setOpen(o => !o);
  };

  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);

  const selected = options.find(o => o.value === value);

  return (
    <>
      <div ref={triggerRef} onMouseDown={e => e.stopPropagation()} onClick={toggle} style={{
        height: 28, fontSize: 11, padding: '0 8px', width: 140,
        background: 'var(--bg-input)', color: 'var(--text-primary)',
        border: `1px solid ${open ? 'var(--blue)' : 'var(--border)'}`,
        borderRadius: 'var(--r-sm)', cursor: 'pointer',
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        userSelect: 'none', boxSizing: 'border-box',
      }}>
        <span>{selected?.label || value}</span>
        <span style={{ fontSize: 9, opacity: 0.6, marginLeft: 4 }}>{open ? '▲' : '▼'}</span>
      </div>
      {open && (
        <div
          onMouseDown={e => e.stopPropagation()}
          style={{
            position: 'fixed', top: coords.top, left: coords.left, width: coords.width,
            zIndex: 99999, background: '#141820', border: '1px solid var(--border-bright)',
            borderRadius: 'var(--r-sm)', boxShadow: '0 8px 28px rgba(0,0,0,0.6)',
            overflow: 'hidden',
          }}>
          {options.map(o => (
            <div key={o.value}
              onClick={() => { onChange(o.value); setOpen(false); }}
              style={{
                padding: '7px 10px', fontSize: 11, cursor: 'pointer',
                background: o.value === value ? 'var(--bg-hover)' : 'transparent',
                color: o.value === value ? 'var(--blue)' : 'var(--text-primary)',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--bg-hover)'; }}
              onMouseLeave={e => { e.currentTarget.style.background = o.value === value ? 'var(--bg-hover)' : 'transparent'; }}
            >
              {o.label}
            </div>
          ))}
        </div>
      )}
    </>
  );
}

// ═══════════════════════════════════════════════════════════
//  HELPERS
// ═══════════════════════════════════════════════════════════
const fmt = (n, decimals = 0) => {
  if (n == null || isNaN(n)) return '—';
  return Math.abs(n).toLocaleString('en-IN', { maximumFractionDigits: decimals, minimumFractionDigits: decimals });
};
const fmtSigned = (n, decimals = 0) => {
  if (n == null || isNaN(n)) return '—';
  const abs = Math.abs(n).toLocaleString('en-IN', { maximumFractionDigits: decimals, minimumFractionDigits: decimals });
  return (n >= 0 ? '+' : '-') + abs;
};
const pnlClass  = v  => parseFloat(v) > 0 ? 'pnl-pos' : parseFloat(v) < 0 ? 'pnl-neg' : 'pnl-zero';
const pnlStr    = v  => { const n = parseFloat(v || 0); return (n >= 0 ? '+' : '') + '₹' + Math.abs(n).toLocaleString('en-IN', { maximumFractionDigits: 0 }); };
const regimeColor = r => ({ AGGRESSIVE: 'var(--green)', NORMAL: 'var(--blue)', DEFENSIVE: 'var(--red)', REDUCED_CAPITAL: 'var(--orange)' }[r] || 'var(--text-muted)');

// ═══════════════════════════════════════════════════════════
//  TOOLTIP TRIGGER ("?" badge)
// ═══════════════════════════════════════════════════════════
const TIPS = {
  strategy:            "Which signal to test. 'All Strategies' runs every signal type in order.",
  capital:             "Starting capital in rupees. Position sizes are derived as a % of the running capital (compounding).",
  start_bar_offset:    "Trading days to replay from your live price cache. 250 bars ≈ 1 year.",
  sl_floor_pct:        "Minimum stop-loss distance. SL never placed closer than this % to entry.",
  sl_ceiling_pct:      "Maximum stop-loss distance. SL never wider than this % from entry.",
  max_trade_alloc_pct: "Capital % per trade. 20% on ₹1L = ₹20,000 per position. Applied to RUNNING capital.",
  max_positions:       "Max simultaneous positions. 0 = auto (capital ÷ ₹15,000).",
  slippage_pct:        "Fill-cost added above the signal price. 0.3% simulates real-world slippage.",
};
function Tip({ id }) {
  return (
    <Tooltip text={TIPS[id]}>
      <span style={{
        display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
        width: 14, height: 14, borderRadius: '50%',
        background: 'var(--bg-hover)', border: '1px solid var(--border-bright)',
        color: 'var(--text-muted)', fontSize: 8, fontWeight: 700,
        cursor: 'help', marginLeft: 4, flexShrink: 0,
      }}>?</span>
    </Tooltip>
  );
}

// ═══════════════════════════════════════════════════════════
//  MINI EQUITY CHART
// ═══════════════════════════════════════════════════════════
function EquityChart({ data, height = 60, color }) {
  const clean = (data || []).filter(v => typeof v === 'number' && isFinite(v));
  if (clean.length < 2) return <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-muted)', fontSize: 11 }}>No data</div>;
  const w = 400, h = height, pad = 2;
  const min = Math.min(...clean), max = Math.max(...clean), range = (max - min) || 1;
  const pts = clean.map((v, i) => [pad + (i / (clean.length - 1)) * (w - pad * 2), pad + (1 - (v - min) / range) * (h - pad * 2)]);
  const lineStr = pts.map(p => p.join(',')).join(' ');
  const areaStr = `${lineStr} ${w - pad},${h} ${pad},${h}`;
  const c = color || (clean[clean.length - 1] >= clean[0] ? 'var(--green)' : 'var(--red)');
  const id = `g${Math.random().toString(36).slice(2, 7)}`;
  return (
    <svg width="100%" height={height} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <defs><linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stopColor={c} stopOpacity="0.25" />
        <stop offset="100%" stopColor={c} stopOpacity="0" />
      </linearGradient></defs>
      <polygon fill={`url(#${id})`} points={areaStr} />
      <polyline fill="none" stroke={c} strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" points={lineStr} />
      <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r="3" fill={c} stroke="var(--bg-card)" strokeWidth="1.5" />
    </svg>
  );
}

// ═══════════════════════════════════════════════════════════
//  POSITIONS TABLE
// ═══════════════════════════════════════════════════════════
function PositionsTable({ positions }) {
  if (!positions || positions.length === 0) return (
    <div className="empty-state">
      <div className="empty-icon">📡</div>
      <div className="empty-title">No open positions</div>
      <div className="empty-sub">Engine is scanning for signals</div>
    </div>
  );
  return (
    <div className="table-wrap" style={{ maxHeight: 340, overflowY: 'auto' }}>
      <table>
        <thead><tr>
          <th>Date</th><th>Symbol</th><th>Strategy</th><th>Type</th><th>Qty</th>
          <th>Entry ₹</th><th>SL ₹</th><th>LTP ₹</th><th>Target ₹</th>
          <th className="td-right">P&L ₹</th><th className="td-right">P&L %</th>
        </tr></thead>
        <tbody>
          {positions.map((pos, i) => {
            const pnl = parseFloat(pos.unrealised_pnl || 0);
            const pnlPct = parseFloat(pos.pnl_pct || 0);
            const entryDate = pos.entry_date || pos.entry_time?.substring(0, 10) || '—';
            return (
              <tr key={i} className="fade-in">
                <td className="td-mono" style={{ color: 'var(--text-muted)', fontSize: 11 }}>{entryDate}</td>
                <td><span className="symbol-chip">{pos.symbol}</span></td>
                <td><span className="badge badge-strat">{(pos.strategy || '').replace(/^S\d+_/, '')}</span></td>
                <td><span className={`badge ${pos.is_short ? 'badge-sell' : 'badge-buy'}`}>{pos.is_short ? 'SHORT' : 'LONG'}</span></td>
                <td className="td-mono">{pos.qty}</td>
                <td className="td-mono">₹{fmt(pos.entry_price, 2)}</td>
                <td className="td-mono" style={{ color: 'var(--red-text)' }}>₹{fmt(pos.stop_price, 2)}</td>
                <td className="td-mono" style={{ color: 'var(--blue)', fontWeight: 700 }}>{pos.ltp ? '₹' + fmt(pos.ltp, 2) : '—'}</td>
                <td className="td-mono" style={{ color: 'var(--text-muted)' }}>{pos.target > 0 ? '₹' + fmt(pos.target, 1) : '—'}</td>
                <td className={`td-right td-mono ${pnlClass(pnl)}`} style={{ fontWeight: 700 }}>{pnlStr(pnl)}</td>
                <td className={`td-right td-mono ${pnlClass(pnlPct)}`}>{fmtSigned(pnlPct, 2)}%</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  ACTIVITY FEED
// ═══════════════════════════════════════════════════════════
function ActivityFeed({ logs }) {
  const getType = a => { const s = (a || '').toLowerCase(); return s.includes('exec') || s.includes('trade') ? 'trade' : s.includes('scan') ? 'scan' : s.includes('risk') || s.includes('sl') ? 'risk' : 'system'; };
  const icons = { trade: '💹', scan: '🔍', risk: '🛡️', system: '⚙️' };
  return (
    <div className="activity-feed" style={{ maxHeight: '100%', overflowY: 'auto' }}>
      {!(logs || []).length ? (
        <div className="empty-state" style={{ padding: '32px 16px' }}>
          <div style={{ fontSize: 24, opacity: 0.3 }}>⏳</div>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>Waiting for activity…</div>
        </div>
      ) : [...(logs || [])].reverse().slice(0, 50).map((e, i) => {
        const type = getType(e.agent);
        return (
          <div className="activity-item" key={i}>
            <div className={`activity-icon ${type}`}>{icons[type]}</div>
            <div className="activity-body">
              <div className="activity-title">{e.action} {e.detail}</div>
              <div className="activity-meta">{e.agent} • {e.time}</div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  JOURNAL TAB
// ═══════════════════════════════════════════════════════════
function TabJournal() {
  const [trades, setTrades] = useState([]);
  const [loading, setLoading] = useState(true);
  const [availableDates, setAvailableDates] = useState([]);
  const [selectedDate, setSelectedDate] = useState(new Date().toISOString().split('T')[0]);

  useEffect(() => { fetch('/api/trades/dates').then(r => r.json()).then(d => setAvailableDates(d.dates || [])).catch(() => {}); }, []);
  useEffect(() => {
    setLoading(true);
    fetch(`/api/trades?date=${selectedDate}`).then(r => r.json()).then(d => { setTrades(d.trades || []); setLoading(false); }).catch(() => setLoading(false));
  }, [selectedDate]);

  const navigate = dir => {
    const idx = availableDates.indexOf(selectedDate);
    if (dir === 'prev' && idx < availableDates.length - 1) setSelectedDate(availableDates[idx + 1]);
    if (dir === 'next' && idx > 0) setSelectedDate(availableDates[idx - 1]);
  };
  const isToday = selectedDate === new Date().toISOString().split('T')[0];
  const wins = trades.filter(t => parseFloat(t.gross_pnl || 0) > 0).length;
  const losses = trades.filter(t => parseFloat(t.gross_pnl || 0) <= 0).length;
  const dayPnl = trades.reduce((s, t) => s + parseFloat(t.gross_pnl || 0), 0);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      <div className="date-nav">
        <button className="date-nav-btn" onClick={() => navigate('prev')}>◀</button>
        <input type="date" value={selectedDate} onChange={e => setSelectedDate(e.target.value)} className="field-input" style={{ width: 140, padding: '4px 10px', fontSize: 12 }} />
        <button className="date-nav-btn" onClick={() => navigate('next')} disabled={isToday}>▶</button>
        <button className="date-nav-btn" onClick={() => setSelectedDate(new Date().toISOString().split('T')[0])}>Today</button>
        <div style={{ flex: 1 }} />
        {trades.length > 0 && (
          <div style={{ display: 'flex', gap: 16, fontSize: 12 }}>
            <span style={{ color: 'var(--text-muted)' }}>Trades: <strong style={{ color: 'var(--text-primary)' }}>{trades.length}</strong></span>
            <span style={{ color: 'var(--green-text)' }}>W: {wins}</span>
            <span style={{ color: 'var(--red-text)' }}>L: {losses}</span>
            <span className={pnlClass(dayPnl)} style={{ fontWeight: 700 }}>{pnlStr(dayPnl)}</span>
          </div>
        )}
        <div style={{ display: 'flex', gap: 4 }}>
          {availableDates.slice(0, 5).map(d => (
            <button key={d} className={`date-chip ${d === selectedDate ? 'active' : ''}`} onClick={() => setSelectedDate(d)}>
              {new Date(d + 'T00:00:00').toLocaleDateString('en-IN', { day: 'numeric', month: 'short' })}
            </button>
          ))}
        </div>
      </div>
      <div className="table-wrap" style={{ flex: 1, overflowY: 'auto' }}>
        {loading ? <div className="empty-state"><div className="spinner" /></div>
          : trades.length === 0 ? <div className="empty-state"><div className="empty-icon">📭</div><div className="empty-title">No trades</div><div className="empty-sub">No trading activity on this date</div></div>
          : (
            <table>
              <thead><tr>
                <th>Time</th><th>Symbol</th><th>Strategy</th><th>Regime</th>
                <th>Qty</th><th>Entry ₹</th><th>Exit ₹</th><th>Exit Reason</th>
                <th className="td-right">Gross P&L</th><th className="td-right">P&L %</th>
              </tr></thead>
              <tbody>
                {[...trades].reverse().map((t, i) => {
                  const pnl = parseFloat(t.gross_pnl || 0);
                  const pnlPct = t.entry_price > 0 ? ((t.full_exit_price - t.entry_price) / t.entry_price * 100) : 0;
                  return (
                    <tr key={i}>
                      <td className="td-mono" style={{ color: 'var(--text-muted)', fontSize: 11 }}>{t.entry_time?.substring(11, 19)}</td>
                      <td><span className="symbol-chip">{t.symbol}</span></td>
                      <td><span className="badge badge-strat">{t.strategy}</span></td>
                      <td style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t.regime || '—'}</td>
                      <td className="td-mono">{t.qty}</td>
                      <td className="td-mono">₹{fmt(t.entry_price, 2)}</td>
                      <td className="td-mono">₹{fmt(t.full_exit_price, 2)}</td>
                      <td><span className="badge badge-gray">{t.exit_reason}</span></td>
                      <td className={`td-right td-mono ${pnlClass(pnl)}`} style={{ fontWeight: 700 }}>{pnlStr(pnl)}</td>
                      <td className={`td-right td-mono ${pnlClass(pnlPct)}`}>{fmtSigned(pnlPct, 2)}%</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
      </div>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  MINI TRADE CHART
// ═══════════════════════════════════════════════════════════
function TradeChart({ trade, onClose }) {
  const prices = trade?.price_slice;
  if (!prices || prices.length < 2) return null;
  const W = 560, H = 130, padL = 46, padR = 8, padT = 12, padB = 24;
  const min = Math.min(...prices) * 0.997, max = Math.max(...prices) * 1.003, range = (max - min) || 1;
  const entryIdx = trade.entry_bar - trade.price_slice_start;
  const exitIdx  = Math.min(trade.exit_bar - trade.price_slice_start, prices.length - 1);
  const px = i => padL + (i / (prices.length - 1)) * (W - padL - padR);
  const py = v => padT + (1 - (v - min) / range) * (H - padT - padB);
  const lineStr = prices.map((v, i) => `${px(i).toFixed(1)},${py(v).toFixed(1)}`).join(' ');
  const ep = { x: px(entryIdx), y: py(prices[entryIdx] ?? trade.entry_price) };
  const xp = { x: px(exitIdx),  y: py(prices[exitIdx]  ?? trade.exit_price) };
  const netPnl = trade.net_pnl ?? trade.gross_pnl ?? 0;
  const pos = netPnl >= 0;
  const fmtD = d => { try { return new Date(d).toLocaleDateString('en-IN', { day: 'numeric', month: 'short', year: '2-digit' }); } catch { return d || ''; } };
  return (
    <div style={{ background: '#101520', border: '1px solid var(--border)', borderRadius: 8, padding: '10px 14px', marginBottom: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <span className="symbol-chip" style={{ fontSize: 12 }}>{trade.symbol}</span>
          <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>
            ₹{trade.entry_price?.toFixed(2)} → ₹{trade.exit_price?.toFixed(2)}
            {'  ·  '}<span className={pnlClass(netPnl)} style={{ fontWeight: 700 }}>{pnlStr(netPnl)} net</span>
            {trade.charges > 0 && <span style={{ color: 'var(--text-muted)', marginLeft: 4 }}>(−₹{fmt(trade.charges, 0)} charges)</span>}
            {'  ·  '}<span style={{ color: 'var(--text-muted)' }}>{trade.exit_reason}</span>
          </span>
        </div>
        <button onClick={onClose} style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', fontSize: 18, lineHeight: 1 }}>×</button>
      </div>
      <svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ display: 'block' }}>
        {[0, 0.25, 0.5, 0.75, 1].map(t => { const y = padT + (1 - t) * (H - padT - padB); return (<g key={t}><line x1={padL} y1={y} x2={W - padR} y2={y} stroke="var(--border)" strokeWidth="0.5" /><text x={padL - 4} y={y + 3} fill="var(--text-muted)" fontSize="8" textAnchor="end">{(min + t * range).toFixed(0)}</text></g>); })}
        <polyline fill="none" stroke="var(--blue)" strokeWidth="1.5" strokeLinejoin="round" points={lineStr} />
        <line x1={ep.x} y1={padT} x2={ep.x} y2={H - padB} stroke="var(--green)" strokeWidth="1" strokeDasharray="4,3" opacity="0.6" />
        <line x1={xp.x} y1={padT} x2={xp.x} y2={H - padB} stroke={pos ? 'var(--green)' : 'var(--red)'} strokeWidth="1" strokeDasharray="4,3" opacity="0.6" />
        <polygon points={`${ep.x},${ep.y - 14} ${ep.x - 7},${ep.y - 2} ${ep.x + 7},${ep.y - 2}`} fill="var(--green)" />
        <text x={ep.x} y={ep.y - 17} fill="var(--green)" fontSize="8" textAnchor="middle">BUY</text>
        <polygon points={`${xp.x},${xp.y + 14} ${xp.x - 7},${xp.y + 2} ${xp.x + 7},${xp.y + 2}`} fill={pos ? 'var(--green)' : 'var(--red)'} />
        <text x={xp.x} y={xp.y + 26} fill={pos ? 'var(--green)' : 'var(--red)'} fontSize="8" textAnchor="middle">SELL</text>
      </svg>
      <div style={{ display: 'flex', gap: 20, fontSize: 10, color: 'var(--text-muted)', marginTop: 4, paddingLeft: padL }}>
        <span style={{ color: 'var(--green)' }}>▲ {fmtD(trade.entry_date)}</span>
        <span style={{ color: pos ? 'var(--green)' : 'var(--red)' }}>▼ {fmtD(trade.exit_date)}</span>
        <span>{trade.holding_bars}d held</span>
      </div>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  COMPOUND CAPITAL BANNER
// ═══════════════════════════════════════════════════════════
function CompoundBanner({ display }) {
  const curve = display?.equity_curve;
  if (!curve || curve.length < 2) return null;
  const startCap = display?.config?.capital || 100000;
  const finalCap = curve[curve.length - 1];
  const gain     = finalCap - startCap;
  const gainPct  = (gain / startCap) * 100;
  const totalCharges = display?.total_charges || 0;
  const grossPnl     = display?.total_gross_pnl ?? (display?.total_pnl || 0) + totalCharges;

  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 0,
      padding: '10px 16px', borderBottom: '1px solid var(--border)',
      background: 'rgba(255,255,255,0.02)', flexWrap: 'wrap', gap: 24,
    }}>
      {/* Compound capital arrow */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div>
          <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Starting Capital</div>
          <div className="td-mono" style={{ fontSize: 15, fontWeight: 700, color: 'var(--text-primary)' }}>₹{fmt(startCap, 0)}</div>
        </div>
        <div style={{ fontSize: 18, color: 'var(--text-muted)', padding: '0 4px' }}>→</div>
        <div>
          <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Compounded Capital</div>
          <div className={`td-mono ${pnlClass(gain)}`} style={{ fontSize: 15, fontWeight: 700 }}>₹{fmt(finalCap, 0)}</div>
        </div>
        <div style={{
          padding: '4px 10px', borderRadius: 6,
          background: gain >= 0 ? 'var(--green-dim)' : 'var(--red-dim)',
          color: gain >= 0 ? 'var(--green-text)' : 'var(--red-text)',
          fontSize: 13, fontWeight: 800, fontFamily: 'JetBrains Mono, monospace',
          marginLeft: 4,
        }}>
          {fmtSigned(gainPct, 2)}%
        </div>
      </div>

      {/* Charges breakdown */}
      <div>
        <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Gross P&L</div>
        <div className={`td-mono ${pnlClass(grossPnl)}`} style={{ fontSize: 14, fontWeight: 700 }}>{pnlStr(grossPnl)}</div>
      </div>
      <div>
        <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Zerodha Charges</div>
        <div className="td-mono pnl-neg" style={{ fontSize: 14, fontWeight: 700 }}>
          {totalCharges > 0 ? `−₹${fmt(totalCharges, 0)}` : '—'}
        </div>
      </div>
      <div>
        <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Net P&L (after charges)</div>
        <div className={`td-mono ${pnlClass(display?.total_pnl)}`} style={{ fontSize: 14, fontWeight: 700 }}>{pnlStr(display?.total_pnl)}</div>
      </div>
      <div>
        <div style={{ fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.07em', whiteSpace: 'nowrap' }}>Drag (charges / gross)</div>
        <div className="td-mono pnl-neg" style={{ fontSize: 14, fontWeight: 700 }}>
          {grossPnl > 0 && totalCharges > 0 ? (totalCharges / Math.abs(grossPnl) * 100).toFixed(1) + '%' : '—'}
        </div>
      </div>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  BACKTEST TAB
// ═══════════════════════════════════════════════════════════

// Defined outside TabBacktest so its identity is stable across renders.
// If defined inside, React sees a new component type every render and
// unmounts/remounts the entire subtree (including Select state) on every keystroke.
function ParamField({ label, k, tip, type = 'number', step = 0.01, width = 70, cfg, setCfg }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3, flexShrink: 0 }}>
      <label style={{ display: 'flex', alignItems: 'center', fontSize: 9, color: 'var(--text-muted)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.06em', whiteSpace: 'nowrap' }}>
        {label}{tip && <Tip id={tip} />}
      </label>
      {type === 'select' ? (
        <Select
          value={cfg[k]}
          onChange={v => setCfg(p => ({ ...p, [k]: v }))}
          options={SELECT_OPTIONS[k] || []}
        />
      ) : (
        <input type="number" step={step}
          value={cfg[k]}
          onChange={e => setCfg(p => ({ ...p, [k]: parseFloat(e.target.value) || 0 }))}
          style={{
            height: 28, fontSize: 11, padding: '0 6px', width,
            background: 'var(--bg-input)', color: 'var(--text-primary)',
            border: '1px solid var(--border)', borderRadius: 'var(--r-sm)',
            outline: 'none', fontFamily: 'JetBrains Mono, monospace',
          }}
        />
      )}
    </div>
  );
}

// Best params from backtest history (2yr, 139 trades):
//   SL 3–5% | Alloc 20% | MaxPos 10 → Return 27.9%, Sharpe 1.14, PF 1.41, MaxDD 17.8%
const DEFAULT_CFG = {
  strategy: 'ALL', start_bar_offset: 250, capital: 100000,
  sl_floor_pct: 3.0, sl_ceiling_pct: 5.0, max_trade_alloc_pct: 20.0,
  max_positions: 10, slippage_pct: 0.3,
};

function TabBacktest() {
  const [cfg, setCfg]               = useState(DEFAULT_CFG);
  const [result, setResult]         = useState(null);
  const [history, setHistory]       = useState([]);
  const [running, setRunning]       = useState(false);
  const [selectedRun, setSelectedRun] = useState(null);
  const [applying, setApplying]     = useState(false);
  const [applyMsg, setApplyMsg]     = useState('');
  const [selectedTrade, setSelectedTrade] = useState(null);
  const [sortKey, setSortKey]       = useState('entry_bar');
  const [sortDir, setSortDir]       = useState(1);
  const [logsOpen, setLogsOpen]     = useState(true);
  const [historyOpen, setHistoryOpen] = useState(true);

  useEffect(() => { fetch('/api/backtest/history').then(r => r.json()).then(d => setHistory(d.results || [])).catch(() => {}); }, []);

  const roundCfg = c => ({
    ...c,
    sl_floor_pct:        Math.round(c.sl_floor_pct        * 10000) / 10000,
    sl_ceiling_pct:      Math.round(c.sl_ceiling_pct      * 10000) / 10000,
    max_trade_alloc_pct: Math.round(c.max_trade_alloc_pct * 10000) / 10000,
    slippage_pct:        Math.round(c.slippage_pct        * 10000) / 10000,
  });

  const runBacktest = async () => {
    setRunning(true); setResult(null); setApplyMsg('');
    try {
      const res = await fetch('/api/backtest/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(roundCfg(cfg)) });
      if (!res.ok) { let t = await res.text(); setApplyMsg('❌ ' + ((!t || t.startsWith('<!') || t.length > 300) ? 'Engine offline — start the engine first.' : t)); return; }
      const data = await res.json();
      setResult(data); setSelectedRun(null);
      fetch('/api/backtest/history').then(r => r.json()).then(d => setHistory(d.results || [])).catch(() => {});
    } catch { setApplyMsg('❌ Engine offline — start the engine first.'); }
    finally { setRunning(false); }
  };

  const applyToConfig = async () => {
    setApplying(true); setApplyMsg('');
    const p = roundCfg(cfg);
    try {
      const res = await fetch('/api/config/apply', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p) });
      if (!res.ok) throw new Error(await res.text());
      setApplyMsg(`✅ Applied live — no restart needed. SL ${p.sl_floor_pct}–${p.sl_ceiling_pct}% | Alloc ${p.max_trade_alloc_pct}% | MaxPos ${p.max_positions || 'auto'}`);
    } catch (e) { setApplyMsg(`❌ ${e.message}`); }
    finally { setApplying(false); }
  };

  const toggleSort = key => { if (sortKey === key) setSortDir(d => -d); else { setSortKey(key); setSortDir(1); } };
  const SortTh = ({ col, label, right }) => (
    <th className={right ? 'td-right' : ''} onClick={() => toggleSort(col)} style={{ cursor: 'pointer', userSelect: 'none', whiteSpace: 'nowrap' }}>
      {label} <span style={{ opacity: sortKey === col ? 1 : 0.3, fontSize: 9 }}>{sortKey === col ? (sortDir > 0 ? '↑' : '↓') : '↕'}</span>
    </th>
  );
  const fmtDate = d => { try { return new Date(d).toLocaleDateString('en-IN', { day: 'numeric', month: 'short', year: '2-digit' }); } catch { return d || '—'; } };

  const display = result || selectedRun || null;
  const Divider = () => <div style={{ width: 1, height: 32, background: 'var(--border)', flexShrink: 0 }} />;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, gap: 10, minHeight: 0 }}>

      {/* ── PARAMS ROW (top, horizontal) ─────────────── */}
      <div className="card" style={{ flexShrink: 0 }}>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: 12, padding: '12px 14px', flexWrap: 'wrap' }}>
          <ParamField label="Strategy"     k="strategy"           tip="strategy"           type="select"            cfg={cfg} setCfg={setCfg} />
          <Divider />
          <ParamField label="Capital ₹"    k="capital"            tip="capital"            step={1000} width={90}  cfg={cfg} setCfg={setCfg} />
          <ParamField label="Bars"         k="start_bar_offset"   tip="start_bar_offset"   step={10}   width={58}  cfg={cfg} setCfg={setCfg} />
          <Divider />
          <ParamField label="SL Floor %"   k="sl_floor_pct"       tip="sl_floor_pct"       step={0.1}  width={55}  cfg={cfg} setCfg={setCfg} />
          <ParamField label="SL Ceiling %" k="sl_ceiling_pct"     tip="sl_ceiling_pct"     step={0.1}  width={55}  cfg={cfg} setCfg={setCfg} />
          <ParamField label="Alloc %"      k="max_trade_alloc_pct" tip="max_trade_alloc_pct" step={1}  width={52}  cfg={cfg} setCfg={setCfg} />
          <ParamField label="Max Pos"      k="max_positions"      tip="max_positions"      step={1}    width={50}  cfg={cfg} setCfg={setCfg} />
          <ParamField label="Slip %"       k="slippage_pct"       tip="slippage_pct"       step={0.05} width={55}  cfg={cfg} setCfg={setCfg} />
          <div style={{ flex: 1 }} />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, alignItems: 'flex-end', flexShrink: 0 }}>
            <div style={{ display: 'flex', gap: 6 }}>
              <button style={{ height: 28, padding: '0 10px', fontSize: 11, background: 'none', border: '1px solid var(--border)', borderRadius: 'var(--r-sm)', color: 'var(--text-muted)', cursor: 'pointer' }}
                onClick={() => { setCfg(DEFAULT_CFG); setApplyMsg(''); }}>Reset</button>
              <button onClick={runBacktest} disabled={running}
                style={{ height: 28, padding: '0 14px', fontSize: 11, fontWeight: 700, borderRadius: 'var(--r-sm)', border: 'none', cursor: running ? 'not-allowed' : 'pointer', minWidth: 110, display: 'flex', alignItems: 'center', gap: 6, justifyContent: 'center', background: 'var(--blue)', color: '#fff', opacity: running ? 0.7 : 1 }}>
                {running ? <><div className="spinner" style={{ width: 12, height: 12 }} />Running…</> : '▶ Run Backtest'}
              </button>
              {display && (
                <button onClick={applyToConfig} disabled={applying}
                  style={{ height: 28, padding: '0 14px', fontSize: 11, fontWeight: 700, borderRadius: 'var(--r-sm)', border: 'none', cursor: 'pointer', background: 'var(--green)', color: '#000' }}>
                  {applying ? '…' : '✓ Apply Config'}
                </button>
              )}
            </div>
            {applyMsg && (
              <div style={{ fontSize: 10, color: applyMsg.startsWith('✅') ? 'var(--green-text)' : 'var(--red-text)', lineHeight: 1.5, maxWidth: 320, textAlign: 'right' }}>
                {applyMsg}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── BODY ─────────────────────────────────────── */}
      <div style={{ display: 'flex', gap: 10, flex: 1, minHeight: 0, overflow: 'hidden' }}>

        {/* History panel (collapsible to icon strip) */}
        <div style={{ width: historyOpen ? 200 : 32, flexShrink: 0, transition: 'width 0.2s', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
          <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
            <div className="card-header" onClick={() => setHistoryOpen(o => !o)}
              style={{ cursor: 'pointer', userSelect: 'none', padding: historyOpen ? '14px 16px 12px' : '14px 6px 12px', justifyContent: historyOpen ? 'space-between' : 'center' }}>
              {historyOpen
                ? <><span className="card-title">History</span><span className="card-sub">{history.length} ◀</span></>
                : <Tooltip text="Run History" placement="right"><span style={{ fontSize: 14, color: 'var(--text-muted)' }}>📋</span></Tooltip>
              }
            </div>
            {historyOpen && (
              <div style={{ overflowY: 'auto', flex: 1 }}>
                {history.length === 0 ? <div className="empty-state" style={{ padding: '24px 16px' }}><div style={{ fontSize: 11, color: 'var(--text-muted)' }}>No runs yet</div></div>
                  : history.map((h, i) => (
                    <div key={i} className={`history-item ${selectedRun?.id === h.id && !result ? 'selected' : ''}`}
                      onClick={() => { setSelectedRun(h); setResult(null); }}>
                      <div>
                        <div style={{ fontSize: 11, fontWeight: 700 }}>{h.config?.strategy || 'ALL'} · {h.config?.start_bar_offset || '?'}b</div>
                        <div style={{ fontSize: 10, color: 'var(--text-muted)', marginTop: 1 }}>
                          {new Date(h.run_at).toLocaleDateString('en-IN', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })}
                        </div>
                      </div>
                      <div style={{ textAlign: 'right' }}>
                        <div className={`td-mono ${pnlClass(h.return_pct)}`} style={{ fontSize: 12, fontWeight: 800 }}>{fmtSigned(h.return_pct, 1)}%</div>
                        <div style={{ fontSize: 10, color: 'var(--text-muted)' }}>{h.win_rate?.toFixed(0)}% · {h.total_trades}T</div>
                      </div>
                    </div>
                  ))}
              </div>
            )}
          </div>
        </div>

        {/* Results */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0, overflowY: 'auto' }}>
          {!display ? (
            <div className="card" style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <div className="empty-state"><div className="empty-icon">🧪</div><div className="empty-title">Set parameters and run a backtest</div><div className="empty-sub">Results appear here.</div></div>
            </div>
          ) : (
            <>
              {/* KPI hero */}
              <div className="card">
                {/* Compound capital + charges banner */}
                <CompoundBanner display={display} />

                <div className="result-hero">
                  {[
                    { label: 'Net Return',     val: fmtSigned(display.return_pct, 2) + '%', cls: pnlClass(display.return_pct) },
                    { label: 'Win Rate',       val: (display.win_rate || 0).toFixed(1) + '%', cls: display.win_rate >= 50 ? 'pnl-pos' : 'pnl-neg' },
                    { label: 'Sharpe Ratio',   val: (display.sharpe_ratio || 0).toFixed(2), cls: display.sharpe_ratio >= 1 ? 'pnl-pos' : display.sharpe_ratio >= 0 ? '' : 'pnl-neg' },
                    { label: 'Max Drawdown',   val: (display.max_drawdown || 0).toFixed(1) + '%', cls: display.max_drawdown > 20 ? 'pnl-neg' : display.max_drawdown > 10 ? '' : 'pnl-pos' },
                    { label: 'Profit Factor',  val: display.profit_factor ? display.profit_factor.toFixed(2) : '—', cls: display.profit_factor >= 1.5 ? 'pnl-pos' : display.profit_factor >= 1 ? '' : 'pnl-neg' },
                    { label: 'Trades',         val: display.total_trades || 0 },
                    { label: 'Avg Hold (d)',   val: (display.avg_holding_bars || 0).toFixed(1) },
                    { label: 'Max Consec Loss',val: display.max_consec_losses || 0, cls: display.max_consec_losses > 4 ? 'pnl-neg' : '' },
                  ].map((kpi, i) => (
                    <div key={i} className="hero-item">
                      <div className="hero-label">{kpi.label}</div>
                      <div className={`hero-val ${kpi.cls || ''}`}>{kpi.val}</div>
                    </div>
                  ))}
                </div>

                {/* Equity curve */}
                <div style={{ padding: '10px 16px 10px' }}>
                  <div style={{ fontSize: 10, fontWeight: 700, color: 'var(--text-muted)', marginBottom: 6, textTransform: 'uppercase', letterSpacing: '0.07em' }}>Equity Curve (compounded, net of charges)</div>
                  <EquityChart data={display.equity_curve} height={80} />
                </div>
              </div>

              {/* Trade log (show/hide) */}
              {display.trades?.length > 0 && (
                <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
                  <div className="card-header" onClick={() => { setLogsOpen(o => !o); setSelectedTrade(null); }}
                    style={{ cursor: 'pointer', userSelect: 'none' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                      <span className="card-title">Simulated Trade Logs</span>
                      <span className="card-sub">{display.trades.length} trades · click row for chart</span>
                    </div>
                    <span style={{
                      fontSize: 11, fontWeight: 600, padding: '3px 10px',
                      borderRadius: 4, border: '1px solid var(--border)',
                      color: logsOpen ? 'var(--blue)' : 'var(--text-muted)',
                      background: 'var(--bg-hover)',
                    }}>
                      {logsOpen ? '▲ Hide' : '▼ Show Logs'}
                    </span>
                  </div>
                  {logsOpen && (
                    <>
                      {selectedTrade && <div style={{ padding: '0 12px' }}><TradeChart trade={selectedTrade} onClose={() => setSelectedTrade(null)} /></div>}
                      <div className="table-wrap" style={{ maxHeight: selectedTrade ? 220 : 400, overflowY: 'auto' }}>
                        <table>
                          <thead><tr>
                            <SortTh col="symbol"       label="Symbol" />
                            <SortTh col="strategy"     label="Strategy" />
                            <SortTh col="entry_date"   label="Entry" />
                            <SortTh col="exit_date"    label="Exit" />
                            <SortTh col="holding_bars" label="Hold" />
                            <SortTh col="entry_price"  label="Entry ₹" />
                            <SortTh col="exit_price"   label="Exit ₹" />
                            <SortTh col="sl"           label="SL ₹" />
                            <th>Reason</th>
                            <SortTh col="gross_pnl"    label="Gross ₹"   right />
                            <SortTh col="charges"      label="Charges ₹" right />
                            <SortTh col="net_pnl"      label="Net ₹"     right />
                            <SortTh col="pnl_pct"      label="Net %"     right />
                          </tr></thead>
                          <tbody>
                            {[...display.trades].sort((a, b) => {
                              const av = a[sortKey], bv = b[sortKey];
                              if (av == null) return 1; if (bv == null) return -1;
                              return sortDir * (av < bv ? -1 : av > bv ? 1 : 0);
                            }).map((t, i) => {
                              const isSelected = selectedTrade?.entry_bar === t.entry_bar && selectedTrade?.symbol === t.symbol;
                              const netPnl = t.net_pnl ?? t.gross_pnl;
                              return (
                                <tr key={i} onClick={() => setSelectedTrade(isSelected ? null : t)}
                                  style={{ cursor: 'pointer', background: isSelected ? 'var(--bg-hover)' : '' }}>
                                  <td className="symbol-chip">{t.symbol}</td>
                                  <td><span className="badge badge-strat">{t.strategy}</span></td>
                                  <td className="td-mono" style={{ fontSize: 11 }}>{fmtDate(t.entry_date)}</td>
                                  <td className="td-mono" style={{ fontSize: 11 }}>{fmtDate(t.exit_date)}</td>
                                  <td className="td-mono" style={{ color: 'var(--text-muted)' }}>{t.holding_bars}d</td>
                                  <td className="td-mono">₹{fmt(t.entry_price, 2)}</td>
                                  <td className="td-mono">₹{fmt(t.exit_price, 2)}</td>
                                  <td className="td-mono" style={{ color: 'var(--red-text)' }}>₹{fmt(t.sl, 2)}</td>
                                  <td><span className="badge badge-gray">{t.exit_reason}</span></td>
                                  <td className={`td-right td-mono ${pnlClass(t.gross_pnl)}`}>{pnlStr(t.gross_pnl)}</td>
                                  <td className="td-right td-mono" style={{ color: 'var(--text-muted)', fontSize: 11 }}>{t.charges > 0 ? '₹' + fmt(t.charges, 0) : '—'}</td>
                                  <td className={`td-right td-mono ${pnlClass(netPnl)}`} style={{ fontWeight: 700 }}>{pnlStr(netPnl)}</td>
                                  <td className={`td-right td-mono ${pnlClass(t.pnl_pct)}`}>{fmtSigned(t.pnl_pct, 2)}%</td>
                                </tr>
                              );
                            })}
                          </tbody>
                        </table>
                      </div>
                    </>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  SCANNER FEED
// ═══════════════════════════════════════════════════════════
function TabScanner({ logs }) {
  const visible = [...(logs || [])].reverse().slice(0, 60);
  return (
    <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      <div className="card-header">
        <span className="card-title" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <div style={{ width: 7, height: 7, borderRadius: '50%', background: 'var(--green)', boxShadow: '0 0 8px var(--green)' }} />
          Live Scanner Feed
        </span>
        <span className="card-sub">{visible.length} events</span>
      </div>
      {visible.length === 0 ? <div className="empty-state"><div className="empty-icon">📡</div><div className="empty-title">Awaiting signals…</div></div> : (
        <div className="table-wrap" style={{ flex: 1, overflowY: 'auto' }}>
          <table>
            <thead><tr><th>Time</th><th>Agent</th><th>Action</th><th>Details</th></tr></thead>
            <tbody>
              {visible.map((log, i) => (
                <tr key={i}>
                  <td className="td-mono" style={{ color: 'var(--text-muted)', fontSize: 11 }}>{log.time}</td>
                  <td><span className="badge badge-gray">{log.agent}</span></td>
                  <td style={{ fontWeight: 600 }}>{log.action}</td>
                  <td style={{ color: 'var(--text-secondary)' }}>{log.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ═══════════════════════════════════════════════════════════
//  MAIN APP
// ═══════════════════════════════════════════════════════════
export default function App() {
  const [tab, setTab]               = useState('dashboard');
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [clock, setClock]           = useState('');
  const [wsLive, setWsLive]         = useState(false);
  const [pnlSummary, setPnlSummary] = useState({ weekly: null, monthly: null });

  const [state, setState] = useState({
    pnl: 0, pnl_history: [], regime: 'UNKNOWN', uptime: '0h 0m 0s',
    positions: [], activity_log: [], universe_count: 0,
    index_data: {}, phases: {},
    daily_stats: { total: 0, wins: 0, losses: 0, win_rate: 0, gross_pnl: 0, avg_win: 0, avg_loss: 0, best_trade: 0, profit_factor: 0 },
  });

  useEffect(() => {
    const t = setInterval(() => setClock(new Date().toLocaleTimeString('en-IN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    const load = () => fetch('/api/pnl-summary').then(r => r.json()).then(d => setPnlSummary(d)).catch(() => {});
    load(); const t = setInterval(load, 60000); return () => clearInterval(t);
  }, []);

  useEffect(() => {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    let ws, timer;
    const connect = () => {
      ws = new WebSocket(`${proto}//${window.location.host}/api/ws/live`);
      ws.onopen = () => setWsLive(true);
      ws.onmessage = e => {
        try {
          const msg = JSON.parse(e.data);
          if (msg.type !== 'live_update') return;
          const health = msg.health || {}, root = msg.status || {}, logs = msg.logs || {};
          setWsLive(true);
          setState(prev => {
            const stats = root.stats || {};
            const pnl = stats.gross_pnl ?? 0;
            const hist = [...(prev.pnl_history || []).slice(-119), pnl];
            const nowIST = new Date(new Date().toLocaleString('en-US', { timeZone: 'Asia/Kolkata' }));
            const hhmm = nowIST.getHours() * 100 + nowIST.getMinutes();
            const isWeekend = nowIST.getDay() === 0 || nowIST.getDay() === 6;
            const isMarket = !isWeekend && hhmm >= 915 && hhmm <= 1530;
            const isPost   = !isWeekend && hhmm > 1530;
            let phases;
            if (isPost) {
              phases = { auto_login: 'pending', universe_load: 'pending', websocket: 'pending', cache_load: 'pending', regime: 'pending', scanner: 'pending', execution: 'pending', ema_exit: 'active', eod_scan: 'active' };
            } else if (!isMarket) {
              phases = { auto_login: 'pending', universe_load: 'pending', websocket: 'pending', cache_load: 'pending', regime: 'pending', scanner: 'pending', execution: 'pending', ema_exit: 'pending', eod_scan: 'pending' };
            } else {
              phases = {
                auto_login:    health.status ? 'active' : 'pending',
                universe_load: root.universe_count > 0 ? 'active' : 'pending',
                websocket:     health.ws_connected ? 'active' : 'pending',
                cache_load:    health.cache_loaded ? 'active' : 'running',
                regime:        (root.regime && root.regime !== 'UNKNOWN') ? 'active' : 'running',
                scanner:       root.engine_stopped ? 'error' : 'active',
                execution:     root.engine_stopped ? 'error' : 'active',
                ema_exit:      hhmm >= 1520 ? 'active' : 'pending',
                eod_scan:      hhmm >= 1545 ? 'active' : 'pending',
              };
            }
            return {
              ...prev, pnl, pnl_history: hist,
              regime: root.regime || 'UNKNOWN',
              uptime: health.uptime || '0h 0m 0s',
              positions: root.open_positions || [],
              universe_count: root.universe_count || 0,
              index_data: (() => {
                const raw = root.index_data || {};
                const n = raw['NIFTY_50'] || {}, b = raw['BANK_NIFTY'] || {}, v = raw['INDIA_VIX'] || {};
                return { nifty50: n.ltp || null, nifty50_pct: n.change ?? null, banknifty: b.ltp || null, banknifty_pct: b.change ?? null, vix: v.ltp || null, vix_pct: v.change ?? null };
              })(),
              activity_log: logs.logs || [],
              phases,
              daily_stats: { ...prev.daily_stats, ...stats },
            };
          });
        } catch (_) {}
      };
      ws.onclose = () => { setWsLive(false); timer = setTimeout(connect, 2500); };
      ws.onerror = () => ws.close();
    };
    connect();
    return () => { clearTimeout(timer); ws?.close(); };
  }, []);

  const ds = state.daily_stats;
  const w = pnlSummary.weekly || {}, m = pnlSummary.monthly || {};
  const regCol = regimeColor(state.regime);

  const PHASES = [
    { key: 'auto_login',    label: 'Auto Login',      icon: '🔑' },
    { key: 'universe_load', label: 'Universe (750)',   icon: '🌐' },
    { key: 'websocket',     label: 'WebSocket Feed',   icon: '📡' },
    { key: 'cache_load',    label: 'Cache (500d)',     icon: '📊' },
    { key: 'regime',        label: 'SMA200 Regime',    icon: '🎯' },
    { key: 'scanner',       label: 'Signal Scanner',   icon: '🔍' },
    { key: 'execution',     label: 'SL Monitor',       icon: '⚡' },
    { key: 'ema_exit',      label: 'EMA20 Exit',       icon: '📈' },
    { key: 'eod_scan',      label: 'EOD Scan',         icon: '📋' },
  ];

  const NAV = [
    { id: 'dashboard', icon: '📊', label: 'Dashboard' },
    { id: 'scanner',   icon: '🔍', label: 'Scanner Feed' },
    { id: 'history',   icon: '📋', label: 'Trade Journal' },
    { id: 'backtest',  icon: '🧪', label: 'Backtest' },
  ];

  // Aggregate pipeline status dot for collapsed sidebar
  const pipelineStatuses = PHASES.map(p => state.phases[p.key] || 'pending');
  const pipelineAgg = pipelineStatuses.includes('error') ? 'error' : pipelineStatuses.includes('running') ? 'running' : pipelineStatuses.some(s => s === 'active') ? 'active' : 'pending';

  return (
    <div className="layout">
      {/* ─── SIDEBAR ───────────────────────────────── */}
      <div className={`sidebar ${sidebarCollapsed ? 'collapsed' : ''}`}>
        {/* Brand */}
        <div className="sidebar-brand" style={sidebarCollapsed ? { padding: '14px 0', justifyContent: 'center' } : {}}>
          <div className="brand-logo">ZNT</div>
          {!sidebarCollapsed && (
            <div>
              <div className="brand-name">Zenith Engine</div>
              <div className="brand-sub">Swing v5.0 · NSE</div>
            </div>
          )}
        </div>

        {/* Nav */}
        <div className="sidebar-nav" style={sidebarCollapsed ? { padding: '8px 0', gap: 4 } : {}}>
          {NAV.map(n => (
            <Tooltip key={n.id} text={sidebarCollapsed ? n.label : ''} placement="right">
              <button
                className={`nav-item ${tab === n.id ? 'active' : ''}`}
                onClick={() => setTab(n.id)}
                style={sidebarCollapsed ? { justifyContent: 'center', padding: '8px 0', width: '100%' } : {}}
              >
                <span className="nav-icon" style={sidebarCollapsed ? { width: 34, height: 34, borderRadius: 8, fontSize: 17, background: tab === n.id ? 'var(--blue-dim)' : 'transparent' } : {}}>{n.icon}</span>
                {!sidebarCollapsed && n.label}
              </button>
            </Tooltip>
          ))}
        </div>

        {/* Pipeline */}
        {!sidebarCollapsed ? (
          <>
            <div className="sidebar-section">Pipeline</div>
            <div className="pipeline">
              {PHASES.map(p => {
                const status = state.phases[p.key] || 'pending';
                return (
                  <div className="pipeline-item" key={p.key}>
                    <div className={`pipe-dot ${status}`} />
                    <span className="pipe-label">{p.icon} {p.label}</span>
                    <span className={`pipe-status ${status === 'active' ? 'ok' : status === 'running' ? 'run' : status === 'error' ? 'err' : ''}`}>
                      {status === 'active' ? '✓' : status === 'running' ? '…' : status === 'error' ? '✗' : '·'}
                    </span>
                  </div>
                );
              })}
            </div>
          </>
        ) : (
          /* Collapsed pipeline — single aggregate dot with tooltip */
          <div style={{ display: 'flex', justifyContent: 'center', padding: '12px 0' }}>
            <Tooltip text={`Pipeline: ${pipelineStatuses.filter(s => s === 'active').length}/${PHASES.length} active`} placement="right">
              <div className={`pipe-dot ${pipelineAgg}`} style={{ width: 10, height: 10 }} />
            </Tooltip>
          </div>
        )}

        <div style={{ flex: 1 }} />

        {/* Footer */}
        <div className="sidebar-footer" style={sidebarCollapsed ? { justifyContent: 'center', padding: '10px 0' } : {}}>
          <Tooltip text={`${wsLive ? 'Connected' : 'Offline'} · ${state.uptime}`} placement="right">
            <div className={`ws-dot ${wsLive ? 'live' : ''}`} />
          </Tooltip>
          {!sidebarCollapsed && <div className="ws-text">{wsLive ? 'Connected' : 'Offline'} · {state.uptime}</div>}
        </div>

        {/* Collapse toggle */}
        <button className="sidebar-toggle" onClick={() => setSidebarCollapsed(o => !o)}
          title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
          {sidebarCollapsed ? '▶▶' : '◀◀'}
        </button>
      </div>

      {/* ─── MAIN AREA ─────────────────────────────── */}
      <div className="main-area">
        {/* Ticker bar */}
        <div className="ticker-bar">
          {[
            { label: 'NIFTY 50',   val: state.index_data?.nifty50,    pct: state.index_data?.nifty50_pct },
            { label: 'BANK NIFTY', val: state.index_data?.banknifty,  pct: state.index_data?.banknifty_pct },
            { label: 'INDIA VIX',  val: state.index_data?.vix,        pct: state.index_data?.vix_pct },
          ].map((idx, i) => (
            <div key={i} className="ticker-item">
              <span className="ticker-label">{idx.label}</span>
              <span className="ticker-val">{idx.val ? idx.val.toLocaleString('en-IN', { maximumFractionDigits: 2 }) : '—'}</span>
              {idx.pct != null && <span className={`ticker-chg ${idx.pct >= 0 ? 'up' : 'down'}`}>{idx.pct >= 0 ? '▲' : '▼'}{Math.abs(idx.pct).toFixed(2)}%</span>}
            </div>
          ))}
          <div className="ticker-spacer" />
          <div className="ticker-regime">
            <div className="regime-dot" style={{ background: regCol, boxShadow: `0 0 8px ${regCol}` }} />
            <span style={{ color: regCol, fontWeight: 800 }}>{state.regime}</span>
          </div>
          <div className="ticker-clock">{clock || '--:--:--'}</div>
          <div className={`ticker-ws ${wsLive ? 'pnl-pos' : 'pnl-neg'}`} style={{ fontSize: 11, fontWeight: 700 }}>
            <div style={{ width: 6, height: 6, borderRadius: '50%', background: 'currentColor', animation: wsLive ? 'pulse-dot 2s infinite' : 'none' }} />
            {wsLive ? 'LIVE' : 'OFFLINE'}
          </div>
        </div>

        {/* ─── DASHBOARD ──────────────────────────── */}
        {tab === 'dashboard' && (
          <div className="content-scroll">
            <div className="stat-row">
              <div className="stat-card">
                <div className="stat-label">Today P&L</div>
                <div className={`stat-value ${pnlClass(state.pnl)}`}>{state.pnl >= 0 ? '+' : ''}₹{Math.abs(state.pnl).toLocaleString('en-IN', { maximumFractionDigits: 0 })}</div>
                <div style={{ display: 'flex', gap: 8, marginTop: 6, flexWrap: 'wrap' }}>
                  <span className="stat-badge neutral">📊 {ds.total || 0} trades</span>
                  <span className="stat-badge up">✅ {ds.wins || 0}W</span>
                  <span className="stat-badge down">❌ {ds.losses || 0}L</span>
                  {ds.win_rate > 0 && <span className={`stat-badge ${ds.win_rate >= 50 ? 'up' : 'down'}`}>{(ds.win_rate || 0).toFixed(0)}% WR</span>}
                </div>
                <div style={{ marginTop: 10, height: 40 }}><EquityChart data={state.pnl_history} height={40} /></div>
              </div>

              <div className="stat-card">
                <div className="stat-label">This Week</div>
                <div className={`stat-value ${pnlClass(w.pnl)}`}>{(w.pnl || 0) >= 0 ? '+' : ''}₹{Math.abs(w.pnl || 0).toLocaleString('en-IN', { maximumFractionDigits: 0 })}</div>
                <div style={{ display: 'flex', gap: 8, marginTop: 6, flexWrap: 'wrap' }}>
                  <span className="stat-badge neutral">{w.trades || 0} trades</span>
                  {w.trades > 0 && <span className={`stat-badge ${(w.wins / w.trades * 100) >= 50 ? 'up' : 'down'}`}>{(w.wins / w.trades * 100).toFixed(0)}% WR</span>}
                </div>
                <div style={{ marginTop: 10 }}>
                  {(() => { const bd = w.breakdown || []; const cumul = [0]; let r = 0; bd.forEach(d => { r += d.pnl || 0; cumul.push(r); }); const days = ['M','T','W','T','F']; return (
                    <div style={{ display: 'flex', alignItems: 'flex-end', gap: 12 }}>
                      <div style={{ flex: 1, height: 36 }}><EquityChart data={cumul} height={36} /></div>
                      <div className="day-dots">{days.map((d, i) => { const e = bd[i]; const s = e ? (e.pnl > 0 ? 'win' : 'loss') : ''; return (<div key={i} className="day-dot"><div className={`day-dot-circle ${s}`} /><div className="day-dot-label">{d}</div></div>); })}</div>
                    </div>
                  ); })()}
                </div>
              </div>

              <div className="stat-card">
                <div className="stat-label">This Month</div>
                <div className={`stat-value ${pnlClass(m.pnl)}`}>{(m.pnl || 0) >= 0 ? '+' : ''}₹{Math.abs(m.pnl || 0).toLocaleString('en-IN', { maximumFractionDigits: 0 })}</div>
                <div style={{ display: 'flex', gap: 8, marginTop: 6, flexWrap: 'wrap' }}>
                  <span className="stat-badge neutral">{m.trades || 0} trades</span>
                  <span className="stat-badge up">₹{Math.abs(m.realized_pnl || 0).toLocaleString('en-IN', { maximumFractionDigits: 0 })} realized</span>
                </div>
                <div style={{ marginTop: 10, height: 36 }}>
                  {(() => { const bd = m.breakdown || []; const cumul = [0]; let r = 0; bd.forEach(d => { r += d.pnl || 0; cumul.push(r); }); return <EquityChart data={cumul} height={36} />; })()}
                </div>
              </div>

              <div className="stat-card">
                <div className="stat-label">Positions</div>
                <div className="stat-value">{state.positions.length}<span style={{ fontSize: 14, color: 'var(--text-muted)', fontWeight: 500 }}> / {Math.floor(100000 * 0.9 / 15000) || 6}</span></div>
                <div style={{ display: 'flex', gap: 8, marginTop: 6 }}><span className="stat-badge neutral">📡 {state.universe_count} symbols</span></div>
                <div style={{ marginTop: 10 }}>
                  {[{ label: 'Profit Factor', val: (ds.profit_factor || 0) > 0 ? (ds.profit_factor || 0).toFixed(2) : '—' }, { label: 'Best Trade', val: '₹' + fmt(ds.best_trade, 0) }].map(({ label, val }) => (
                    <div key={label} style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, padding: '3px 0', borderBottom: '1px solid var(--border)' }}>
                      <span style={{ color: 'var(--text-muted)' }}>{label}</span>
                      <span style={{ color: 'var(--text-primary)', fontWeight: 700 }} className="td-mono">{val}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>

            <div className="bottom-split">
              <div className="card" style={{ display: 'flex', flexDirection: 'column' }}>
                <div className="card-header">
                  <div><span className="card-title">Active Positions</span><span className="card-sub" style={{ marginLeft: 8 }}>{state.positions.length} open</span></div>
                  <span className="badge badge-gray">{state.universe_count} symbols</span>
                </div>
                <PositionsTable positions={state.positions} />
              </div>
              <div className="card" style={{ display: 'flex', flexDirection: 'column' }}>
                <div className="card-header">
                  <span className="card-title">Engine Activity</span>
                  <span className="card-sub">{(state.activity_log || []).length} events</span>
                </div>
                <div style={{ flex: 1, overflow: 'hidden' }}><ActivityFeed logs={state.activity_log} /></div>
              </div>
            </div>
          </div>
        )}

        {tab === 'scanner' && <div className="content-scroll"><TabScanner logs={state.activity_log} /></div>}
        {tab === 'history' && <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}><TabJournal /></div>}
        {tab === 'backtest' && <div className="content-scroll" style={{ flexDirection: 'column' }}><TabBacktest /></div>}
      </div>
    </div>
  );
}
