let page = 1;
let pageSize = 25;
let filterSmall = false;

function fmtPrice(n) {
  return Number(n).toLocaleString('zh-CN', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
}

function fmtQty(n) {
  return Number(n).toLocaleString('zh-CN', { maximumFractionDigits: 4 });
}

function fmtAmt(n) {
  n = Number(n);
  if (!isFinite(n)) return '-';
  return n.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function fmtTime(ts) {
  try {
    return new Date(Number(ts || 0)).toLocaleString('zh-CN', { hour12: false });
  } catch (_) {
    return '-';
  }
}

function render(rows) {
  const host = document.getElementById('table');
  if (!rows || !rows.length) {
    host.innerHTML = '<div class="small">暂无清算记录</div>';
    return;
  }
  let html = '<table><thead><tr><th>时间</th><th>交易所</th><th>方向</th><th>价格</th><th>数量</th><th>金额 (USD)</th></tr></thead><tbody>';
  for (const row of rows) {
    html += '<tr><td>' + fmtTime(row.event_ts) + '</td><td>' + String(row.exchange || '').toUpperCase() + '</td><td>' + String(row.side || '-') + '</td><td>' + fmtPrice(row.price) + '</td><td>' + fmtQty(row.qty) + '</td><td>' + fmtAmt(row.notional_usd) + '</td></tr>';
  }
  html += '</tbody></table>';
  host.innerHTML = html;
}

function toggleFilter() {
  filterSmall = !filterSmall;
  const btn = document.getElementById('filterBtn');
  if (btn) btn.textContent = filterSmall ? '显示全部' : '过滤小单';
  load();
}

async function load() {
  const resp = await fetch('/api/liquidations?page=' + page + '&limit=' + pageSize);
  const data = await resp.json();
  let rows = data.rows || [];
  if (filterSmall) rows = rows.filter((x) => Number(x.qty || 0) >= 1);
  document.getElementById('meta').textContent =
    '第 ' + data.page + ' 页 | 每页 ' + data.page_size + ' 条' + (filterSmall ? ' | 已过滤小于 1 ETH 的记录' : '');
  render(rows);
}

function prev() {
  if (page > 1) {
    page--;
    load();
  }
}

function next() {
  page++;
  load();
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
