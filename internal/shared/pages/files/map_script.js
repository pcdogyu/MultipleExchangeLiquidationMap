let currentMode = 'weighted';
let dashboard = null;
let orderbook = null;
let persistedEvents = { bid: [], ask: [] };

function fmtPrice(n) {
  return Number(n).toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
}

function fmtAmount(n) {
  n = Number(n);
  if (!isFinite(n)) return '-';
  const a = Math.abs(n);
  if (a >= 1e8) return (n / 1e8).toFixed(2) + '亿';
  if (a >= 1e6) return (n / 1e6).toFixed(2) + '百万';
  return (n / 1e4).toFixed(2) + '万';
}

function renderActive() {
  document.querySelectorAll('button[data-mode]').forEach((b) => {
    b.classList.toggle('active', (b.dataset.mode || '') === currentMode);
  });
}

function renderMergedPanel() {
  const el = document.getElementById('mergeStats');
  const title = document.getElementById('mergeTitle');
  const hint = document.getElementById('mergeHint');
  const merged = orderbook && orderbook.merged;
  if (title) title.textContent = currentMode === 'weighted' ? '加权盘口统计' : '合并盘口统计';
  if (hint) hint.textContent = currentMode === 'weighted' ? '基于成交与 OI 估计' : '多交易所合并盘口与价差';
  if (!el) return;
  if (!merged) {
    el.innerHTML = '<div class="small">暂无盘口数据</div>';
    return;
  }
  const spread = Number(merged.best_ask || 0) - Number(merged.best_bid || 0);
  el.innerHTML =
    '<div class="merge-card"><div class="merge-label">Best Bid</div><div class="merge-val">' + fmtPrice(merged.best_bid) + '</div></div>' +
    '<div class="merge-card"><div class="merge-label">Best Ask</div><div class="merge-val">' + fmtPrice(merged.best_ask) + '</div></div>' +
    '<div class="merge-card"><div class="merge-label">Mid / Spread</div><div class="merge-val">' + fmtPrice(merged.mid) + ' <span style="font-size:12px;color:#64748b">(' + fmtPrice(spread) + ')</span></div></div>';
}

function buildShares(states) {
  const order = ['binance', 'okx', 'bybit'];
  const out = {};
  let total = 0;
  for (const ex of order) {
    const s = (states || []).find((v) => (v.exchange || '').toLowerCase() === ex);
    const oi = Number((s && s.oi_value_usd) || 0);
    out[ex] = oi;
    total += oi;
  }
  if (total <= 0) {
    for (const ex of order) out[ex] = 1 / order.length;
  } else {
    for (const ex of order) out[ex] /= total;
  }
  return out;
}

function exchangeWsHealthy(ex) {
  const ob = orderbook && orderbook[ex];
  if (!ob) return false;
  const now = Date.now();
  const last = Math.max(Number(ob.last_ws_event || 0), Number(ob.last_snapshot || 0), Number(ob.updated_ts || 0));
  if (!(last > 0)) return false;
  return (now - last) <= 20000;
}

function renderWsStatus() {
  const el = document.getElementById('wsStatus');
  if (!el) return;
  const show = (name, ex) => name + ' ' + (exchangeWsHealthy(ex) ? '已连接' : '未连接');
  el.textContent = [show('Binance', 'binance'), show('OKX', 'okx'), show('Bybit', 'bybit')].join('  ');
}

function renderMeta() {
  if (!dashboard) return;
  const t = new Date(dashboard.generated_at).toLocaleString();
  document.getElementById('meta').textContent = '覆盖 Binance / Bybit / OKX | 更新时间 ' + t + ' | 当前模式 ' + currentMode;
  const s = buildShares(dashboard.states);
  document.getElementById('weights').textContent =
    currentMode === 'weighted'
      ? 'OI 权重 Binance ' + (s.binance * 100).toFixed(1) + '% | Bybit ' + (s.bybit * 100).toFixed(1) + '% | OKX ' + (s.okx * 100).toFixed(1) + '%'
      : '合并模式下直接聚合多交易所盘口';
}

function loadModeFromStorage() {
  try {
    const m = localStorage.getItem('orderbook_mode');
    if (m === 'weighted' || m === 'merged') currentMode = m;
  } catch (_) {}
}

function setMode(mode) {
  const next = mode === 'merged' ? 'merged' : 'weighted';
  if (currentMode === next) return;
  currentMode = next;
  try {
    localStorage.setItem('orderbook_mode', currentMode);
  } catch (_) {}
  renderActive();
  load();
}

async function load() {
  const [r1, r2] = await Promise.all([
    fetch('/api/dashboard'),
    fetch('/api/orderbook?limit=60&mode=' + encodeURIComponent(currentMode)),
  ]);
  dashboard = await r1.json();
  orderbook = await r2.json();
  renderActive();
  renderMeta();
  renderWsStatus();
  renderMergedPanel();
}

async function openUpgradeModal() {
  const modal = document.getElementById('upgradeModal');
  const logEl = document.getElementById('upgradeLog');
  const foot = document.getElementById('upgradeFoot');
  if (!modal || !logEl || !foot) return;
  modal.classList.add('show');
  logEl.textContent = '';
  foot.textContent = '正在触发升级...';
  const resp = await fetch('/api/upgrade/pull', { method: 'POST' });
  const data = await resp.json().catch(() => ({ error: 'response parse failed', output: '' }));
  if (data.error) {
    logEl.textContent = String(data.output || '');
    foot.textContent = '触发失败: ' + data.error;
    return;
  }
  foot.textContent = '已触发，正在执行...';
  let stable = 0;
  for (let i = 0; i < 180; i++) {
    await new Promise((res) => setTimeout(res, 1000));
    const progress = await fetch('/api/upgrade/progress').then((x) => x.json()).catch(() => null);
    if (!progress) continue;
    logEl.textContent = String(progress.log || '');
    logEl.scrollTop = logEl.scrollHeight;
    if (progress.done) {
      foot.textContent = String(progress.exit_code || '') === '0' ? '升级完成并已重启' : '升级完成，退出码 ' + String(progress.exit_code || '?');
      return;
    }
    if (!progress.running) stable++;
    else stable = 0;
    if (stable >= 3) {
      foot.textContent = '升级进程已结束，但状态未知，请检查日志';
      return;
    }
  }
  foot.textContent = '升级仍在进行，请稍后再看';
}

function closeUpgradeModal() {
  const modal = document.getElementById('upgradeModal');
  if (modal) modal.classList.remove('show');
}

async function doUpgrade(event) {
  if (event) event.preventDefault();
  openUpgradeModal();
  return false;
}

document.querySelectorAll('button[data-mode]').forEach((b) => {
  b.onclick = () => setMode(b.dataset.mode || 'weighted');
});

loadModeFromStorage();
renderActive();
setInterval(load, 5000);
load();
(async () => {
  try {
    const resp = await fetch('/api/version');
    const v = await resp.json();
    const el = document.getElementById('globalFooter');
    if (el) el.textContent = 'Code by Yuhao@jiansutech.com - ' + (v.commit_time || '-') + ' - ' + (v.commit_id || '-') + ' - ' + (v.branch || '-');
  } catch (_) {
    const el = document.getElementById('globalFooter');
    if (el) el.textContent = 'Code by Yuhao@jiansutech.com - - - -';
  }
})();
