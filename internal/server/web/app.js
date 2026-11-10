const state = { clients: [], key: '', connected: false, updatedAt: null, error: '' };
const $ = (id) => document.getElementById(id);
const labels = { heart_rate: 'Heart rate', person_temperature: 'Body temperature', ambient_temperature: 'Room temperature', motion: 'Motion' };
const glyphs = { heart_rate: '♡', person_temperature: '♨', ambient_temperature: '☀', motion: '⌁' };
const thresholds = { heart_rate: [40, 120], person_temperature: [35, 38] };
const recentWindow = 10000;

function statusFor(client) {
  const seen = Date.parse(client.last_seen || '');
  if (!Number.isFinite(seen) || Date.now() - seen > recentWindow) return 'stale';
  const limits = thresholds[client.type];
  if (client.has_value && limits) {
    const value = Number(client.value);
    if (Number.isFinite(value) && (value < limits[0] || value > limits[1])) return 'warn';
  }
  return 'active';
}

function ago(date) {
  const time = Date.parse(date || '');
  if (!Number.isFinite(time)) return 'No reading yet';
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 5) return 'Just now';
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function cell(row, content, className = '') {
  const td = document.createElement('td');
  if (className) td.className = className;
  if (typeof content === 'string') td.textContent = content;
  else td.append(content);
  row.append(td);
  return td;
}

function render() {
  const now = new Date();
  $('today').textContent = new Intl.DateTimeFormat(undefined, { month: 'long', day: 'numeric', year: 'numeric' }).format(now);
  const clients = state.clients;
  const active = clients.filter(c => statusFor(c) === 'active').length;
  const attention = clients.filter(c => statusFor(c) !== 'active').length;
  $('total-count').textContent = state.connected ? clients.length : '—';
  $('active-count').textContent = state.connected ? active : '—';
  $('attention-count').textContent = state.connected ? attention : '—';
  $('last-updated').textContent = state.updatedAt ? new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' }).format(state.updatedAt) : '—';
  const pill = $('connection-pill');
  pill.classList.toggle('offline', !state.connected);
  pill.lastElementChild.textContent = state.connected ? 'Hub connected' : (state.error || 'Connecting');
  $('hub-state').textContent = state.connected ? 'Connected and listening' : (state.error || 'Checking connection');
  $('hub-description').textContent = state.connected ? 'The server is responding and sensor data is up to date.' : 'Live readings will appear when the connection is restored.';

  const search = $('search').value.trim().toLowerCase();
  const filter = $('filter').value;
  const filtered = clients.filter(c => {
    const status = statusFor(c);
    const matches = `${c.id} ${labels[c.type] || c.type}`.toLowerCase().includes(search);
    return matches && (filter === 'all' || (filter === 'active' && status === 'active') || (filter === 'attention' && status !== 'active'));
  });
  const body = $('sensor-rows');
  body.replaceChildren();
  if (!filtered.length) {
    const tr = document.createElement('tr');
    const td = cell(tr, state.connected ? (clients.length ? 'No sensors match your search.' : 'No sensors connected yet. Send a reading or heartbeat to get started.') : 'Waiting for Home Hub data…', 'empty');
    td.colSpan = 5;
    body.append(tr);
  } else {
    filtered.forEach(client => {
      const tr = document.createElement('tr');
      const name = document.createElement('span'); name.className = 'sensor-name';
      const icon = document.createElement('span'); icon.className = 'sensor-glyph'; icon.textContent = glyphs[client.type] || '◈';
      const id = document.createElement('span'); id.textContent = client.id;
      name.append(icon, id); cell(tr, name);
      cell(tr, labels[client.type] || client.type);
      cell(tr, client.has_value ? `${client.value} ${client.unit || ''}`.trim() : 'Awaiting reading', 'reading');
      cell(tr, ago(client.last_seen));
      const status = statusFor(client);
      const badge = document.createElement('span'); badge.className = `status-badge ${status === 'active' ? '' : status}`; badge.textContent = { active: 'Active', stale: 'Stale', warn: 'Review' }[status];
      cell(tr, badge); body.append(tr);
    });
  }
  $('table-count').textContent = state.connected ? `Showing ${filtered.length} of ${clients.length} sensors` : 'Waiting for data';
}

async function refresh() {
  const headers = state.key ? { Authorization: `Bearer ${state.key}` } : {};
  try {
    const response = await fetch('/clients', { headers, cache: 'no-store' });
    if (response.status === 401) {
      const hadKey = Boolean(state.key);
      state.connected = false; state.error = 'API key required'; state.key = '';
      if (!$('auth-dialog').open) $('auth-dialog').showModal();
      $('auth-error').textContent = hadKey ? 'That key was not accepted. Please try again.' : '';
      render(); return;
    }
    if (!response.ok) throw new Error(`Server returned ${response.status}`);
    const data = await response.json();
    if (!Array.isArray(data)) throw new Error('Unexpected server response');
    state.clients = data; state.connected = true; state.error = ''; state.updatedAt = new Date();
    if ($('auth-dialog').open) $('auth-dialog').close();
  } catch (error) {
    state.connected = false; state.error = 'Connection unavailable';
  }
  render();
}

$('search').addEventListener('input', render);
$('filter').addEventListener('change', render);
$('auth-form').addEventListener('submit', event => {
  event.preventDefault();
  state.key = $('api-key').value.trim();
  $('api-key').value = '';
  $('auth-error').textContent = '';
  if (state.key) refresh();
});
render();
refresh();
setInterval(() => { if (!$('auth-dialog').open) refresh(); else render(); }, 5000);
