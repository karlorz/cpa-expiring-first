package console

// StatusHTML is the self-contained status page served at
// /v0/resource/plugins/expiring-first/status. The payload is embedded server-side, so the page
// issues no further requests and needs no management key. Credential IDs are redacted by the
// server before they reach the page.
const StatusHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Expiring First</title>
<style>
  :root {
    --bg: #f8fafc; --surface: #ffffff; --surface-subtle: #f1f5f9; --border: #e2e8f0;
    --text: #0f172a; --text-muted: #64748b; --primary: #2563eb;
    --success: #16a34a; --success-bg: #dcfce7; --warning: #d97706; --warning-bg: #fef3c7;
    --danger: #dc2626; --danger-bg: #fee2e2; --radius: 8px;
    --mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0b0f19; --surface: #111827; --surface-subtle: #1f2937; --border: #374151;
      --text: #f9fafb; --text-muted: #9ca3af; --primary: #3b82f6;
      --success: #22c55e; --success-bg: #052e16; --warning: #f59e0b; --warning-bg: #451a03;
      --danger: #ef4444; --danger-bg: #450a0a;
    }
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    background: var(--bg); color: var(--text); line-height: 1.5; padding: 1.5rem; }
  .container { max-width: 1180px; margin: 0 auto; display: flex; flex-direction: column; gap: 1.25rem; }
  header { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: baseline; justify-content: space-between; }
  h1 { font-size: 1.4rem; }
  h2 { font-size: 1rem; margin-bottom: 0.5rem; }
  .muted { color: var(--text-muted); font-size: 0.85rem; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 1rem 1.1rem; }
  .grid { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); }
  .metric { background: var(--surface-subtle); border-radius: var(--radius); padding: 0.6rem 0.75rem; }
  .metric .label { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
  .metric .value { font-size: 1.05rem; font-weight: 600; font-family: var(--mono); }
  table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
  th, td { text-align: left; padding: 0.45rem 0.5rem; border-bottom: 1px solid var(--border); vertical-align: top; }
  th { color: var(--text-muted); font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; }
  tr:last-child td { border-bottom: none; }
  code { font-family: var(--mono); font-size: 0.8rem; }
  .pill { display: inline-block; padding: 0.1rem 0.45rem; border-radius: 999px; font-size: 0.72rem; font-weight: 600; }
  .pill.ok { background: var(--success-bg); color: var(--success); }
  .pill.warn { background: var(--warning-bg); color: var(--warning); }
  .pill.bad { background: var(--danger-bg); color: var(--danger); }
  .pill.info { background: var(--surface-subtle); color: var(--text-muted); }
  .bar { position: relative; height: 8px; border-radius: 999px; background: var(--surface-subtle); overflow: hidden; min-width: 90px; }
  .bar span { position: absolute; inset: 0 auto 0 0; background: var(--primary); }
  .bar span.warn { background: var(--warning); }
  .bar span.bad { background: var(--danger); }
  .empty { color: var(--text-muted); font-size: 0.85rem; font-style: italic; }
</style>
</head>
<body>
<div class="container">
  <header>
    <div>
      <h1>Expiring First</h1>
      <div class="muted">Weekly-soonest credential ranking with in-process quota probes.</div>
    </div>
    <div class="muted" id="generated"></div>
  </header>

  <section class="card">
    <h2>Status</h2>
    <div class="grid" id="metrics"></div>
  </section>

  <section class="card">
    <h2>Pins</h2>
    <div id="pins"></div>
  </section>

  <section class="card">
    <h2>Accounts and quota windows</h2>
    <div id="quota"></div>
  </section>

  <section class="card">
    <h2>Next pick per provider</h2>
    <div id="next-picks"></div>
  </section>

  <section class="card">
    <h2>Picks served by this plugin</h2>
    <div id="pick-counts"></div>
  </section>
</div>
<script id="bootstrap" type="application/json">__EXPIRING_FIRST_BOOTSTRAP_JSON__</script>
<script>
(function () {
  var data = {};
  try { data = JSON.parse(document.getElementById('bootstrap').textContent); } catch (error) { data = {}; }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) { node.className = className; }
    if (text !== undefined && text !== null) { node.textContent = String(text); }
    return node;
  }

  function table(headers, rows) {
    if (!rows.length) { return el('div', 'empty', 'No data yet.'); }
    var node = el('table');
    var head = el('thead');
    var headRow = el('tr');
    headers.forEach(function (header) { headRow.appendChild(el('th', null, header)); });
    head.appendChild(headRow);
    node.appendChild(head);
    var body = el('tbody');
    rows.forEach(function (cells) {
      var row = el('tr');
      cells.forEach(function (cell) {
        var td = el('td');
        if (cell instanceof Node) { td.appendChild(cell); } else { td.textContent = cell === undefined || cell === null ? '—' : String(cell); }
        row.appendChild(td);
      });
      body.appendChild(row);
    });
    node.appendChild(body);
    return node;
  }

  function remainingBar(percent) {
    var wrap = el('div', 'bar');
    var fill = el('span');
    var clamped = Math.max(0, Math.min(100, percent));
    fill.style.width = clamped + '%';
    if (clamped <= 0) { fill.className = 'bad'; } else if (clamped <= 25) { fill.className = 'warn'; }
    wrap.appendChild(fill);
    return wrap;
  }

  function metric(label, value) {
    var node = el('div', 'metric');
    node.appendChild(el('div', 'label', label));
    node.appendChild(el('div', 'value', value));
    return node;
  }

  function resetText(window) {
    if (!window.resets_at) { return 'window not started'; }
    return window.reset_in ? window.reset_in : window.resets_at;
  }

  var generated = data.generated_at ? new Date(data.generated_at).toLocaleString() : '';
  document.getElementById('generated').textContent = generated ? 'Updated ' + generated : '';

  var metrics = document.getElementById('metrics');
  metrics.appendChild(metric('Scheduler', data.enabled ? 'active' : 'disabled'));
  metrics.appendChild(metric('Providers', (data.providers && data.providers.length) ? data.providers.join(', ') : 'all'));
  metrics.appendChild(metric('Version', data.version || 'dev'));
  metrics.appendChild(metric('Probe interval', data.probe_interval || '—'));
  metrics.appendChild(metric('Last probe', data.last_probe_at ? new Date(data.last_probe_at).toLocaleString() : 'not yet'));
  metrics.appendChild(metric('Live pool', data.live_enabled ? String(data.live_pool_size) + ' (' + (data.live_strategy || 'fill-first') + ')' : 'not configured'));
  metrics.appendChild(metric('Pins', (data.pins || []).length));
  metrics.appendChild(metric('Accounts with readings', (data.quota || []).length));

  var pinRows = (data.pins || []).map(function (pin) {
    return [
      pin.provider, pin.model, pin.auth_id,
      new Date(pin.pinned_at).toLocaleString(),
      new Date(pin.last_picked_at).toLocaleString(),
      pin.missing_since ? new Date(pin.missing_since).toLocaleString() : 'present'
    ];
  });
  document.getElementById('pins').appendChild(table(['Provider', 'Model', 'Auth (redacted)', 'Pinned at', 'Last picked', 'Availability'], pinRows));

  var quotaRows = [];
  (data.quota || []).forEach(function (account) {
    var label = el('div');
    label.appendChild(el('code', null, account.auth || account.auth_id));
    if (account.group) { label.appendChild(el('div', 'muted', 'group ' + account.group)); }
    if (account.provider) { label.appendChild(el('div', 'muted', account.provider)); }
    if (account.error) { label.appendChild(el('span', 'pill bad', account.error)); }
    if (!account.windows || !account.windows.length) {
      quotaRows.push([label, '—', '—', '—', account.probe_age || '—']);
      return;
    }
    account.windows.forEach(function (window, index) {
      var usage = el('div');
      usage.appendChild(el('code', null, (100 - window.remaining_percent).toFixed(0) + '% used / ' + window.remaining_percent.toFixed(0) + '% left'));
      usage.appendChild(remainingBar(window.remaining_percent));
      var state = el('span', window.exhausted ? 'pill bad' : 'pill ok', window.exhausted ? 'exhausted' : 'available');
      quotaRows.push([
        index === 0 ? label : '',
        window.name + ' (' + Math.round(window.window_minutes) + 'm)',
        usage,
        state,
        resetText(window) + (window.models && window.models.length ? ' · models: ' + window.models.join(', ') : '')
      ]);
    });
    quotaRows[quotaRows.length - 1][4] = quotaRows[quotaRows.length - 1][4] + ' · probe ' + (account.probe_age || '—');
  });
  document.getElementById('quota').appendChild(table(['Auth (redacted)', 'Window', 'Usage', 'State', 'Reset / models'], quotaRows));

  var nextRows = (data.next_picks || []).map(function (pick) {
    var status = pick.ranked ? el('span', 'pill ok', 'weekly-soonest') : el('span', 'pill info', 'sticky / built-in');
    return [pick.provider, pick.model, pick.auth || '—', status];
  });
  document.getElementById('next-picks').appendChild(table(['Provider', 'Representative model', 'Auth (redacted)', 'Decided by'], nextRows));

  var countRows = (data.pick_counts || []).map(function (count) {
    return [count.provider, count.handled, count.ranked];
  });
  document.getElementById('pick-counts').appendChild(table(['Provider', 'Picks handled', 'Decided by weekly-soonest'], countRows));
})();
</script>
</body>
</html>
`
