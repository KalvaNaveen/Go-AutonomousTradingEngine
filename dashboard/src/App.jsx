import { useState, useEffect, useRef } from 'react';
import './index.css';

// ════════════════════════════════════════════
//  SVG Mini Charts
// ════════════════════════════════════════════
function AreaChart({ data, width = 400, height = 140 }) {
  const clean = (data || []).filter(v => typeof v === 'number' && !isNaN(v));
  if (clean.length < 2) {
    return <svg width={width} height={height}><text x={width / 2} y={height / 2} textAnchor="middle" fill="#9CA3AF" fontSize="13">Collecting data...</text></svg>;
  }
  const min = Math.min(...clean);
  const max = Math.max(...clean);
  const range = (max - min) || 1;
  const pad = 4;
  const pts = clean.map((v, i) => {
    const x = pad + (i / (clean.length - 1)) * (width - pad * 2);
    const y = pad + (1 - (v - min) / range) * (height - pad * 2);
    return [x, y];
  });
  const line = pts.map(p => p.join(',')).join(' ');
  const area = `${pts.map(p => p.join(',')).join(' ')} ${width - pad},${height} ${pad},${height}`;
  const gradId = `agrad-${Math.random().toString(36).slice(2, 8)}`;

  const lastVal = clean[clean.length - 1];
  const isPositive = lastVal >= 0;
  const fillColor = isPositive ? '#2ECC71' : '#E8505B';

  return (
    <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      <defs>
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={fillColor} stopOpacity="0.2" />
          <stop offset="100%" stopColor={fillColor} stopOpacity="0" />
        </linearGradient>
      </defs>
      <polygon fill={`url(#${gradId})`} points={area} />
      <polyline fill="none" stroke={fillColor} strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" points={line} />
      {pts.length > 0 && (
        <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r="4" fill={fillColor} stroke="white" strokeWidth="2" />
      )}
    </svg>
  );
}

function DonutChart({ data, size = 120 }) {
  const total = data.reduce((s, d) => s + d.value, 0);
  if (total === 0) return null;
  const r = (size - 8) / 2;
  const cx = size / 2, cy = size / 2;
  const circ = 2 * Math.PI * r;
  let offset = 0;

  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
      {data.map((d, i) => {
        const pct = d.value / total;
        const dash = circ * pct;
        const gap = circ - dash;
        const o = offset;
        offset += dash;
        return (
          <circle key={i} cx={cx} cy={cy} r={r} fill="none" stroke={d.color}
            strokeWidth="8" strokeDasharray={`${dash} ${gap}`}
            strokeDashoffset={-o} strokeLinecap="round"
            transform={`rotate(-90 ${cx} ${cy})`} />
        );
      })}
      <text x={cx} y={cy - 6} textAnchor="middle" fontSize="18" fontWeight="700" fill="#1A1D1F">{total}</text>
      <text x={cx} y={cy + 12} textAnchor="middle" fontSize="11" fill="#9CA3AF">Trades</text>
    </svg>
  );
}

// ════════════════════════════════════════════
//  SUB-TAB COMPONENTS
// ════════════════════════════════════════════

function TabScanner({ logs }) {
  const scannerLogs = logs.filter(l => l.agent && l.agent.toLowerCase().includes('scanner')).reverse();
  const visibleLogs = scannerLogs.slice(0, 50);

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      {/* ── LOG FEED TABLE ── */}
      <div className="card animate-in" style={{ animationDelay: '0.05s', flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0, padding: 0 }}>
        <div className="card-header" style={{ padding: '20px 24px', borderBottom: '1px solid var(--border-light)', background: 'var(--bg-card)' }}>
          <h3 style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '18px' }}>
            <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: '#10B981', boxShadow: '0 0 10px #10B981' }}></div>
            Live Scanner Feed
          </h3>
          <span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>Latest {visibleLogs.length} pulses</span>
        </div>
        
        <div className="table-scroll" style={{ overflowY: 'auto' }}>
          {visibleLogs.length === 0 ? (
            <div style={{ padding: '64px', textAlign: 'center' }}>
              <div style={{ fontSize: '48px', marginBottom: '16px', opacity: 0.5 }}>📡</div>
              <div style={{ fontSize: '16px', fontWeight: 600, color: 'var(--text-secondary)' }}>Awaiting telemetry signals...</div>
            </div>
          ) : (
            <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
              <thead style={{ position: 'sticky', top: 0, background: 'var(--bg-card)', zIndex: 1, borderBottom: '1px solid var(--border-light)' }}>
                <tr>
                  <th style={{ padding: '12px 24px', fontSize: '11px', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 600 }}>Time</th>
                  <th style={{ padding: '12px 24px', fontSize: '11px', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 600 }}>Agent</th>
                  <th style={{ padding: '12px 24px', fontSize: '11px', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 600 }}>Action</th>
                  <th style={{ padding: '12px 24px', fontSize: '11px', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.05em', fontWeight: 600 }}>Details</th>
                </tr>
              </thead>
              <tbody>
                {visibleLogs.map((log, i) => {
                  const isCycle = log.detail && log.detail.includes('SCAN_CYCLE');
                  return (
                    <tr key={i} style={{ 
                      borderBottom: '1px solid var(--border-light)',
                      background: i % 2 !== 0 ? 'rgba(0,0,0,0.01)' : 'transparent',
                      transition: 'background 0.2s',
                    }} className="hover-row">
                      <td style={{ padding: '14px 24px', fontSize: '13px', color: 'var(--text-muted)', fontFamily: 'monospace' }}>{log.time}</td>
                      <td style={{ padding: '14px 24px' }}>
                        <span className="type-badge" style={{ 
                          background: isCycle ? 'rgba(48, 99, 245, 0.1)' : 'var(--bg-badge-blue)', 
                          color: isCycle ? 'var(--accent-blue)' : 'var(--text-primary)',
                          fontSize: '11px',
                          display: 'inline-block'
                        }}>
                          {log.agent}
                        </span>
                      </td>
                      <td style={{ padding: '14px 24px', fontSize: '13px', fontWeight: 600, color: 'var(--text-primary)' }}>{log.action}</td>
                      <td style={{ padding: '14px 24px', fontSize: '14px', color: isCycle ? 'var(--text-secondary)' : 'var(--text-primary)', fontWeight: isCycle ? 400 : 500, lineHeight: 1.4 }}>
                        {log.detail}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function TabJournal() {
  const [trades, setTrades] = useState([]);
  const [loading, setLoading] = useState(true);
  const [availableDates, setAvailableDates] = useState([]);
  const [selectedDate, setSelectedDate] = useState(new Date().toISOString().split('T')[0]);
  const [summary, setSummary] = useState(null);

  // Load available trading dates on mount
  useEffect(() => {
    fetch('/api/trades/dates')
      .then(res => res.json())
      .then(data => { setAvailableDates(data.dates || []); })
      .catch(() => {});
  }, []);

  // Load trades whenever selectedDate changes
  useEffect(() => {
    setLoading(true);
    Promise.all([
      fetch(`/api/trades?date=${selectedDate}`).then(r => r.json()),
      fetch(`/api/summary?date=${selectedDate}`).then(r => r.json()),
    ]).then(([tradeData, sumData]) => {
      setTrades(tradeData.trades || []);
      setSummary(sumData.summary || null);
      setLoading(false);
    }).catch(() => setLoading(false));
  }, [selectedDate]);

  const totalPnl = trades.reduce((s, t) => s + parseFloat(t.gross_pnl || 0), 0);
  const wins = trades.filter(t => parseFloat(t.gross_pnl || 0) > 0).length;
  const losses = trades.filter(t => parseFloat(t.gross_pnl || 0) <= 0).length;
  const isToday = selectedDate === new Date().toISOString().split('T')[0];

  const navigateDate = (dir) => {
    const idx = availableDates.indexOf(selectedDate);
    if (dir === 'prev' && idx < availableDates.length - 1) setSelectedDate(availableDates[idx + 1]);
    if (dir === 'next' && idx > 0) setSelectedDate(availableDates[idx - 1]);
  };

  const formatDateDisplay = (d) => {
    const dt = new Date(d + 'T00:00:00');
    return dt.toLocaleDateString('en-IN', { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', flex: 1, minHeight: 0 }}>

      {/* Date Navigation Bar */}
      <div className="card" style={{ padding: '16px 24px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexShrink: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          <button onClick={() => navigateDate('prev')}
            style={{ background: 'var(--bg-hover)', border: '1px solid var(--border-light)', borderRadius: '8px', padding: '6px 12px', cursor: 'pointer', fontSize: '14px', color: 'var(--text-primary)' }}>
            ◀
          </button>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <input type="date" value={selectedDate} onChange={e => setSelectedDate(e.target.value)}
              style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-light)', borderRadius: '8px', padding: '8px 14px', fontSize: '14px', fontWeight: 600, color: 'var(--text-primary)', cursor: 'pointer' }} />
            <span style={{ fontSize: '13px', color: 'var(--text-muted)' }}>{formatDateDisplay(selectedDate)}</span>
            {isToday && <span className="type-badge" style={{ background: 'var(--bg-badge-green)', color: '#059669', fontSize: '10px' }}>TODAY</span>}
          </div>
          <button onClick={() => navigateDate('next')} disabled={isToday}
            style={{ background: isToday ? 'var(--bg-secondary)' : 'var(--bg-hover)', border: '1px solid var(--border-light)', borderRadius: '8px', padding: '6px 12px', cursor: isToday ? 'not-allowed' : 'pointer', fontSize: '14px', color: isToday ? 'var(--text-muted)' : 'var(--text-primary)', opacity: isToday ? 0.5 : 1 }}>
            ▶
          </button>
          <button onClick={() => setSelectedDate(new Date().toISOString().split('T')[0])}
            style={{ background: 'var(--bg-hover)', border: '1px solid var(--border-light)', borderRadius: '8px', padding: '6px 12px', cursor: 'pointer', fontSize: '12px', fontWeight: 600, color: 'var(--accent-blue)' }}>
            Today
          </button>
        </div>

        {/* Quick date chips */}
        <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
          {availableDates.slice(0, 7).map(d => (
            <button key={d} onClick={() => setSelectedDate(d)}
              style={{
                background: d === selectedDate ? 'var(--accent-blue)' : 'var(--bg-hover)',
                color: d === selectedDate ? '#fff' : 'var(--text-secondary)',
                border: 'none', borderRadius: '6px', padding: '4px 10px', fontSize: '11px', fontWeight: 600, cursor: 'pointer',
                transition: 'all 0.15s ease'
              }}>
              {new Date(d + 'T00:00:00').toLocaleDateString('en-IN', { day: 'numeric', month: 'short' })}
            </button>
          ))}
        </div>
      </div>

      {/* Summary Strip */}
      {trades.length > 0 && (
        <div className="stat-strip" style={{ flexShrink: 0 }}>
          {[
            { icon: '📊', label: 'Trades', value: trades.length },
            { icon: '✅', label: 'Wins', value: wins, color: '#059669' },
            { icon: '❌', label: 'Losses', value: losses, color: 'var(--accent-red)' },
            { icon: '📈', label: 'Win Rate', value: `${trades.length > 0 ? ((wins / trades.length) * 100).toFixed(0) : 0}%` },
            { icon: '💰', label: 'Day P&L', value: `${totalPnl >= 0 ? '+' : ''}₹${Math.abs(totalPnl).toFixed(0)}`, color: totalPnl >= 0 ? '#059669' : 'var(--accent-red)' },
          ].map((s, i) => (
            <div key={i} className="stat-pill">
              <span className="stat-pill-icon">{s.icon}</span>
              <span className="stat-pill-value" style={s.color ? { color: s.color } : {}}>{s.value}</span>
              <span className="stat-pill-label">{s.label}</span>
            </div>
          ))}
        </div>
      )}

      {/* Trades Table */}
      <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        <div className="card-header" style={{ paddingBottom: '16px' }}>
          <h3>Trade Journal</h3>
          <span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>{trades.length} trades on {formatDateDisplay(selectedDate)}</span>
        </div>
        <div className="table-scroll" style={{ overflowY: 'auto' }}>
          {loading ? <div style={{ padding: '24px', textAlign: 'center' }}>Loading...</div> : trades.length === 0 ? (
            <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-muted)' }}>
              <div style={{ fontSize: '48px', marginBottom: '12px' }}>📭</div>
              <div style={{ fontWeight: 600, marginBottom: '4px' }}>No trades recorded</div>
              <div style={{ fontSize: '13px' }}>No trading activity found for {formatDateDisplay(selectedDate)}</div>
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Entry Time</th><th>Symbol</th><th>Strategy</th><th>Qty</th>
                  <th>Avg Entry</th><th>Avg Exit</th><th>Reason</th><th style={{ textAlign: 'right' }}>Gross P&L</th>
                </tr>
              </thead>
              <tbody>
                {[...trades].reverse().map((t, i) => {
                  const pnl = parseFloat(t.gross_pnl || 0);
                  return (
                    <tr key={i}>
                      <td style={{ color: 'var(--text-muted)' }}>{t.entry_time?.substring(11, 19)}</td>
                      <td className="symbol-cell">{t.symbol}</td>
                      <td>{t.strategy}</td>
                      <td>{t.qty}</td>
                      <td>₹{t.entry_price?.toFixed(2)}</td>
                      <td>₹{t.full_exit_price?.toFixed(2)}</td>
                      <td>
                        <span className="type-badge" style={{ background: '#F3F4F6', color: 'var(--text-secondary)' }}>
                          {t.exit_reason}
                        </span>
                      </td>
                      <td style={{ textAlign: 'right', fontWeight: 600 }} className={pnl >= 0 ? 'pnl-positive' : 'pnl-negative'}>
                        {pnl >= 0 ? '+' : ''}₹{Math.abs(pnl).toFixed(0)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function NewsFeedWidget({ news }) {
  return (
    <div className="card" style={{ flex: 1, height: '100%', display: 'flex', flexDirection: 'column', minHeight: 0 }}>
      <div className="card-header" style={{ paddingBottom: '12px' }}>
        <h3>Macro News Feed</h3>
        <span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>Live impact</span>
      </div>
      <div className="table-scroll" style={{ flex: 1, padding: '0 24px 24px', display: 'flex', flexDirection: 'column', gap: '12px' }}>
        {!news || news.length === 0 ? (
          <div style={{ textAlign: 'center', color: 'var(--text-muted)', fontSize: '12px', marginTop: '20px' }}>No news updates</div>
        ) : (
          news.map((n, i) => {
            const isBull = n.sentiment === 'bullish';
            const isBear = n.sentiment === 'bearish';
            const hasImage = !!n.image;
            return (
              <div key={i} style={{
                borderBottom: hasImage ? 'none' : '1px solid var(--border-light)',
                padding: hasImage ? '16px' : '0 0 12px 0',
                borderRadius: hasImage ? '12px' : '0',
                display: 'flex', flexDirection: 'column', gap: '6px',
                backgroundImage: hasImage ? `linear-gradient(to bottom, rgba(0,0,0,0.2), rgba(0,0,0,0.85)), url(${n.image})` : 'none',
                backgroundSize: 'cover',
                backgroundPosition: 'center',
                boxShadow: hasImage ? '0 4px 6px -1px rgba(0, 0, 0, 0.1)' : 'none',
                marginBottom: hasImage ? '4px' : '0'
              }}>
                <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                  <span style={{ fontSize: '10px', fontWeight: 800, color: hasImage ? '#E5E7EB' : 'var(--accent-purple)' }}>{n.source}</span>
                  <span style={{ fontSize: '10px', fontWeight: 600, color: hasImage ? '#D1D5DB' : 'var(--text-muted)' }}>{n.time?.substring(0, 5)}</span>
                </div>
                <p style={{ fontSize: '13px', margin: 0, fontWeight: hasImage ? 600 : 500, color: hasImage ? '#fff' : 'var(--text-primary)', lineHeight: 1.4 }}>{n.title}</p>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: '6px' }}>
                  <span className="type-badge" style={{
                    fontSize: '10px', padding: '2px 8px', fontWeight: 700, letterSpacing: '0.02em',
                    background: isBull ? (hasImage ? 'rgba(16,185,129,0.25)' : 'var(--bg-badge-green)') :
                      isBear ? (hasImage ? 'rgba(239,68,68,0.25)' : 'var(--bg-badge-red)') :
                        (hasImage ? 'rgba(255,255,255,0.15)' : '#F3F4F6'),
                    color: hasImage ? (isBull ? '#34D399' : isBear ? '#F87171' : '#F3F4F6') :
                      (isBull ? '#059669' : isBear ? 'var(--accent-red)' : 'var(--text-secondary)'),
                    border: hasImage ? `1px solid ${isBull ? 'rgba(16,185,129,0.5)' : isBear ? 'rgba(239,68,68,0.5)' : 'rgba(255,255,255,0.3)'}` : 'none'
                  }}>
                    {isBull ? 'BULLISH' : isBear ? 'BEARISH' : 'NEUTRAL'}
                  </span>
                  <span style={{ fontSize: '10px', fontWeight: 600, color: hasImage ? '#9CA3AF' : 'var(--text-muted)' }}>{n.symbol || ''}</span>
                </div>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

const AVAILABLE_STRATEGIES = [
  "S1_MA_CROSS", "S2_BB_MEAN_REV", "S3_ORB", "S6_TREND_SHORT", 
  "S6_VWAP_BAND", "S7_MEAN_REV_LONG", "S8_VOL_PIVOT", "S9_MTF_MOMENTUM", 
  "S10_GAP_FILL", "S11_VWAP_REVERT", "S12_EOD_REVERT", "S13_SECTOR_ROT", 
  "S14_RSI_SCALP", "S15_RSI_SWING"
];

function TabSimulator() {
  const [days, setDays] = useState(30);
  const [top, setTop] = useState(50);
  const [running, setRunning] = useState(false);
  const [logs, setLogs] = useState([]);
  const [selectedStrategies, setSelectedStrategies] = useState(AVAILABLE_STRATEGIES);

  const toggleStrategy = (strat) => {
    if (selectedStrategies.includes(strat)) {
      setSelectedStrategies(selectedStrategies.filter(s => s !== strat));
    } else {
      setSelectedStrategies([...selectedStrategies, strat]);
    }
  };

  const runSimulator = () => {
    setRunning(true);
    setLogs(["[SIM] Initializing Native Go Backtester..."]);
    const wsUrl = `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/ws/simulator?days=${days}&top=${top}&strategies=${selectedStrategies.join(',')}`;

    const ws = new WebSocket(wsUrl);
    ws.onopen = () => setLogs(prev => [...prev, "[SIM] Connected to Engine Simulator Core."]);
    ws.onmessage = (e) => setLogs(prev => [...prev, e.data]);
    ws.onclose = () => {
      setRunning(false);
      setLogs(prev => [...prev, "[SIM] Connection closed."]);
    };
    ws.onerror = () => {
      setLogs(prev => [...prev, "[ERROR] Connection to simulator failed. Backend route /api/ws/simulator not implemented yet."]);
    };
  };

  return (
    <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column', padding: '24px', minHeight: 0 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '24px' }}>
        <div>
          <h3 style={{ fontSize: '18px', fontWeight: 600 }}>Quantum Simulator Options</h3>
          <p style={{ fontSize: '13px', color: 'var(--text-muted)' }}>Replay historical intraday ticks through core strategy paths.</p>
        </div>
        <div style={{ display: 'flex', gap: '16px', alignItems: 'flex-end' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <label style={{ fontSize: '11px', fontWeight: 600, color: 'var(--text-secondary)' }}>LOOKBACK DAYS</label>
            <input type="number" value={days} onChange={e => setDays(Number(e.target.value))} style={{ padding: '8px 12px', border: '1px solid var(--border-light)', borderRadius: '8px', width: '100px' }} />
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <label style={{ fontSize: '11px', fontWeight: 600, color: 'var(--text-secondary)' }}>TOP SYMBOLS (VOL)</label>
            <input type="number" value={top} onChange={e => setTop(Number(e.target.value))} style={{ padding: '8px 12px', border: '1px solid var(--border-light)', borderRadius: '8px', width: '100px' }} />
          </div>
          <button onClick={runSimulator} disabled={running} style={{ padding: '10px 24px', background: 'var(--accent-blue)', color: 'white', border: 'none', borderRadius: '8px', fontWeight: 600, cursor: running ? 'not-allowed' : 'pointer' }}>
            {running ? 'Simulating...' : 'Run Simulation'}
          </button>
        </div>
      </div>

      <div style={{ marginBottom: '24px' }}>
        <h4 style={{ fontSize: '13px', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '12px' }}>FILTER STRATEGIES</h4>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px' }}>
          {AVAILABLE_STRATEGIES.map(strat => {
            const isActive = selectedStrategies.includes(strat);
            return (
              <button
                key={strat}
                onClick={() => toggleStrategy(strat)}
                style={{
                  padding: '6px 12px',
                  borderRadius: '16px',
                  fontSize: '12px',
                  fontWeight: 600,
                  cursor: 'pointer',
                  border: isActive ? '1px solid var(--accent-blue)' : '1px solid var(--border-light)',
                  background: isActive ? 'var(--accent-blue)' : 'transparent',
                  color: isActive ? 'white' : 'var(--text-secondary)',
                  transition: 'all 0.2s ease'
                }}
              >
                {strat}
              </button>
            );
          })}
        </div>
      </div>

      <div style={{ flex: 1, background: '#111827', borderRadius: '12px', padding: '16px', overflowY: 'auto', color: '#10B981', fontFamily: 'monospace', fontSize: '13px', lineHeight: 1.6 }}>
        {logs.length === 0 ? <div style={{ color: '#6B7280' }}>Ready to initialize Engine. Waiting for backtest parameters...</div> : null}
        {logs.map((L, i) => <div key={i}>{L}</div>)}
      </div>
    </div>
  );
}

// ════════════════════════════════════════════
//  MAIN APP
// ════════════════════════════════════════════
function App() {
  const [activeTab, setActiveTab] = useState('dashboard');
  const [clock, setClock] = useState('');
  const [wsConnected, setWsConnected] = useState(false);

  // Global state binding from Core Engine
  const [state, setState] = useState({
    pnl: 0, pnl_history: [], regime: 'UNKNOWN', uptime: '0h 0m 0s',
    positions: [], activity_log: [],
    universe_count: 0,
    index_data: { nifty50: null, banknifty: null, vix: null },
    news_feed: [], ml_stats: { trained: false, accuracy: 0, samples: 0 },
    phases: {
      auto_login: 'pending', cache_load: 'pending', universe_load: 'pending',
      websocket: 'pending', scanner: 'pending', execution: 'pending',
      risk: 'pending', eod_squareoff: 'pending'
    },
    daily_stats: { total: 0, wins: 0, losses: 0, win_rate: 0, gross_pnl: 0, avg_win: 0, avg_loss: 0, best_trade: 0, profit_factor: 0 },
    strategy_breakdown: {}
  });

  // Global log fetching for Scanner Tab fallback
  const [globalLogs, setGlobalLogs] = useState([]);
  const [pnlSummary, setPnlSummary] = useState({ weekly: null, monthly: null });

  useEffect(() => {
    if (activeTab === 'scanner') {
      fetch('/api/logs')
        .then(res => res.json())
        .then(data => setGlobalLogs(data.logs || []))
        .catch(() => { });
    }
  }, [activeTab]);

  // Fetch weekly/monthly P&L summary on mount and every 60s
  useEffect(() => {
    const load = () => {
      fetch('/api/pnl-summary')
        .then(r => r.json())
        .then(data => setPnlSummary(data))
        .catch(() => {});
    };
    load();
    const interval = setInterval(load, 60000);
    return () => clearInterval(interval);
  }, []);

  // Clock
  useEffect(() => {
    const t = setInterval(() => {
      setClock(new Date().toLocaleTimeString('en-IN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' }));
    }, 1000);
    return () => clearInterval(t);
  }, []);

  // WebSocket Live Core Engine binding
  useEffect(() => {
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.host}/api/ws/live`;
    let ws = null, reconnect = null;

    const connect = () => {
      ws = new WebSocket(wsUrl);
      ws.onopen = () => setWsConnected(true);
      ws.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data);
          if (data.type === 'live_update') {
            const health = data.health || {};
            const root = data.status || {};
            const logs = data.logs || {};
            setWsConnected(true);
            setState(prev => {
              const stats = root.stats || {};
              const pnl = stats.gross_pnl != null ? stats.gross_pnl : (root.daily_pnl || 0);
              const hist = [...(prev.pnl_history || []).slice(-59), pnl];

              // Check if we're inside market hours (09:15 - 15:30 IST)
              const nowIST = new Date(new Date().toLocaleString('en-US', { timeZone: 'Asia/Kolkata' }));
              const hhmm = nowIST.getHours() * 100 + nowIST.getMinutes();
              const isWeekend = nowIST.getDay() === 0 || nowIST.getDay() === 6;
              const isMarketHours = !isWeekend && hhmm >= 915 && hhmm <= 1530;
              const isPostMarket = !isWeekend && hhmm > 1530;
              const isPreMarket = !isWeekend && hhmm < 830;

              let phases;
              if (isPostMarket) {
                // After market close: everything grey, only EOD green
                phases = {
                  auto_login: 'pending', cache_load: 'pending', universe_load: 'pending',
                  websocket: 'pending', scanner: 'pending', execution: 'pending',
                  risk: 'pending', eod_squareoff: 'active'
                };
              } else if (isPreMarket || isWeekend) {
                // Before engine boot or non-trading days: all grey (pending)
                phases = {
                  auto_login: 'pending', cache_load: 'pending', universe_load: 'pending',
                  websocket: 'pending', scanner: 'pending', execution: 'pending',
                  risk: 'pending', eod_squareoff: 'pending'
                };
              } else {
                // During market: derive from live engine state
                phases = {
                  auto_login: health.status ? 'active' : 'pending',
                  cache_load: health.cache_loaded ? 'active' : (health.status === 'running' ? 'running' : 'pending'),
                  universe_load: root.universe_count > 0 ? 'active' : 'pending',
                  websocket: health.ws_connected ? 'active' : 'pending',
                  scanner: root.engine_stopped ? 'error' : (health.status === 'running' ? 'active' : 'pending'),
                  execution: root.engine_stopped ? 'error' : (health.status === 'running' ? 'active' : 'pending'),
                  risk: root.engine_stopped ? 'error' : (health.status === 'running' ? 'active' : 'pending'),
                  eod_squareoff: 'pending'
                };
              }

              const stratBreak = { ...(prev.strategy_breakdown || {}) };
              (root.open_positions || []).forEach(p => {
                const s = p.strategy || 'UNKNOWN';
                if (!stratBreak[s]) stratBreak[s] = 0;
              });

              return {
                ...prev, pnl, pnl_history: hist, regime: root.regime || 'UNKNOWN',
                uptime: health.uptime || '0h 0m 0s',
                positions: root.open_positions || [],
                universe_count: root.universe_count || 0,
                index_data: root.index_data || prev.index_data,
                news_feed: root.news_feed || prev.news_feed || [],
                activity_log: logs.logs || [],
                ml_stats: root.ml_stats || prev.ml_stats,
                phases, strategy_breakdown: stratBreak,
                daily_stats: {
                  ...prev.daily_stats,
                  ...stats
                }
              };
            });
          }
        } catch (err) { }
      };
      ws.onclose = () => { setWsConnected(false); reconnect = setTimeout(connect, 2000); };
      ws.onerror = () => ws.close();
    };
    connect();
    return () => { if (reconnect) clearTimeout(reconnect); if (ws) ws.close(); };
  }, []);

  const pnlIsPositive = state.pnl >= 0;
  const ds = state.daily_stats;

  const stratColors = ['#3063F5', '#2ECC71', '#E8505B', '#F59E0B', '#8B5CF6', '#06B6D4', '#EC4899', '#10B981'];
  const donutData = Object.entries(state.strategy_breakdown).map(([name, count], i) => ({
    name, value: count || 1, color: stratColors[i % stratColors.length]
  }));
  if (donutData.length === 0 && state.positions.length > 0) {
    const counts = {};
    state.positions.forEach(p => { counts[p.strategy] = (counts[p.strategy] || 0) + 1; });
    Object.entries(counts).forEach(([name, count], i) => {
      donutData.push({ name, value: count, color: stratColors[i % stratColors.length] });
    });
  }

  const getActivityType = (agent) => {
    if (!agent) return 'system';
    const a = agent.toLowerCase();
    if (a.includes('scan')) return 'scan';
    if (a.includes('exec')) return 'trade';
    if (a.includes('risk')) return 'risk';
    if (a.includes('login') || a.includes('auth')) return 'login';
    return 'system';
  };
  const getActivityIcon = (type) => ({ scan: '🔍', trade: '💰', risk: '🛡️', login: '🔑', system: '⚙️' }[type] || '⚙️');

  const profitTarget = 5000;
  const lossLimit = -3000;
  const profitPct = Math.min(Math.max((state.pnl / profitTarget) * 100, 0), 100);
  const lossPct = Math.min(Math.max((Math.abs(state.pnl) / Math.abs(lossLimit)) * 100, 0), 100);

  return (
    <>
      {/* ─── SIDEBAR ────────────────────────── */}
      <div className="sidebar">
        <div className="sidebar-brand">
          <div className="brand-icon">₿NF</div>
          <div>
            <h1>BNF Engine</h1>
            <span>Quantum Terminal v2.0</span>
          </div>
        </div>

        <div className="sidebar-section">
          <div className="sidebar-section-label">Navigation</div>
        </div>
        <nav className="sidebar-nav">
          {[
            { id: 'dashboard', icon: '📊', label: 'Dashboard' },
            { id: 'scanner', icon: '🔍', label: 'Scanner Feed' },
            { id: 'history', icon: '📋', label: 'Trade Journal' },

            { id: 'simulator', icon: '🧪', label: 'Quantum Simulator' },
          ].map(item => (
            <button key={item.id} className={`nav-item ${activeTab === item.id ? 'active' : ''}`} onClick={() => setActiveTab(item.id)}>
              <span className="nav-icon">{item.icon}</span>{item.label}
            </button>
          ))}
        </nav>



        {/* Engine Phases Visualization */}
        <div className="engine-phases">
          <div className="sidebar-section-label" style={{ padding: '0 8px 12px' }}>Engine Pipeline</div>
          {[
            { key: 'auto_login', label: 'Auto Login', icon: '🔑', time: '08:30' },
            { key: 'cache_load', label: 'Cache Preload', icon: '📊', time: '08:45' },
            { key: 'universe_load', label: 'Universe Load', icon: '🌐', time: '08:50' },
            { key: 'websocket', label: 'WebSocket Feed', icon: '📡', time: '09:00' },
            { key: 'scanner', label: 'Scanner Active', icon: '🔍', time: '09:16' },
            { key: 'execution', label: 'Execution Ready', icon: '⚡', time: '09:16' },
            { key: 'risk', label: 'Risk Checks', icon: '🛡️', time: 'Always' },
            { key: 'eod_squareoff', label: 'EOD Squareoff', icon: '🏁', time: '15:15' },
          ].map(phase => (
            <div className="phase-item" key={phase.key}>
              <div className={`phase-dot ${state.phases[phase.key] || 'pending'}`}></div>
              <span className="phase-label">{phase.icon} {phase.label}</span>
              <span className="phase-time">{phase.time}</span>
            </div>
          ))}
        </div>

        <div className="sidebar-footer">
          <div className={`footer-dot ${wsConnected ? '' : 'offline'}`}></div>
          <span style={{ fontSize: '12px', fontWeight: 500, color: 'var(--text-secondary)' }}>
            {wsConnected ? 'Connected' : 'Disconnected'} • {state.uptime}
          </span>
        </div>
      </div>

      {/* ─── MAIN AREA ──────────────────────── */}
      <div className="main-area">
        {/* Universal Header */}
        <div className="header-bar animate-in">
          <div style={{ display: 'flex', alignItems: 'center', gap: '32px' }}>
            <h2>
              {activeTab === 'dashboard' ? '' :
                activeTab === 'scanner' ? 'Live Scanner' :
                  activeTab === 'history' ? 'Trade Journal' :
                    activeTab === 'simulator' ? 'Historical Strategy Simulator' : 'Trade Analysis'}
            </h2>

            {/* Market Indices in Header */}
            <div style={{ display: 'flex', gap: '12px' }}>
              {[
                { label: 'NIFTY 50', val: state.index_data?.nifty50, pct: state.index_data?.nifty50_pct },
                { label: 'BANK NIFTY', val: state.index_data?.banknifty, pct: state.index_data?.banknifty_pct },
                { label: 'INDIA VIX', val: state.index_data?.vix, pct: state.index_data?.vix_pct },
              ].map((idx, i) => (
                <div key={i} className="card" style={{ display: 'flex', alignItems: 'center', gap: '8px', padding: '6px 12px', borderRadius: '100px', margin: 0, boxShadow: 'none', border: '1px solid var(--border-light)' }}>
                  <span style={{ fontSize: '10px', fontWeight: 700, color: 'var(--text-muted)', letterSpacing: '0.02em' }}>{idx.label}</span>
                  <span style={{ width: '5em', fontSize: '13px', fontWeight: 800, color: 'var(--text-primary)', fontVariantNumeric: 'tabular-nums' }}>
                    {idx.val ? idx.val.toLocaleString('en-IN') : '—'}
                  </span>
                  {idx.pct != null && (
                    <span className="type-badge" style={{
                      fontSize: '10px', padding: '2px 4px',
                      background: idx.pct >= 0 ? 'var(--bg-badge-green)' : 'var(--bg-badge-red)',
                      color: idx.pct >= 0 ? '#059669' : 'var(--accent-red)'
                    }}>
                      {idx.pct >= 0 ? '▲' : '▼'}{Math.abs(idx.pct).toFixed(2)}%
                    </span>
                  )}
                </div>
              ))}
            </div>
          </div>

          <div className="header-right">
            <div className="header-regime">
              <div className="regime-dot" style={{ background: state.regime === 'BULL' ? 'var(--accent-green)' : state.regime === 'BEAR' ? 'var(--accent-red)' : state.regime === 'VOLATILE' ? 'var(--accent-orange)' : 'var(--accent-blue)' }}></div>
              {state.regime || 'UNKNOWN'}
            </div>
            <div className="header-clock">{clock || '--:--:--'}</div>
            <div style={{
              display: 'flex', alignItems: 'center', gap: '6px',
              background: wsConnected ? 'var(--bg-badge-green)' : 'var(--bg-badge-red)',
              padding: '6px 14px', borderRadius: 'var(--radius-full)',
              fontSize: '12px', fontWeight: 600, color: wsConnected ? '#059669' : 'var(--accent-red)'
            }}>
              <div style={{ width: '6px', height: '6px', borderRadius: '50%', background: 'currentColor' }}></div>
              {wsConnected ? 'LIVE' : 'OFFLINE'}
            </div>
          </div>
        </div>


        {/* ─── TAB CONTENT ROUTING ──────────────────────── */}

        {activeTab === 'scanner' && <TabScanner logs={state.activity_log || []} />}
        {activeTab === 'history' && <TabJournal />}

        {activeTab === 'simulator' && <TabSimulator />}

        {activeTab === 'dashboard' && (
          <>
            {/* TOP ROW */}
            <div className="top-row animate-in" style={{ animationDelay: '0.05s' }}>
              <div style={{ display: 'flex', gap: '16px', flex: 1 }}>
                {/* ── DAILY P&L ── */}
                {(() => {
                  const dPnl = state.pnl || 0;
                  const dPos = dPnl >= 0;
                  return (
                    <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
                      <div className="card-header" style={{ paddingBottom: '4px' }}>
                        <div>
                          <div style={{ fontSize: '11px', fontWeight: 700, color: 'var(--text-muted)', letterSpacing: '0.05em', textTransform: 'uppercase' }}>📅 Daily Net P&L</div>
                          <div style={{ display: 'flex', alignItems: 'baseline', marginTop: '6px', gap: '8px' }}>
                            <span style={{ fontSize: '24px', fontWeight: 800, color: dPos ? '#059669' : 'var(--accent-red)', fontVariantNumeric: 'tabular-nums' }}>
                              {dPos ? '+' : ''}₹{Math.abs(dPnl).toLocaleString('en-IN')}
                            </span>
                          </div>
                        </div>
                        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: '2px', fontSize: '11px', color: 'var(--text-muted)' }}>
                          <span>Trades: <strong style={{ color: 'var(--text-primary)' }}>{ds.total}</strong></span>
                          <span>W/L: <strong style={{ color: '#059669' }}>{ds.wins}</strong>/<strong style={{ color: 'var(--accent-red)' }}>{ds.losses}</strong></span>
                        </div>
                      </div>
                      <div style={{ flex: 1, minHeight: '80px', padding: '0 8px 8px' }}>
                        <AreaChart data={[0, ...(state.pnl_history || [])]} width={300} height={90} />
                      </div>
                    </div>
                  );
                })()}

                {/* ── WEEKLY P&L ── */}
                {(() => {
                  const w = pnlSummary.weekly || {};
                  const wPnl = w.pnl || 0;
                  const wPos = wPnl >= 0;
                  const wTrades = w.trades || 0;
                  const wWins = w.wins || 0;
                  const wLosses = w.losses || 0;
                  const bd = w.breakdown || [];
                  // Cumulative equity curve: Always start at 0
                  const cumulative = [0];
                  let running = 0;
                  bd.forEach(d => { running += (d.pnl || 0); cumulative.push(running); });
                  // Day dots: Mon-Fri
                  const dayLabels = ['M', 'T', 'W', 'T', 'F'];
                  const dayMap = {};
                  bd.forEach(d => {
                    const dt = new Date(d.date + 'T00:00:00');
                    const dow = dt.getDay(); // 0=Sun, 1=Mon...
                    if (dow >= 1 && dow <= 5) dayMap[dow - 1] = d.pnl > 0 ? 'win' : 'loss';
                  });
                  const todayDow = new Date().getDay();

                  return (
                    <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
                      <div className="card-header" style={{ paddingBottom: '4px' }}>
                        <div>
                          <div style={{ fontSize: '11px', fontWeight: 700, color: 'var(--text-muted)', letterSpacing: '0.05em', textTransform: 'uppercase' }}>📊 Weekly Net P&L</div>
                          <div style={{ display: 'flex', alignItems: 'baseline', marginTop: '6px', gap: '8px' }}>
                            <span style={{ fontSize: '24px', fontWeight: 800, color: wPos ? '#059669' : 'var(--accent-red)', fontVariantNumeric: 'tabular-nums' }}>
                              {wPos ? '+' : ''}₹{Math.abs(wPnl).toLocaleString('en-IN')}
                            </span>
                          </div>
                        </div>
                        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: '2px', fontSize: '11px', color: 'var(--text-muted)' }}>
                          <span>Trades: <strong style={{ color: 'var(--text-primary)' }}>{wTrades}</strong></span>
                          <span>W/L: <strong style={{ color: '#059669' }}>{wWins}</strong>/<strong style={{ color: 'var(--accent-red)' }}>{wLosses}</strong></span>
                        </div>
                      </div>
                      <div style={{ flex: 1, minHeight: '52px', padding: '0 8px' }}>
                        <AreaChart data={cumulative} width={300} height={65} />
                      </div>
                      {/* Day dots */}
                      <div style={{ display: 'flex', justifyContent: 'center', gap: '10px', padding: '6px 0 10px' }}>
                        {dayLabels.map((label, i) => {
                          const status = dayMap[i]; // 'win' | 'loss' | undefined
                          const isFuture = (i + 1) > todayDow && todayDow !== 0;
                          return (
                            <div key={i} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '3px' }}>
                              <div style={{
                                width: '10px', height: '10px', borderRadius: '50%',
                                background: status === 'win' ? '#059669' : status === 'loss' ? 'var(--accent-red)' : isFuture ? 'var(--border-light)' : '#D1D5DB',
                                transition: 'all 0.3s ease'
                              }}></div>
                              <span style={{ fontSize: '9px', fontWeight: 600, color: 'var(--text-muted)' }}>{label}</span>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  );
                })()}

                {/* ── MONTHLY P&L ── */}
                {(() => {
                  const m = pnlSummary.monthly || {};
                  const mPnl = m.pnl || 0;
                  const mPos = mPnl >= 0;
                  const mTrades = m.trades || 0;
                  const mWins = m.wins || 0;
                  const mLosses = m.losses || 0;
                  const bd = m.breakdown || [];
                  // Cumulative equity curve: Always start at 0
                  const cumulative = [0];
                  let running = 0;
                  bd.forEach(d => { running += (d.pnl || 0); cumulative.push(running); });
                  const winRate = mTrades > 0 ? ((mWins / mTrades) * 100).toFixed(0) : '0';
                  const monthName = new Date().toLocaleDateString('en-IN', { month: 'long' });

                  return (
                    <div className="card" style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
                      <div className="card-header" style={{ paddingBottom: '4px' }}>
                        <div>
                          <div style={{ fontSize: '11px', fontWeight: 700, color: 'var(--text-muted)', letterSpacing: '0.05em', textTransform: 'uppercase' }}>📆 {monthName} P&L</div>
                          <div style={{ display: 'flex', alignItems: 'baseline', marginTop: '6px', gap: '8px' }}>
                            <span style={{ fontSize: '24px', fontWeight: 800, color: mPos ? '#059669' : 'var(--accent-red)', fontVariantNumeric: 'tabular-nums' }}>
                              {mPos ? '+' : ''}₹{Math.abs(mPnl).toLocaleString('en-IN')}
                            </span>
                          </div>
                        </div>
                        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: '2px', fontSize: '11px', color: 'var(--text-muted)' }}>
                          <span>Trades: <strong style={{ color: 'var(--text-primary)' }}>{mTrades}</strong></span>
                          <span>{winRate}% win · {bd.length} days</span>
                        </div>
                      </div>
                      <div style={{ flex: 1, minHeight: '80px', padding: '0 8px 8px' }}>
                        <AreaChart data={cumulative} width={300} height={90} />
                      </div>
                    </div>
                  );
                })()}
              </div>

              <div className="right-column">
                <NewsFeedWidget news={state.news_feed} />
              </div>
            </div>

            {/* STAT STRIP — Compact pill-style cards like the index header */}
            <div className="stat-strip animate-in" style={{ animationDelay: '0.1s' }}>
              {[
                { label: 'Win Rate', value: `${(ds.win_rate || 0).toFixed(0)}%`, icon: '📈', pct: ds.win_rate, isGood: (ds.win_rate || 0) >= 50 },
                { label: 'Avg Win', value: `₹${Math.abs(ds.avg_win || 0).toFixed(0)}`, icon: '✅', isGood: true },
                { label: 'Avg Loss', value: `₹${Math.abs(ds.avg_loss || 0).toFixed(0)}`, icon: '❌', isGood: false },
                { label: 'Profit Factor', value: (ds.profit_factor || 0) > 0 ? (ds.profit_factor || 0).toFixed(2) : '—', icon: '⚖️', isGood: (ds.profit_factor || 0) > 1 },
                { label: 'Best Trade', value: `₹${Math.abs(ds.best_trade || 0).toFixed(0)}`, icon: '🏆', isGood: (ds.best_trade || 0) > 0 },
                { label: 'ML Accuracy', value: state.ml_stats?.trained ? `${(state.ml_stats.accuracy || 0).toFixed(0)}%` : '—', icon: '🤖', isGood: state.ml_stats?.trained, sub: state.ml_stats?.trained ? `${state.ml_stats.samples}s` : '' },
              ].map((s, i) => (
                <div className="card stat-pill" key={i}>
                  <span className="stat-pill-icon">{s.icon}</span>
                  <span className="stat-pill-value">{s.value}</span>
                  <span className="stat-pill-label">{s.label}</span>
                  {s.sub && <span className="stat-pill-sub">{s.sub}</span>}
                </div>
              ))}
            </div>

            {/* BOTTOM ROW */}
            <div className="bottom-row animate-in" style={{ animationDelay: '0.15s' }}>
              <div className="card table-card">
                <div className="card-header" style={{ paddingBottom: '16px' }}>
                  <h3>Active Positions & Recent Trades</h3>
                  <span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>{state.universe_count || 0} symbols • {state.positions.length} open</span>
                </div>
                <div className="table-scroll" style={{ overflowY: 'auto' }}>
                  {state.positions.length === 0 ? (
                    <div style={{ padding: '48px 24px', textAlign: 'center' }}>
                      <div style={{ fontSize: '32px', marginBottom: '12px', filter: 'grayscale(0.3)' }}>📡</div>
                      <div style={{ fontSize: '14px', fontWeight: 600 }}>Waiting for signals</div>
                    </div>
                  ) : (
                    <table>
                      <thead>
                        <tr><th>Time</th><th>Symbol</th><th>Strategy</th><th>Type</th><th>Qty</th><th>Entry</th><th>LTP</th><th>Target</th><th style={{ textAlign: 'right' }}>P&L</th></tr>
                      </thead>
                      <tbody>
                        {state.positions.map((pos, i) => {
                          const pnl = parseFloat(pos.unrealised_pnl || 0);
                          return (
                            <tr key={i}>
                              <td style={{ color: 'var(--text-muted)', fontSize: '12px' }}>{pos.entry_time?.substring(11, 16)}</td>
                              <td className="symbol-cell">{pos.symbol}</td>
                              <td style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>{pos.strategy?.replace(/^S\d+_/, '')}</td>
                              <td><span className={`type-badge ${pos.is_short ? 'sell' : 'buy'}`}>{pos.is_short ? 'SHORT' : 'LONG'}</span></td>
                              <td>{pos.qty}</td>
                              <td>{pos.entry_price?.toFixed(1)}</td>
                              <td style={{ fontWeight: 600, color: 'var(--accent-blue)' }}>{pos.ltp?.toFixed(1) || '—'}</td>
                              <td style={{ color: 'var(--text-muted)' }}>{pos.target?.toFixed(1)}</td>
                              <td style={{ textAlign: 'right' }} className={pnl >= 0 ? 'pnl-positive' : 'pnl-negative'}>{pnl >= 0 ? '+' : ''}₹{Math.abs(pnl).toFixed(0)}</td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  )}
                </div>
              </div>

              <div className="card activity-card">
                <div className="card-header" style={{ paddingBottom: '12px' }}>
                  <h3>Engine Activity</h3>
                  <span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>{state.activity_log.length} events</span>
                </div>
                <div className="activity-list" style={{ overflowY: 'auto' }}>
                  {state.activity_log.length === 0 ? (
                    <div style={{ padding: '32px 0', textAlign: 'center' }}>⏳<br /><span style={{ fontSize: '12px', color: 'var(--text-muted)' }}>Waiting...</span></div>
                  ) : (
                    state.activity_log.slice(-30).reverse().map((entry, i) => {
                      const type = getActivityType(entry.agent);
                      return (
                        <div className="activity-item" key={i}>
                          <div className={`activity-dot-wrap ${type}`}>{getActivityIcon(type)}</div>
                          <div className="activity-text">
                            <div className="activity-title">{entry.action} {entry.detail}</div>
                            <div className="activity-sub">{entry.agent} • {entry.time}</div>
                          </div>
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            </div>
          </>
        )}
      </div>
    </>
  );
}

export default App;
