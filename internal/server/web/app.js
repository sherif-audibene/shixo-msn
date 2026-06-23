// shixo-msn web client. Mirrors the desktop app over the same JSON+WS API.
// All API requests go via apiFetch() which adds the bearer token.

(() => {
  const $ = (id) => document.getElementById(id);
  const TOKEN_KEY = 'shixo-msn:token';

  // ---------- state ----------
  let token = localStorage.getItem(TOKEN_KEY) || '';
  let items = [];          // newest first
  let query = '';
  let folderFilter = '';   // '' all, '\x00' uncategorized, else exact match
  let ws = null;
  let source = '';

  // ---------- helpers ----------
  function fmtSize(n) {
    if (n < 1024) return n + ' B';
    const u = ['KB', 'MB', 'GB', 'TB'];
    let i = 0;
    let v = n / 1024;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(1) + ' ' + u[i];
  }
  function fmtTime(s) {
    const d = new Date(s);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  function shortSrc(s) {
    if (!s) return '';
    s = s.replace(/\.(local|lan|home|internal)$/, '');
    const i = s.indexOf('-');
    if (i > 0 && i <= 12) return s.slice(0, i);
    return s.length > 12 ? s.slice(0, 12) : s;
  }
  function oneLine(s) {
    return (s || '').replace(/\r\n|\r|\n/g, ' ↵ ');
  }
  const PASSWORD_FOLDER = 'passwords';
  const PASSWORD_MASK = '••••••••';
  function isPasswordItem(it) {
    return it && it.kind === 'text' && (it.folder || '').toLowerCase() === PASSWORD_FOLDER;
  }
  function host() {
    return location.host || 'this server';
  }

  async function apiFetch(path, init = {}) {
    init.headers = Object.assign({}, init.headers || {}, {
      'Authorization': 'Bearer ' + token,
    });
    return fetch(path, init);
  }

  // ---------- login ----------
  $('loginForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const t = $('tokenInput').value.trim();
    if (!t) return;
    $('loginErr').textContent = '';
    // Verify by calling /api/items with the candidate token.
    const r = await fetch('/api/items', { headers: { 'Authorization': 'Bearer ' + t } });
    if (r.status === 401 || r.status === 403) {
      $('loginErr').textContent = 'Invalid token.';
      return;
    }
    if (!r.ok) {
      $('loginErr').textContent = 'Server error: ' + r.status;
      return;
    }
    token = t;
    localStorage.setItem(TOKEN_KEY, token);
    bootApp();
  });

  $('refreshBtn').addEventListener('click', () => { if (token) reconnect(); });

  // reconnect re-fetches the history and forces the websocket to drop and
  // reopen — used when the user clicks Refresh after the server was offline.
  async function reconnect() {
    if (!token) return;
    setStatus('warn', 'reconnecting…');
    if (ws) {
      try { ws.onclose = null; ws.close(); } catch {}
      ws = null;
    }
    try {
      const r = await apiFetch('/api/items');
      if (r.status === 401 || r.status === 403) {
        localStorage.removeItem(TOKEN_KEY); token = ''; showLogin(); return;
      }
      if (!r.ok) throw new Error('http ' + r.status);
      const data = await r.json();
      items = (data.items || []).slice().sort((a, b) =>
        new Date(b.created_at) - new Date(a.created_at)
      );
      applyFilter();
    } catch (e) {
      setStatus('err', 'offline: ' + e.message);
    }
    connectWS();
  }

  $('logoutBtn').addEventListener('click', () => {
    localStorage.removeItem(TOKEN_KEY);
    token = '';
    if (ws) { try { ws.close(); } catch {} ws = null; }
    showLogin();
  });

  function showLogin() {
    $('app').classList.add('hidden');
    $('login').classList.remove('hidden');
    $('tokenInput').value = '';
    $('tokenInput').focus();
  }

  function showApp() {
    $('login').classList.add('hidden');
    $('app').classList.remove('hidden');
    $('serverHost').textContent = host();
  }

  // ---------- list rendering ----------
  function uniqueFolders() {
    const s = new Set();
    items.forEach(i => { if (i.folder) s.add(i.folder); });
    return [...s].sort();
  }
  function applyFilter() {
    const q = query.toLowerCase();
    const f = folderFilter;
    const out = items.filter(it => {
      if (f === '\x00') { if (it.folder) return false; }
      else if (f && it.folder !== f) return false;
      if (!q) return true;
      const hay = ((it.title || '') + '\n' + (it.folder || '') + '\n' +
        (it.filename || '') + '\n' + (it.source || '') + '\n' + (it.text || '')).toLowerCase();
      return hay.includes(q);
    });
    renderList(out);
    refreshFolderSuggest();
  }
  function refreshFolderSuggest() {
    const folders = uniqueFolders();

    const dl = $('folderSuggest');
    dl.innerHTML = '';
    folders.forEach(f => {
      const o = document.createElement('option'); o.value = f; dl.appendChild(o);
    });

    const sel = $('folderFilter');
    const cur = sel.value;
    sel.innerHTML = '';
    [['', 'All folders'], ['\x00', 'Uncategorized']]
      .concat(folders.map(f => [f, f]))
      .forEach(([v, l]) => {
        const o = document.createElement('option');
        o.value = v; o.textContent = l;
        sel.appendChild(o);
      });
    sel.value = cur || folderFilter || '';
  }
  function renderList(visible) {
    const ol = $('history');
    ol.innerHTML = '';
    visible.forEach(it => {
      const li = document.createElement('li');
      li.dataset.id = it.id;

      const left = document.createElement('div'); left.className = 'left';
      const ic = document.createElement('span'); ic.textContent = it.kind === 'file' ? '📎' : '📝';
      const tm = document.createElement('span'); tm.textContent = fmtTime(it.created_at);
      const sr = document.createElement('span'); sr.className = 'src'; sr.textContent = shortSrc(it.source);
      left.append(ic, tm, sr);

      const center = document.createElement('div'); center.className = 'center';
      if (it.title) {
        const t = document.createElement('div'); t.className = 'ttl'; t.textContent = it.title;
        center.appendChild(t);
      }
      const pv = document.createElement('div'); pv.className = 'pv';
      const k = document.createElement('span'); k.className = 'kind';
      if (it.kind === 'text') {
        k.textContent = 'text';
        const preview = isPasswordItem(it) ? PASSWORD_MASK : oneLine(it.text || '').slice(0, 200);
        pv.append(k, preview);
      } else {
        k.textContent = 'file';
        pv.append(k, (it.filename || '') + '  ·  ' + fmtSize(it.size || 0));
      }
      center.appendChild(pv);

      const right = document.createElement('div'); right.className = 'right';
      if (it.folder) {
        const c = document.createElement('span'); c.className = 'chip'; c.textContent = it.folder;
        right.appendChild(c);
      }
      const primary = document.createElement('button');
      primary.textContent = it.kind === 'text' ? 'Copy' : 'Save';
      primary.addEventListener('click', (e) => {
        e.stopPropagation();
        if (it.kind === 'text') copyText(it);
        else saveFile(it);
      });
      const del = document.createElement('button');
      del.className = 'danger'; del.textContent = '🗑';
      del.addEventListener('click', (e) => { e.stopPropagation(); confirmDelete(it); });
      right.append(primary, del);

      li.append(left, center, right);
      li.addEventListener('click', () => openModal(it));
      ol.appendChild(li);
    });
  }

  // ---------- send text ----------
  $('sendTextBtn').addEventListener('click', async () => {
    const text = $('pasteInput').value;
    if (!text) return;
    const title = $('titleInput').value.trim();
    const folder = $('folderInput').value.trim();
    $('pasteInput').value = '';
    $('titleInput').value = '';
    $('folderInput').value = '';
    setProgress('sending text…', null);
    try {
      const r = await apiFetch('/api/items/text', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, folder, text, source }),
      });
      if (!r.ok) throw new Error(await r.text());
    } catch (e) {
      alert('send text failed: ' + e.message);
    } finally {
      clearProgress();
    }
  });

  // ---------- send file (chunked when >40MB) ----------
  $('fileInput').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    if (!file) return;
    e.target.value = '';
    const title = $('titleInput').value.trim();
    const folder = $('folderInput').value.trim();
    $('titleInput').value = '';
    $('folderInput').value = '';
    await sendFile(file, title, folder);
  });

  const CHUNK_THRESHOLD = 40 * 1024 * 1024;
  const CHUNK_SIZE = 32 * 1024 * 1024;

  async function sendFile(file, title, folder) {
    try {
      if (file.size > CHUNK_THRESHOLD) {
        await sendFileChunked(file, title, folder);
      } else {
        await sendFileSingle(file, title, folder);
      }
    } catch (e) {
      alert('upload failed: ' + e.message);
    } finally {
      clearProgress();
    }
  }

  // Use XHR for the single-shot upload to get upload progress events.
  function sendFileSingle(file, title, folder) {
    return new Promise((resolve, reject) => {
      const q = new URLSearchParams({ name: file.name, source });
      if (title) q.set('title', title);
      if (folder) q.set('folder', folder);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/items/file?' + q.toString());
      xhr.setRequestHeader('Authorization', 'Bearer ' + token);
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');
      xhr.upload.onprogress = (ev) => {
        if (ev.lengthComputable) setProgress(
          `uploading ${file.name} — ${fmtSize(ev.loaded)} / ${fmtSize(ev.total)}`,
          (ev.loaded / ev.total) * 100,
        );
      };
      xhr.onload = () => xhr.status >= 200 && xhr.status < 300
        ? resolve()
        : reject(new Error(xhr.status + ' ' + (xhr.responseText || xhr.statusText)));
      xhr.onerror = () => reject(new Error('network error'));
      setProgress(`uploading ${file.name}…`, 0);
      xhr.send(file);
    });
  }

  async function sendFileChunked(file, title, folder) {
    const init = await apiFetch('/api/uploads', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ filename: file.name, size: file.size, source, title, folder }),
    });
    if (!init.ok) throw new Error('init upload: ' + (await init.text()));
    const { upload_id, chunk_size } = await init.json();
    const cs = chunk_size > 0 ? chunk_size : CHUNK_SIZE;
    let offset = 0;
    while (offset < file.size) {
      const end = Math.min(offset + cs, file.size);
      const blob = file.slice(offset, end);
      await new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('PUT', `/api/uploads/${upload_id}/chunk?offset=${offset}`);
        xhr.setRequestHeader('Authorization', 'Bearer ' + token);
        xhr.setRequestHeader('Content-Type', 'application/octet-stream');
        const base = offset;
        xhr.upload.onprogress = (ev) => {
          if (ev.lengthComputable) {
            const done = base + ev.loaded;
            setProgress(`uploading ${file.name} — ${fmtSize(done)} / ${fmtSize(file.size)}`,
              (done / file.size) * 100);
          }
        };
        xhr.onload = () => xhr.status >= 200 && xhr.status < 300
          ? resolve()
          : reject(new Error(`chunk @${offset}: ${xhr.status} ${xhr.responseText || ''}`));
        xhr.onerror = () => reject(new Error('chunk network error'));
        xhr.send(blob);
      });
      offset = end;
    }
    const fin = await apiFetch(`/api/uploads/${upload_id}/finalize`, { method: 'POST' });
    if (!fin.ok) throw new Error('finalize: ' + (await fin.text()));
  }

  function setProgress(label, percent) {
    $('progressLabel').textContent = label;
    const bar = $('progressBar');
    bar.classList.remove('hidden');
    if (percent == null) {
      bar.removeAttribute('value'); // indeterminate
    } else {
      bar.value = percent;
    }
  }
  function clearProgress() {
    $('progressLabel').textContent = '';
    const bar = $('progressBar');
    bar.classList.add('hidden');
    bar.value = 0;
  }

  // ---------- copy / save / delete ----------
  async function copyText(it) {
    let text = it.text;
    if (!text) {
      const r = await apiFetch('/api/items/' + it.id + '/content');
      if (!r.ok) { alert('fetch failed'); return; }
      text = await r.text();
    }
    try { await navigator.clipboard.writeText(text); } catch { /* ignore */ }
  }
  function saveFile(it) {
    // Add token via query because <a download> can't set headers.
    const url = `/api/items/${it.id}/content?token=${encodeURIComponent(token)}`;
    const a = document.createElement('a');
    a.href = url;
    a.download = it.filename || 'download';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }
  async function confirmDelete(it) {
    const msg = it.kind === 'file'
      ? `Remove ${it.filename} (${fmtSize(it.size || 0)}) from all machines?`
      : 'Remove this item from all machines?';
    if (!confirm(msg)) return;
    const r = await apiFetch('/api/items/' + it.id, { method: 'DELETE' });
    if (!r.ok && r.status !== 404) alert('delete failed: ' + r.status);
  }

  // ---------- detail modal ----------
  let modalItem = null;
  let editing = false;
  let revealPwd = false; // reset on every openModal()
  function openModal(it) {
    modalItem = it;
    editing = false;
    revealPwd = false;
    const ttl = it.title ? `${it.title}   ·   ` : '';
    $('modalTitle').textContent = ttl + new Date(it.created_at).toLocaleString() + '   from ' + it.source;
    $('editTitle').value = it.title || '';
    $('editFolder').value = it.folder || '';
    const body = $('modalBody');
    body.innerHTML = '';
    if (it.kind === 'text') {
      const ta = document.createElement('textarea');
      ta.value = it.text || '';
      ta.readOnly = true;
      ta.id = 'modalText';
      body.appendChild(ta);
    } else {
      const dl = document.createElement('dl'); dl.className = 'filemeta';
      const rows = [
        ['Filename', it.filename],
        ['Size', fmtSize(it.size || 0)],
        ['SHA-256', it.sha256 || ''],
        ['Source', it.source],
        ['When', new Date(it.created_at).toLocaleString()],
      ];
      rows.forEach(([k, v]) => {
        const dt = document.createElement('dt'); dt.textContent = k;
        const dd = document.createElement('dd'); dd.textContent = v;
        dl.append(dt, dd);
      });
      body.appendChild(dl);
    }
    applyModalMode();
    $('modal').classList.remove('hidden');
  }
  function applyModalMode() {
    const it = modalItem;
    const meta = $('modalMeta'), editBtn = $('modalEdit'), cancelBtn = $('modalCancel'),
          primary = $('modalPrimary'), revealBtn = $('modalReveal');
    const pwd = isPasswordItem(it);
    if (editing) {
      meta.classList.remove('hidden');
      editBtn.classList.add('hidden');
      cancelBtn.classList.remove('hidden');
      revealBtn.classList.add('hidden'); // edit forces real text into the field
      primary.textContent = 'Save';
      if (it.kind === 'text') {
        const ta = $('modalText');
        if (ta) { ta.readOnly = false; ta.value = it.text || ''; }
      }
    } else {
      meta.classList.add('hidden');
      editBtn.classList.remove('hidden');
      cancelBtn.classList.add('hidden');
      primary.textContent = it.kind === 'text' ? 'Copy' : 'Save…';
      if (it.kind === 'text') {
        const ta = $('modalText');
        if (ta) {
          ta.readOnly = true;
          ta.value = (pwd && !revealPwd) ? PASSWORD_MASK : (it.text || '');
        }
      }
      if (pwd && it.kind === 'text') {
        revealBtn.classList.remove('hidden');
        revealBtn.textContent = revealPwd ? 'Hide' : 'Show';
      } else {
        revealBtn.classList.add('hidden');
      }
    }
  }
  function closeModal() {
    $('modal').classList.add('hidden');
    modalItem = null;
    editing = false;
  }
  $('modalClose').addEventListener('click', closeModal);
  $('modal').addEventListener('click', (e) => { if (e.target === $('modal')) closeModal(); });
  $('modalEdit').addEventListener('click', () => { editing = true; applyModalMode(); });
  $('modalReveal').addEventListener('click', () => { revealPwd = !revealPwd; applyModalMode(); });
  $('modalCancel').addEventListener('click', () => {
    editing = false;
    $('editTitle').value = modalItem.title || '';
    $('editFolder').value = modalItem.folder || '';
    applyModalMode();
  });
  $('modalDelete').addEventListener('click', () => {
    const it = modalItem; closeModal(); confirmDelete(it);
  });
  $('modalPrimary').addEventListener('click', async () => {
    const it = modalItem;
    if (editing) {
      const body = {
        title: $('editTitle').value.trim(),
        folder: $('editFolder').value.trim(),
      };
      if (it.kind === 'text') {
        body.text = ($('modalText') && $('modalText').value) || '';
      }
      const r = await apiFetch('/api/items/' + it.id, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) { alert('save failed: ' + (await r.text())); return; }
      closeModal();
    } else {
      if (it.kind === 'text') { await copyText(it); }
      else { saveFile(it); }
      closeModal();
    }
  });

  // ---------- search / filter ----------
  $('searchInput').addEventListener('input', (e) => {
    query = e.target.value.trim();
    applyFilter();
  });
  $('folderFilter').addEventListener('change', (e) => {
    folderFilter = e.target.value;
    applyFilter();
  });

  // ---------- drag & drop ----------
  let dragDepth = 0;
  document.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
    if (dragDepth === 1) $('dropOverlay').classList.remove('hidden');
  });
  document.addEventListener('dragleave', () => {
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) $('dropOverlay').classList.add('hidden');
  });
  document.addEventListener('dragover', (e) => { if (hasFiles(e)) e.preventDefault(); });
  document.addEventListener('drop', async (e) => {
    dragDepth = 0;
    $('dropOverlay').classList.add('hidden');
    if (!hasFiles(e)) return;
    e.preventDefault();
    const files = [...e.dataTransfer.files];
    const title = $('titleInput').value.trim();
    const folder = $('folderInput').value.trim();
    $('titleInput').value = '';
    $('folderInput').value = '';
    for (let i = 0; i < files.length; i++) {
      // Only attach title to the first file in a batch — same as desktop.
      await sendFile(files[i], i === 0 ? title : '', folder);
    }
  });
  function hasFiles(e) {
    if (!e.dataTransfer) return false;
    return [...e.dataTransfer.types].includes('Files');
  }

  // ---------- websocket ----------
  function connectWS() {
    const wsURL = (location.protocol === 'https:' ? 'wss://' : 'ws://') +
      location.host + '/api/ws?token=' + encodeURIComponent(token);
    setStatus('warn', 'connecting…');
    try { ws = new WebSocket(wsURL); } catch (e) { setStatus('err', 'ws error'); return; }
    ws.onopen = () => setStatus('ok', 'connected');
    ws.onclose = () => {
      setStatus('err', 'disconnected');
      // Auto-reconnect after a short delay if the user is still logged in.
      setTimeout(() => { if (token) connectWS(); }, 3000);
    };
    ws.onerror = () => setStatus('err', 'ws error');
    ws.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data);
        if (m.type === 'new_item' && m.item) {
          items = [m.item, ...items];
          applyFilter();
        } else if (m.type === 'updated' && m.item) {
          const i = items.findIndex(x => x.id === m.item.id);
          if (i >= 0) { items[i] = m.item; applyFilter(); }
        } else if (m.type === 'deleted' && m.id) {
          items = items.filter(x => x.id !== m.id);
          applyFilter();
        }
      } catch {}
    };
  }
  function setStatus(state, label) {
    const dot = $('statusDot');
    dot.classList.remove('ok', 'warn', 'err');
    dot.classList.add(state);
    $('statusText').textContent = label;
  }

  // ---------- boot ----------
  async function bootApp() {
    showApp();
    source = 'web-' + (navigator.platform || 'browser').replace(/\s+/g, '');
    // Initial fetch
    const r = await apiFetch('/api/items');
    if (r.status === 401 || r.status === 403) {
      localStorage.removeItem(TOKEN_KEY);
      token = '';
      showLogin();
      return;
    }
    if (!r.ok) {
      alert('Failed to load history: ' + r.status);
      return;
    }
    const data = await r.json();
    items = (data.items || []).slice().sort((a, b) =>
      new Date(b.created_at) - new Date(a.created_at)
    );
    applyFilter();
    connectWS();
  }

  if (token) bootApp(); else showLogin();
})();
