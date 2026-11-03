/* Shared browser utilities for Prizm's embedded operational dashboard. */
(function () {
  const API = '/api/v1';
  const labels = {
    '/index.html': 'Overview', '/': 'Overview', '/v2.html': 'Operations',
    '/usage.html': 'Usage', '/workflow-editor.html': 'Workflow', '/editor.html': 'Agents',
    '/workspace.html': 'Workspace', '/scheduler.html': 'Scheduler', '/config.html': 'Settings',
    '/multiagent-runs.html': 'Multi-Agent Runs', '/freewill.html': 'Freewill'
  };

  function escape(value) {
    const el = document.createElement('span');
    el.textContent = value == null ? '' : String(value);
    return el.innerHTML;
  }

  function formatTime(value) {
    if (!value) return '—';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
  }

  function relativeTime(value) {
    if (!value) return '—';
    const delta = Math.round((Date.now() - new Date(value).getTime()) / 60000);
    if (Number.isNaN(delta)) return '—';
    if (Math.abs(delta) < 1) return 'just now';
    if (Math.abs(delta) < 60) return `${Math.abs(delta)}m ${delta >= 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(Math.abs(delta) / 60);
    if (hours < 24) return `${hours}h ${delta >= 0 ? 'ago' : 'from now'}`;
    return `${Math.round(hours / 24)}d ${delta >= 0 ? 'ago' : 'from now'}`;
  }

  function statusClass(status) {
    const value = String(status || 'unknown').toLowerCase().replace(/[^a-z0-9]+/g, '-');
    if (/fail|deny|reject|error|cancel|exhausted/.test(value)) return 'danger';
    if (/pending|wait|approval|pause|warn|validat/.test(value)) return 'warning';
    if (/run|progress|active|assigned|created|queue/.test(value)) return 'info';
    if (/complete|success|healthy|running|approved|ok/.test(value)) return 'success';
    return 'neutral';
  }

  function badge(status) {
    const text = status ? String(status).replace(/_/g, ' ') : 'unknown';
    return `<span class="status-badge ${statusClass(status)}"><span aria-hidden="true">●</span> ${escape(text)}</span>`;
  }

  function apiToken() {
    try { return sessionStorage.getItem('prizm-api-token') || ''; } catch (_) { return ''; }
  }

  async function request(path, options) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 30000);
    try {
      const method = String((options && options.method) || 'GET').toUpperCase();
      const mutates = method !== 'GET' && method !== 'HEAD';
      let token = apiToken();
      if (mutates && !token) {
        // The API requires a bearer token on state-changing calls after the
        // loopback-only era. Ask once per tab session.
        const entered = window.prompt('Prizm API token (required to make changes):', '');
        if (entered === null) throw new Error('No API token provided — this change was not made.');
        token = entered.trim();
        try { sessionStorage.setItem('prizm-api-token', token); } catch (_) { /* private mode */ }
      }
      const headers = { Accept: 'application/json', ...(options && options.headers) };
      if (token) headers.Authorization = `Bearer ${token}`;
      const response = await fetch(endpoint, {
        headers,
        signal: controller.signal, ...options
      });
      const text = await response.text();
      let payload = null;
      let parsed = !text;
      try { if (text) { payload = JSON.parse(text); parsed = true; } } catch (_) { /* handled below */ }
      if (!response.ok) {
        if (response.status === 401 && mutates) {
          try { sessionStorage.removeItem('prizm-api-token'); } catch (_) { /* ignore */ }
          throw new Error('Invalid or expired API token — retry the change to enter a new one');
        }
        const message = payload && (payload.error || payload.message) || response.statusText || 'Request failed';
        throw new Error(`${message} (${response.status})`);
      }
      if (!parsed) throw new Error('The service returned malformed data.');
      return payload;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error('The service did not respond in time. You can retry safely.');
      throw error;
    } finally { clearTimeout(timeout); }
  }

  function setShellHealth(text, healthy) {
    const target = document.querySelector('[data-prizm-health]');
    if (!target) return;
    target.textContent = text;
    target.dataset.state = healthy ? 'healthy' : 'offline';
  }

  function initShell() {
    const nav = document.querySelector('.nav');
    if (!nav) return;
    const path = window.location.pathname === '/' ? '/index.html' : window.location.pathname;
    nav.setAttribute('aria-label', 'Primary navigation');
    nav.innerHTML = `
      <a class="skip-link" href="#main-content">Skip to content</a>
      <a class="brand" href="/index.html" aria-label="Prizm overview"><span aria-hidden="true">◆</span> Prizm</a>
      <button class="nav-toggle" type="button" aria-expanded="false" aria-controls="primary-nav">Menu</button>
      <div class="nav-links" id="primary-nav">
        ${Object.entries(labels).filter(([href]) => href !== '/').map(([href, label]) => `<a href="${href}" ${path === href ? 'aria-current="page"' : ''}>${label}</a>`).join('')}
      </div>
      <span class="shell-health" role="status" aria-live="polite" data-prizm-health data-state="loading">Checking service…</span>`;
    const toggle = nav.querySelector('.nav-toggle');
    toggle.addEventListener('click', () => {
      const open = nav.classList.toggle('nav-open');
      toggle.setAttribute('aria-expanded', String(open));
    });
    request('/status').then(status => setShellHealth(status.status === 'running' ? 'System healthy' : String(status.status || 'Service available'), status.status === 'running'))
      .catch(() => {
        // One retry before declaring the service down — phone/tailnet first
        // connections can be slower than expected, and a false 'unavailable'
        // chip makes a healthy service look broken.
        setTimeout(() => {
          request('/status').then(status => setShellHealth(status.status === 'running' ? 'System healthy' : String(status.status || 'Service available'), status.status === 'running'))
            .catch(() => setShellHealth('Service unavailable', false));
        }, 2500);
      });
  }

  document.addEventListener('DOMContentLoaded', initShell);
  window.PrizmUI = { API, request, escape, formatTime, relativeTime, statusClass, badge };
}());
