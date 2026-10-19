package console

// StatusHTML is the self-contained status page served at
// /v0/resource/plugins/expiring-first/status. The first payload is embedded server-side, so the
// page renders immediately and issues no request to start. It then reloads the cached snapshot
// from /v0/resource/plugins/expiring-first/data every 30 seconds while the tab is visible; that
// route only reads state. The Probe now button posts to the host's authenticated management
// route with a key the user types into the page, kept in memory only.
const StatusHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Expiring First</title>
<style>
  :root {
    --bg: #f8fafc; --surface: #ffffff; --surface-subtle: #f1f5f9; --border: #cbd5e1;
    --text: #0f172a; --text-muted: #475569; --primary: #1d4ed8;
    --success: #15803d; --success-bg: #dcfce7; --warning: #b45309; --warning-bg: #fef3c7;
    --danger: #b91c1c; --danger-bg: #fee2e2; --radius: 8px;
    --mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0b0f19; --surface: #111827; --surface-subtle: #1f2937; --border: #4b5563;
      --text: #f9fafb; --text-muted: #cbd5e1; --primary: #60a5fa;
      --success: #4ade80; --success-bg: #052e16; --warning: #fbbf24; --warning-bg: #451a03;
      --danger: #f87171; --danger-bg: #450a0a;
    }
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { max-width: 100%; overflow-x: hidden; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    background: var(--bg); color: var(--text); line-height: 1.5; padding: 1.5rem; }
  .container { max-width: 1180px; margin: 0 auto; display: flex; flex-direction: column; gap: 1.25rem; }
  header { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: baseline; justify-content: space-between; }
  h1 { font-size: 1.4rem; }
  h2 { font-size: 1rem; margin-bottom: 0.35rem; }
  .muted { color: var(--text-muted); font-size: 0.85rem; overflow-wrap: anywhere; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 1rem 1.1rem; min-width: 0; }
  .grid { display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); }
  .metric { background: var(--surface-subtle); border-radius: var(--radius); padding: 0.6rem 0.75rem; min-width: 0; }
  .metric .label { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); overflow-wrap: anywhere; }
  .metric .value { font-size: 1.05rem; font-weight: 600; font-family: var(--mono); overflow-wrap: anywhere; }
  .table-wrap { width: 100%; max-width: 100%; overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
  th, td { text-align: left; padding: 0.45rem 0.5rem; border-bottom: 1px solid var(--border); vertical-align: top; overflow-wrap: anywhere; word-break: break-word; }
  th { color: var(--text-muted); font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; }
  tr:last-child td { border-bottom: none; }
  code { font-family: var(--mono); font-size: 0.8rem; overflow-wrap: anywhere; }
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
  .toolbar { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
  button, input { font: inherit; }
  button { background: var(--primary); color: #ffffff; border: 1px solid transparent; border-radius: var(--radius);
    padding: 0.4rem 0.8rem; font-weight: 600; cursor: pointer; }
  button.secondary { background: var(--surface-subtle); color: var(--text); border-color: var(--border); }
  button:disabled { opacity: 0.55; cursor: not-allowed; }
  input[type="password"] { border: 1px solid var(--border); border-radius: var(--radius); padding: 0.4rem 0.5rem;
    background: var(--surface); color: var(--text); min-width: 0; max-width: 100%; flex: 1 1 12rem; }
  .banner { border-radius: var(--radius); padding: 0.55rem 0.75rem; font-size: 0.85rem; border: 1px solid transparent; overflow-wrap: anywhere; }
  .banner.hidden { display: none; }
  .banner.error { background: var(--danger-bg); color: var(--danger); border-color: var(--danger); }
  .banner.warn { background: var(--warning-bg); color: var(--warning); border-color: var(--warning); }
  .banner.ok { background: var(--success-bg); color: var(--success); border-color: var(--success); }
  @media (max-width: 480px) {
    body { padding: 0.75rem; }
    h1 { font-size: 1.2rem; }
  }
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

  <div id="banner" class="banner hidden" role="status" aria-live="polite"></div>

  <section class="card">
    <h2>Plugin state</h2>
    <div class="grid" id="metrics"></div>
    <div class="muted" id="serving-note" style="margin-top:0.6rem"></div>
  </section>

  <section class="card">
    <h2>Probe controls</h2>
    <p class="muted">Reloading the page and the automatic refresh both read the cached snapshot; neither starts a probe. Running a probe needs the host management key, which stays in this page's memory only.</p>
    <div class="toolbar" style="margin-top:0.6rem">
      <label class="muted" for="management-key">Management key</label>
      <input id="management-key" type="password" autocomplete="off" spellcheck="false" placeholder="host management key">
      <button id="probe-now" type="button">Probe now</button>
      <button id="reload-data" class="secondary" type="button">Refresh display</button>
      <button id="forget-key" class="secondary" type="button">Forget key</button>
    </div>
  </section>

  <section class="card">
    <h2>Pins</h2>
    <p class="muted">A pin exists only for a provider and model route this plugin actually routed traffic for.</p>
    <div id="pins"></div>
  </section>

  <section class="card">
    <h2>Schedule outcome</h2>
    <p class="muted">Live account list and saved account priority from the host, ranked by this scheduler. #1 is the predicted next pick. Last selected is the latest actual pick. Weekly and five-hour columns are Gemini quota for Antigravity; Claude/GPT/OpenAI quota is not probed unless enabled. Display refresh reloads this list; quota numbers update on the probe interval or Probe now.</p>
    <div id="account-order"></div>
  </section>

  <section class="card">
    <h2>Other quota readings</h2>
    <p class="muted">Providers without a schedule-outcome row. Antigravity Claude/GPT/OpenAI windows are omitted.</p>
    <div id="quota"></div>
  </section>

  <section class="card">
    <h2>Routing previews</h2>
    <p class="muted">What the ranking would pick right now for the configured preview routes and for routes with observed traffic. Previews are cached quota estimates: the host's actual model choice and cooldown state can differ, and a preview is not a served request. These rows start no inference calls.</p>
    <div id="previews"></div>
  </section>

  <section class="card">
    <h2>Picks served by this plugin</h2>
    <p class="muted">Scheduler decisions handled by this plugin. Successful upstream responses can be verified in the host request logs.</p>
    <div id="pick-counts"></div>
  </section>
</div>
<script id="bootstrap" type="application/json">__EXPIRING_FIRST_BOOTSTRAP_JSON__</script>
<script>
(function () {
  'use strict';
  var DATA_URL = '/v0/resource/plugins/expiring-first/data';
  var REFRESH_URL = '/v0/management/plugins/expiring-first/refresh';
  var POLL_MS = 30000;

  // The management key lives in this variable only: never in localStorage, the URL, or a log.
  var managementKey = '';
  var lastData = null;
  var pollTimer = null;
  var inFlight = false;
  var lastError = '';

  function readBootstrap() {
    try {
      return JSON.parse(document.getElementById('bootstrap').textContent);
    } catch (error) {
      return {};
    }
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) { node.className = className; }
    if (text !== undefined && text !== null) { node.textContent = String(text); }
    return node;
  }

  function table(headers, rows) {
    if (!rows.length) { return el('div', 'empty', 'No data yet.'); }
    var wrap = el('div', 'table-wrap');
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
    wrap.appendChild(node);
    return wrap;
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

  function replace(node, children) {
    node.textContent = '';
    children.forEach(function (child) { node.appendChild(child); });
  }

  function setBanner(kind, message) {
    var banner = document.getElementById('banner');
    if (!message) { banner.className = 'banner hidden'; banner.textContent = ''; return; }
    banner.className = 'banner ' + kind;
    banner.textContent = message;
  }

  function render(data) {
    data = data || {};
    lastData = data;

    var generated = data.generated_at ? new Date(data.generated_at).toLocaleString() : '';
    document.getElementById('generated').textContent = generated ? 'Updated ' + generated : '';

    var readings = data.readings || 0;
    var outcomes = data.probe_outcomes || 0;
    var metrics = document.getElementById('metrics');
    var metricNodes = [
      metric('Plugin', data.registered ? 'registered' : 'not registered'),
      metric('Providers', (data.providers && data.providers.length) ? data.providers.join(', ') : 'all'),
      metric('Version', data.version || 'dev'),
      metric('Probe interval', data.probe_interval || '—'),
      metric('Last probe', data.last_probe_at ? new Date(data.last_probe_at).toLocaleString() : 'not yet'),
      metric('Live pool', data.live_enabled ? String(data.live_pool_size) + ' (' + (data.live_strategy || 'fill-first') + ')' : 'not configured'),
      metric('Pins', (data.pins || []).length),
      metric('Readings / probe outcomes', readings + ' / ' + outcomes),
      metric('Accounts probed OK', (data.accounts_succeeded || 0) + ' of ' + outcomes),
      metric('Accounts failed', data.accounts_failed || 0),
      metric('Picks handled', data.picks_handled || 0)
    ];
    replace(metrics, metricNodes);

    document.getElementById('serving-note').textContent = (data.picks_handled || 0) > 0
      ? 'This plugin has answered ' + data.picks_handled + ' picks (decided by weekly-soonest ranking: ' + (data.picks_ranked || 0) + '). Counters only count picks this plugin handled; the host may route other traffic elsewhere.'
      : 'No picks observed yet. Registration only means the plugin loaded and registered its routes; it is not proof that the host routed traffic here.';

    var pinRows = (data.pins || []).map(function (pin) {
      return [
        pin.provider, pin.model, pin.auth_id,
        new Date(pin.pinned_at).toLocaleString(),
        new Date(pin.last_picked_at).toLocaleString(),
        pin.missing_since ? new Date(pin.missing_since).toLocaleString() : 'present'
      ];
    });
    replace(document.getElementById('pins'), [table(['Provider', 'Model', 'Auth (redacted)', 'Pinned at', 'Last picked', 'Availability'], pinRows)]);

    var orderSections = [];
    (data.previews || []).forEach(function (route) {
      var section = el('div');
      section.appendChild(el('h3', 'muted', route.provider + ' / ' + route.model));
      var rows = (route.accounts || []).map(function (account) {
        var result = account.order ? ('#' + account.order) : 'Excluded';
        if (account.next_pick) { result += ' · next pick'; }
        if (account.last_selected) { result += ' · last selected'; }
        return [result, account.auth, account.account_priority, account.weekly || '—', account.five_hour || '—', account.reason];
      });
      section.appendChild(table(['Order / outcome', 'Account', 'Account priority', 'Weekly', 'Five-hour', 'Reason'], rows));
      orderSections.push(section);
    });
    replace(document.getElementById('account-order'), orderSections.length ? orderSections : [el('div', 'empty', 'Configure preview_models or send a request to see the account order.')]);

    var rankedAuth = {};
    (data.previews || []).forEach(function (route) {
      (route.accounts || []).forEach(function (account) { rankedAuth[account.auth] = true; });
    });
    var quotaRows = [];
    (data.quota || []).forEach(function (account) {
      if (rankedAuth[account.auth] || rankedAuth[account.auth_id]) { return; }
      var label = el('div');
      label.appendChild(el('code', null, account.auth || account.auth_id));
      if (account.group) { label.appendChild(el('div', 'muted', 'group ' + account.group)); }
      if (account.provider) { label.appendChild(el('div', 'muted', account.provider)); }
      if (account.error) {
        label.appendChild(el('span', 'pill bad', 'probe failed'));
        label.appendChild(el('div', 'muted', account.error));
        if (account.retained) { label.appendChild(el('div', 'muted', 'Last valid evidence retained with its original age.')); }
      }
      if (!account.windows || !account.windows.length) {
        quotaRows.push([label, '—', el('span', 'pill warn', 'no readings'), el('span', 'pill warn', 'unknown'), account.probe_age || '—']);
        return;
      }
      account.windows.forEach(function (window, index) {
        var usage = el('div');
        usage.appendChild(el('code', null, (100 - window.remaining_percent).toFixed(0) + '% used / ' + window.remaining_percent.toFixed(0) + '% left'));
        usage.appendChild(remainingBar(window.remaining_percent));
        var state = account.stale ? el('span', 'pill warn', 'stale / ignored') : el('span', window.exhausted ? 'pill bad' : 'pill ok', window.exhausted ? 'exhausted' : 'available');
        quotaRows.push([
          index === 0 ? label : '',
          window.name + ' (' + Math.round(window.window_minutes) + 'm)',
          usage,
          state,
          resetText(window) + (window.models && window.models.length ? ' · models: ' + window.models.join(', ') : '')
        ]);
      });
      quotaRows[quotaRows.length - 1][4] = quotaRows[quotaRows.length - 1][4] + ' · reading ' + (account.probe_age || '—');
    });
    replace(document.getElementById('quota'), [table(['Auth (redacted)', 'Window', 'Usage', 'State', 'Reset / models'], quotaRows)]);

    var previewRows = (data.previews || []).map(function (preview) {
      var source = preview.observed_traffic ? el('span', 'pill info', 'observed traffic') : el('span', 'pill info', 'configured route');
      var decided = preview.ranked
        ? el('span', 'pill ok', 'quota ranking preview')
        : el('span', 'pill warn', 'delegated / no usable quota');
      return [preview.provider, preview.model, preview.auth || '—', source, decided];
    });
    replace(document.getElementById('previews'), [table(['Provider', 'Model', 'Previewed pick (redacted)', 'Route source', 'Preview basis'], previewRows)]);

    var countRows = (data.pick_counts || []).map(function (count) {
      return [count.provider, count.handled, count.ranked];
    });
    replace(document.getElementById('pick-counts'), [
      countRows.length ? table(['Provider', 'Picks handled', 'Decided by weekly-soonest'], countRows) : el('div', 'empty', 'No picks observed yet.')
    ]);
  }

  // fetchData loads the cached snapshot. It never sends the management key, so it is safe on the
  // public resource route and cannot leak the key into a request the host logs.
  function fetchData(options) {
    if (inFlight) { return; }
    inFlight = true;
    fetch(DATA_URL, { method: 'GET', headers: { 'Accept': 'application/json' }, cache: 'no-store' })
      .then(function (response) {
        if (!response.ok) { throw new Error('snapshot request failed: HTTP ' + response.status); }
        return response.json();
      })
      .then(function (data) {
        lastError = '';
        setBanner('ok', options && options.note ? options.note : '');
        render(data);
      })
      .catch(function (error) {
        lastError = error && error.message ? error.message : 'snapshot request failed';
        // Keep the last good state on screen and say it may be stale.
        setBanner('error', 'Could not reload the snapshot: ' + lastError + '. Showing the last data received' +
          (lastData && lastData.generated_at ? ' at ' + new Date(lastData.generated_at).toLocaleString() : '') + '.');
      })
      .then(function () { inFlight = false; });
  }

  function probeNow() {
    if (inFlight) { return; }
    if (!managementKey) {
      setBanner('error', 'Enter the host management key to run a probe. The key stays in this page only.');
      return;
    }
    inFlight = true;
    var button = document.getElementById('probe-now');
    button.disabled = true;
    fetch(REFRESH_URL, {
      method: 'POST',
      headers: { 'Accept': 'application/json', 'Authorization': 'Bearer ' + managementKey }
    })
      .then(function (response) {
        if (response.status === 401 || response.status === 403) {
          managementKey = '';
          document.getElementById('management-key').value = '';
          throw new Error('the management key was rejected; it has been cleared');
        }
        if (!response.ok) { throw new Error('probe request failed: HTTP ' + response.status); }
        return response.json();
      })
      .then(function (data) {
        setBanner('ok', 'Probe finished.');
        render(data);
      })
      .catch(function (error) {
        setBanner('error', error && error.message ? error.message : 'probe request failed');
      })
      .then(function () {
        inFlight = false;
        button.disabled = false;
      });
  }

  document.getElementById('management-key').addEventListener('input', function (event) {
    managementKey = event.target.value;
  });
  document.getElementById('probe-now').addEventListener('click', probeNow);
  document.getElementById('reload-data').addEventListener('click', function () { fetchData(); });
  document.getElementById('forget-key').addEventListener('click', function () {
    managementKey = '';
    document.getElementById('management-key').value = '';
    setBanner('ok', 'Management key cleared from memory.');
  });

  render(readBootstrap());

  function schedulePoll() {
    if (pollTimer !== null) { clearInterval(pollTimer); }
    pollTimer = setInterval(function () {
      if (document.visibilityState === 'visible') { fetchData(); }
    }, POLL_MS);
  }
  schedulePoll();
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') { fetchData(); }
  });
})();
</script>
</body>
</html>
`
