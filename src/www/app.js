/* ===========================================================
   LXCPanel 前端
   =========================================================== */
(() => {
  'use strict';

  /* ---------- base url / api ---------- */
  const BASE = (() => {
    let p = location.pathname;
    if (!p.endsWith('/')) p = p.slice(0, p.lastIndexOf('/') + 1);
    if (p.endsWith('/index.html')) p = p.slice(0, -'index.html'.length);
    return p || '/';
  })();
  const api = (p) => BASE + 'api/' + p;
  const icon = (n, cls) => `<svg class="${cls || ''}" width="16" height="16" aria-hidden="true"><use href="#i-${n}"/></svg>`;

  /* ---------- state ---------- */
  const S = {
    view: 'overview',
    data: null,
    containers: [],
    env: null,
    settings: null,
    distros: null,
    drawerAux: null,
    filter: { q: '', state: 'all' },
    drawer: null,
    pollTimer: null,
    drawerTimer: null,
    create: null,
    taskES: null,
    term: null,
    loading: true,
  };

  const $ = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

  /* ---------- format ---------- */
  const STATE_TEXT = {
    RUNNING: '运行中', STOPPED: '已停止', FROZEN: '已冻结', STARTING: '启动中',
    STOPPING: '停止中', FREEZING: '冻结中', THAWED: '恢复中', ABORTING: '中止中',
    UNKNOWN: '未知', ERROR: '异常',
  };
  const stateClass = (s) => (s === 'RUNNING' ? 'running' : s === 'FROZEN' ? 'frozen' : 'stopped');

  function fmtBytes(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0, n = b;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (n >= 100 || i === 0 ? Math.round(n) : n.toFixed(n >= 10 ? 1 : 2)) + ' ' + u[i];
  }
  function fmtUptime(sec) {
    sec = Math.max(0, sec | 0);
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d} 天 ${h} 小时`;
    if (h > 0) return `${h} 小时 ${m} 分`;
    return `${m} 分钟`;
  }
  function fmtDate(ts) {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  // 后端空切片可能序列化成 null，所有数组成员访问都必须先过 arr()。
  const arr = (v) => (Array.isArray(v) ? v : []);

  function pctClass(p) { return p >= 90 ? 'crit' : p >= 70 ? 'warn' : ''; }

  /* ---------- toast ---------- */
  function toast(msg, type) {
    const el = document.createElement('div');
    el.className = 'toast ' + (type || 'info');
    el.innerHTML = icon(type === 'err' ? 'alert' : type === 'ok' ? 'check' : 'info') + `<div>${esc(msg)}</div>`;
    $('#toasts').appendChild(el);
    setTimeout(() => { el.classList.add('out'); setTimeout(() => el.remove(), 260); }, type === 'err' ? 5200 : 3200);
  }

  /* ---------- 剪贴板 ---------- */
  // fnOS 面板多是 http 局域网访问，非安全上下文没有 navigator.clipboard，
  // 这里用 textarea + execCommand 兜底。
  async function copyText(text) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch (_) { /* 继续走兜底 */ }
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.top = '-1000px';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      ta.setSelectionRange(0, ta.value.length);
      const ok = document.execCommand('copy');
      ta.remove();
      return ok;
    } catch (_) {
      return false;
    }
  }

  async function copyWithToast(text, what) {
    const ok = await copyText(text);
    toast(ok ? (what || '已复制') : '复制失败，请手动选中', ok ? 'ok' : 'err');
  }

  /* ---------- http ---------- */
  async function req(path, opts) {
    const o = Object.assign({ headers: {} }, opts || {});
    if (o.body && typeof o.body === 'object') {
      o.headers['Content-Type'] = 'application/json';
      o.body = JSON.stringify(o.body);
    }
    const r = await fetch(api(path), o);
    if (r.status === 401 && !String(path).startsWith('auth/')) {
      location.reload();
      throw new Error('登录状态已失效');
    }
    const ct = r.headers.get('content-type') || '';
    const body = ct.includes('json') ? await r.json() : await r.text();
    if (!r.ok) throw new Error((body && body.error) || r.statusText || '请求失败');
    return body;
  }

  /* ---------- theme ---------- */
  function initTheme() {
    const saved = localStorage.getItem('lxcpanel-theme');
    const sys = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    const q = new URLSearchParams(location.search).get('theme');
    const forced = (q === 'dark' || q === 'light') ? q : null;
    applyTheme(forced || saved || sys);
  }
  function applyTheme(t) {
    document.documentElement.setAttribute('data-theme', t);
    $('#btnTheme').innerHTML = icon(t === 'dark' ? 'sun' : 'moon');
  }
  $('#btnTheme').onclick = () => {
    const cur = document.documentElement.getAttribute('data-theme');
    const next = cur === 'dark' ? 'light' : 'dark';
    localStorage.setItem('lxcpanel-theme', next);
    applyTheme(next);
  };

  /* ---------- data ---------- */
  async function loadOverview() {
    try {
      const d = await req('overview');
      S.data = d;
      S.containers = d.containers || [];
      S.env = d.env;
      S.loading = false;
      updateChrome();
      if (S.view === 'overview') renderOverview();
      else if (S.view === 'containers') patchContainerRows();
    } catch (e) {
      S.loading = false;
      if (!S._errShown) { toast('读取系统状态失败：' + e.message, 'err'); S._errShown = true; }
    }
  }

  function updateChrome() {
    const d = S.data;
    if (!d) return;
    const sys = d.system, env = d.env, sum = d.summary;
    $('#navCount').textContent = sum ? sum.total : 0;
    const envProblems = arr(env && env.problems);
    $('#navEnv').textContent = envProblems.length ? envProblems.length : '✓';
    $('#navEnv').style.background = envProblems.length ? 'var(--danger-soft)' : 'var(--success-soft)';
    $('#navEnv').style.color = envProblems.length ? 'var(--danger)' : 'var(--success)';
    $('#hostTag').textContent = (sys.hostname || 'fnOS') + ' · ' + (sys.os_name || '');
    $('#verTag').textContent = 'v' + (sys.panel_version || '1.0.0');
    const chip = $('#chipLxc');
    if (env.installed) {
      chip.innerHTML = `<i class="dot"></i><span>LXC ${esc(env.version || '已就绪')}</span>`;
      chip.querySelector('.dot').style.background = 'var(--success)';
    } else {
      chip.innerHTML = `<i class="dot"></i><span>未安装 LXC</span>`;
      chip.querySelector('.dot').style.background = 'var(--danger)';
    }
  }

  /* ---------- router ---------- */
  const TITLES = {
    overview: ['概览', '系统资源与容器运行总览'],
    containers: ['容器', '管理全部 LXC 容器'],
    create: ['创建容器', '从官方镜像源快速部署新容器'],
    ports: ['端口转发', '把宿主机端口映射到容器服务'],
    env: ['运行环境', 'LXC 依赖与内核能力检查'],
    settings: ['设置与 API', '访问控制、镜像源与 API 调用方式'],
  };
  function go(view, force) {
    if (S.view === view && !force) return;
    S.view = view;
    if (location.hash !== '#' + view) {
      try { history.replaceState(null, '', '#' + view); } catch (_) { }
    }
    $$('.nav-item').forEach((el) => el.classList.toggle('active', el.dataset.view === view));
    const t = TITLES[view] || ['', ''];
    $('#viewTitle').textContent = t[0];
    $('#viewSub').textContent = t[1];
    render();
  }
  // 再次点击当前项也强制重绘，便于从渲染异常中恢复。
  $$('.nav-item').forEach((el) => (el.onclick = () => go(el.dataset.view, true)));

  function viewFromHash() {
    const h = (location.hash || '').replace('#', '');
    return TITLES[h] ? h : 'overview';
  }

  function render() {
    const v = $('#view');
    v.classList.remove('view-enter');
    void v.offsetWidth;
    v.classList.add('view-enter');
    try {
      if (S.view === 'overview') renderOverview();
      else if (S.view === 'containers') renderContainers();
      else if (S.view === 'create') renderCreate();
      else if (S.view === 'ports') renderPorts();
      else if (S.view === 'env') renderEnv();
      else if (S.view === 'settings') renderSettings();
    } catch (err) {
      // 渲染抛错时必须清空容器，否则会残留上一个视图，看起来像"点错了页面"。
      console.error('[LXCPanel] render failed:', err);
      v.innerHTML = `<div class="card card-pad">
        <div class="section-title">${icon('alert')} 页面渲染失败</div>
        <div class="muted" style="font-size:12.5px;line-height:1.8">
          当前视图「${esc(TITLES[S.view] ? TITLES[S.view][0] : S.view)}」数据异常：${esc(err && err.message ? err.message : String(err))}
        </div>
        <button class="btn primary" style="margin-top:14px" onclick="location.reload()">${icon('refresh')} 刷新页面</button>
      </div>`;
    }
  }

  /* =========================================================
     概览
     ========================================================= */
  function statCard(label, ic, value, unit, foot, accent, extra) {
    return `<div class="stat" style="--accent:${accent}">
      <div class="stat-top">${icon(ic)}<span>${label}</span></div>
      <div class="stat-value">${value}${unit ? `<small>${unit}</small>` : ''}</div>
      <div class="stat-foot">${foot || ''}</div>
      ${extra || ''}
    </div>`;
  }

  function barBlock(label, used, total, unit) {
    const p = total ? Math.min(100, (used / total) * 100) : 0;
    return `<div style="margin-bottom:14px">
      <div class="stat-line"><span class="lbl">${label}</span>
        <span class="val">${fmtBytes(used)} / ${fmtBytes(total)}<span class="muted" style="font-weight:400"> · ${p.toFixed(1)}%</span></span></div>
      <div class="bar ${pctClass(p)}"><i style="width:${p.toFixed(2)}%"></i></div>
    </div>`;
  }

  function renderOverview() {
    if (!S.data) { $('#view').innerHTML = skeletonView(); return; }
    const d = S.data, sys = d.system, sum = d.summary, env = d.env;
    const memTotal = sys.mem_total, memUsed = sys.mem_total - sys.mem_avail;
    const diskUsed = sys.disk_total - sys.disk_free;
    const memPct = memTotal ? (memUsed / memTotal) * 100 : 0;
    const runningContainers = (d.containers || []).filter((c) => c.state === 'RUNNING');

    const problems = arr(env.problems);
    const banner = problems.length ? `
      <div class="card card-pad" style="margin-bottom:16px;border-color:color-mix(in srgb,var(--warning) 40%,var(--border));background:var(--warning-soft)">
        <div style="display:flex;gap:12px;align-items:flex-start">
          <div class="check-ico warn" style="margin-top:0">${icon('alert')}</div>
          <div style="flex:1">
            <b style="font-size:13px">运行环境需要处理（${problems.length} 项）</b>
            <div class="muted" style="font-size:12px;margin-top:4px;line-height:1.6">${esc(env.problems[0])}</div>
          </div>
          <button class="btn sm" onclick="window.__go('env')">去处理</button>
        </div>
      </div>` : '';

    const rows = (d.containers || []).slice(0, 6).map((c) => `
      <tr onclick="window.__openContainer('${esc(c.name)}')">
        <td><div class="cell-name">${esc(c.name)}
          <span class="state ${stateClass(c.state)}" style="margin-left:2px"><i></i>${STATE_TEXT[c.state] || c.state}</span></div>
          <div class="cell-sub">${esc(c.os || '—')}</div></td>
        <td class="mono muted">${c.ipv4 && c.ipv4.length ? esc(c.ipv4[0]) : '—'}${arr(c.ipv6).length ? `<div class="cell-sub mono" style="font-size:10.5px">${esc(c.ipv6[0])}</div>` : ''}</td>
        <td class="right mono">${c.state === 'RUNNING' ? c.cpu_percent.toFixed(1) + '%' : '—'}</td>
        <td class="right mono">${c.state === 'RUNNING' ? fmtBytes(c.mem_bytes) : '—'}</td>
      </tr>`).join('');

    $('#view').innerHTML = `
      ${banner}
      <div class="grid stats" style="margin-bottom:16px">
        ${statCard('容器总数', 'box', sum.total, '个', `${sum.running} 运行 · ${sum.frozen} 冻结 · ${sum.stopped} 停止`, 'var(--primary)')}
        ${statCard('CPU 负载', 'cpu', sys.load1.toFixed(2), '', `${sys.cpu_count} 核 · 5 分钟 ${sys.load5.toFixed(2)}`, 'var(--violet)')}
        ${statCard('内存占用', 'mem', memPct.toFixed(1), '%', `${fmtBytes(memUsed)} / ${fmtBytes(memTotal)}`, 'var(--success)')}
        ${statCard('存储空间', 'disk', sys.disk_total ? ((diskUsed / sys.disk_total) * 100).toFixed(1) : '0', '%', `${fmtBytes(diskUsed)} / ${fmtBytes(sys.disk_total)}`, 'var(--warning)')}
      </div>

      <div class="grid two">
        <div class="card">
          <div class="card-head"><h3>容器</h3><span class="hint">${sum.running} 运行 / ${sum.total} 总数</span></div>
          ${rows ? `<div class="table-wrap"><table class="tbl">
            <thead><tr><th>名称</th><th>IP 地址</th><th class="right">CPU</th><th class="right">内存</th></tr></thead>
            <tbody>${rows}</tbody></table></div>`
        : `<div class="empty">${icon('box')}<h4>还没有容器</h4><p>选择一个发行版模板，几十秒即可跑起第一个容器。</p>
             <button class="btn primary" onclick="window.__go('create')">${icon('plus')} 创建容器</button></div>`}
        </div>

        <div>
          <div class="card card-pad" style="margin-bottom:14px">
            <div class="section-title">${icon('layers')} 主机资源</div>
            ${barBlock('内存', memUsed, memTotal)}
            ${barBlock('存储（' + esc(sys.disk_path) + '）', diskUsed, sys.disk_total)}
            <dl class="kv" style="margin-top:16px">
              <dt>主机名</dt><dd>${esc(sys.hostname)}</dd>
              <dt>系统</dt><dd>${esc(sys.os_name || '—')}</dd>
              <dt>内核</dt><dd class="mono">${esc(sys.kernel)}</dd>
              <dt>架构</dt><dd>${esc(sys.arch)} · cgroup v${sys.cgroup_version}</dd>
              <dt>运行时长</dt><dd>${fmtUptime(sys.uptime_sec)}</dd>
              <dt>LXC 版本</dt><dd>${esc(sys.lxc_version || '未安装')}</dd>
            </dl>
          </div>

          <div class="card card-pad">
            <div class="section-title">${icon('box')} 运行中的容器 <span class="count">${runningContainers.length}</span></div>
            ${runningContainers.length ? runningContainers.map((c) => `
              <div style="display:flex;align-items:center;gap:10px;padding:7px 0;cursor:pointer" onclick="window.__openContainer('${esc(c.name)}')">
                <span class="state running"><i></i></span>
                <span style="flex:1;font-size:13px;font-weight:550">${esc(c.name)}</span>
                <span class="mono muted" style="font-size:11.5px">${c.cpu_percent.toFixed(1)}% · ${fmtBytes(c.mem_bytes)}</span>
              </div>`).join('')
        : `<div class="muted" style="font-size:12.5px;padding:6px 0">暂无运行中的容器</div>`}
          </div>
        </div>
      </div>

      <div class="card" style="margin-top:14px">
        <div class="card-head"><h3>整机资源趋势</h3><span class="hint" id="hostHint"></span></div>
        <div class="card-pad">
          <svg class="chart" id="hostChart" viewBox="0 0 600 150" preserveAspectRatio="none" style="height:150px"></svg>
          <div class="chart-legend">
            <span><i style="background:var(--primary)"></i>CPU 使用率</span>
            <span><i style="background:var(--violet)"></i>内存占用</span>
          </div>
        </div>
      </div>`;

    drawHostChart($('#hostChart'), d.host_points || [], sys.mem_total);
  }

  function drawHostChart(svg, pts, memTotal) {
    if (!svg) return;
    const hint = $('#hostHint');
    const step = (S.data && S.data.sample_secs) || 10;
    if (!pts || pts.length < 2) {
      svg.innerHTML = `<text x="300" y="78" text-anchor="middle" fill="var(--muted)" font-size="11">正在采集采样点，约 20 秒后显示趋势</text>`;
      if (hint) hint.textContent = '';
      return;
    }
    const W = 600, H = 150, pad = 8;
    const n = pts.length;
    const x = (i) => (i / (n - 1)) * W;
    const yCpu = (v) => H - pad - (Math.min(100, Math.max(0, v)) / 100) * (H - pad * 2);
    const yMem = (v) => H - pad - (memTotal ? Math.min(1, Math.max(0, v / memTotal)) : 0) * (H - pad * 2);

    const line = (arr, yf) => arr.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${yf(v).toFixed(1)}`).join(' ');
    const area = (arr, yf) => `${line(arr, yf)} L${W},${H} L0,${H} Z`;
    const last = pts[pts.length - 1];
    const mins = Math.round((n * step) / 60);

    const grid = [25, 50, 75].map((p) => {
      const y = (H - pad) - (p / 100) * (H - pad * 2);
      return `<line x1="0" y1="${y.toFixed(1)}" x2="${W}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="1" stroke-dasharray="4 6" vector-effect="non-scaling-stroke"/>`;
    }).join('');

    svg.innerHTML = `
      <defs>
        <linearGradient id="gHost" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="var(--primary)" stop-opacity=".30"/>
          <stop offset="100%" stop-color="var(--primary)" stop-opacity="0"/>
        </linearGradient>
      </defs>
      ${grid}
      <path d="${area(pts.map((p) => p.cpu), yCpu)}" fill="url(#gHost)"/>
      <path d="${line(pts.map((p) => p.mem), yMem)}" fill="none" stroke="var(--violet)" stroke-width="1.6" stroke-opacity=".8" vector-effect="non-scaling-stroke"/>
      <path d="${line(pts.map((p) => p.cpu), yCpu)}" fill="none" stroke="var(--primary)" stroke-width="2" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>
      <circle cx="${x(n - 1).toFixed(1)}" cy="${yCpu(last.cpu).toFixed(1)}" r="3" fill="var(--primary)" vector-effect="non-scaling-stroke"/>`;

    if (hint) {
      hint.textContent = `最近 ${mins || 1} 分钟 · 每 ${step} 秒采样 · CPU ${last.cpu.toFixed(1)}% · 内存 ${memTotal ? ((last.mem / memTotal) * 100).toFixed(1) : 0}%`;
    }
  }

  function skeletonView() {
    return `<div class="grid stats" style="margin-bottom:16px">${[1, 2, 3, 4].map(() => `<div class="skeleton" style="height:104px"></div>`).join('')}</div>
      <div class="skeleton" style="height:280px"></div>`;
  }

  /* =========================================================
     容器列表
     ========================================================= */
  function filtered() {
    const q = S.filter.q.trim().toLowerCase();
    return S.containers.filter((c) => {
      if (S.filter.state !== 'all' && stateClass(c.state) !== S.filter.state && !(S.filter.state === 'autostart' && c.autostart)) return false;
      if (!q) return true;
      return (c.name || '').toLowerCase().includes(q) || (c.os || '').toLowerCase().includes(q) || (c.ipv4 || []).join(' ').includes(q);
    });
  }

  // 容器 root 密码单元格：创建时自动生成的随机密码，直接显示，点一下即复制。
  function passCell(c) {
    if (!c.root_pass) return '<span class="muted">—</span>';
    return `<div class="passcell" onclick="event.stopPropagation()">
      <code class="pass" title="点击复制密码" onclick="window.__copyPass('${esc(c.name)}')">${esc(c.root_pass)}</code>
      <button class="btn icon xs" title="复制密码" onclick="window.__copyPass('${esc(c.name)}')">${icon('copy')}</button>
    </div>`;
  }

  function rowHtml(c) {
    const st = stateClass(c.state);
    const canTerm = c.state === 'RUNNING';
    const primary = st === 'running'
      ? `<button class="btn icon sm" title="停止" onclick="event.stopPropagation();window.__act('${esc(c.name)}','stop')">${icon('stop')}</button>
         <button class="btn icon sm" title="重启" onclick="event.stopPropagation();window.__act('${esc(c.name)}','restart')">${icon('restart')}</button>`
      : st === 'frozen'
        ? `<button class="btn icon sm" title="解冻" onclick="event.stopPropagation();window.__act('${esc(c.name)}','unfreeze')">${icon('play')}</button>`
        : `<button class="btn icon sm" title="启动" onclick="event.stopPropagation();window.__act('${esc(c.name)}','start')">${icon('play')}</button>`;
    const memPct = c.mem_limit ? Math.min(100, (c.mem_bytes / c.mem_limit) * 100) : 0;
    return `<tr onclick="window.__openContainer('${esc(c.name)}')">
      <td style="min-width:180px">
        <div class="cell-name">${esc(c.name)}</div>
        <div class="cell-sub"><span class="state ${st}"><i></i>${STATE_TEXT[c.state] || c.state}</span></div>
      </td>
      <td><div style="font-size:12.5px">${esc(c.os || '—')}</div><div class="cell-sub">${esc(c.arch || '')}${c.privileged ? ' · 特权' : ''}</div></td>
      <td class="mono muted">${c.ipv4 && c.ipv4.length ? esc(c.ipv4[0]) : '<span class="muted">—</span>'}${arr(c.ipv6).length ? `<div class="cell-sub mono" style="font-size:10.5px;color:var(--muted)">${esc(c.ipv6[0])}</div>` : ''}</td>
      <td>${passCell(c)}</td>
      <td class="right" style="min-width:96px">
        ${c.state === 'RUNNING'
        ? `<div class="mono" style="font-size:12.5px">${c.cpu_percent.toFixed(1)}%</div>
             <div class="bar thin" style="margin-top:4px;width:70px;margin-left:auto"><i style="width:${Math.min(100, c.cpu_percent).toFixed(0)}%"></i></div>`
        : '<span class="muted">—</span>'}
      </td>
      <td class="right" style="min-width:110px">
        ${c.state === 'RUNNING'
        ? `<div class="mono" style="font-size:12.5px">${fmtBytes(c.mem_bytes)}</div>
             ${c.mem_limit ? `<div class="bar thin" style="margin-top:4px;width:70px;margin-left:auto"><i style="width:${memPct.toFixed(0)}%"></i></div>` : ''}`
        : '<span class="muted">—</span>'}
      </td>
      <td class="nowrap">${c.autostart ? '<span class="pill ok">自启</span>' : '<span class="pill">手动</span>'}</td>
      <td style="width:1%">
        <div class="row-actions">
          ${primary}
          <button class="btn icon sm" title="终端" ${canTerm ? '' : 'disabled'} onclick="event.stopPropagation();window.__term('${esc(c.name)}')">${icon('term')}</button>
          <button class="btn icon sm" title="删除" onclick="event.stopPropagation();window.__del('${esc(c.name)}')">${icon('trash')}</button>
        </div>
      </td>
    </tr>`;
  }

  function renderContainers() {
    const list = filtered();
    $('#view').innerHTML = `
      <div class="toolbar">
        <div class="search">${icon('search')}<input id="fq" placeholder="搜索名称、系统或 IP" value="${esc(S.filter.q)}"></div>
        <select class="select" id="fstate">
          <option value="all">全部状态</option>
          <option value="running">运行中</option>
          <option value="stopped">已停止</option>
          <option value="frozen">已冻结</option>
          <option value="autostart">开机自启</option>
        </select>
        <div class="spacer"></div>
        <button class="btn" onclick="window.__refresh()">${icon('refresh')} 刷新</button>
        <button class="btn primary" onclick="window.__go('create')">${icon('plus')} 新建容器</button>
      </div>
      <div class="card">
        <div class="table-wrap">
          <table class="tbl">
            <thead><tr>
              <th>容器</th><th>系统</th><th>IP 地址</th><th>root 密码</th><th class="right">CPU</th><th class="right">内存</th><th>自启</th><th></th>
            </tr></thead>
            <tbody id="cbody"></tbody>
          </table>
        </div>
        <div id="cempty"></div>
      </div>
      <div class="muted" style="font-size:12px;margin-top:10px">共 ${S.containers.length} 个容器，显示 ${list.length} 个</div>`;

    $('#fq').oninput = (e) => { S.filter.q = e.target.value; patchContainerRows(); };
    $('#fstate').value = S.filter.state;
    $('#fstate').onchange = (e) => { S.filter.state = e.target.value; patchContainerRows(); };
    patchContainerRows();
  }

  function patchContainerRows() {
    const body = $('#cbody');
    if (!body) return;
    const list = filtered();
    body.innerHTML = list.map(rowHtml).join('');
    $('#cempty').innerHTML = list.length ? '' : `<div class="empty">${icon('search')}<h4>没有匹配的容器</h4><p>换个关键词，或直接创建一个新容器。</p></div>`;
  }

  /* =========================================================
     创建容器
     ========================================================= */
  // 发行版标识：图标取自 Simple Icons（CC0 1.0）收录的官方品牌图形，单色铺白在品牌色底板上。
  // 底色对偏亮的绿色/蓝色做了压暗，保证白色图形的对比度。
  const DISTRO_TILE = {
    debian: '#a80030', ubuntu: '#e95420', almalinux: '#0f4266', rockylinux: '#0a7d5c',
    centos: '#932279', alpine: '#0d597f', archlinux: '#0c74ab', fedora: '#294172',
    opensuse: '#4c8a16', kali: '#1b6fa8',
  };
  const DISTRO_LOGO = {
    debian: 'M13.88 12.685c-.4 0 .08.2.601.28.14-.1.27-.22.39-.33a3.001 3.001 0 01-.99.05m2.14-.53c.23-.33.4-.69.47-1.06-.06.27-.2.5-.33.73-.75.47-.07-.27 0-.56-.8 1.01-.11.6-.14.89m.781-2.05c.05-.721-.14-.501-.2-.221.07.04.13.5.2.22M12.38.31c.2.04.45.07.42.12.23-.05.28-.1-.43-.12m.43.12l-.15.03.14-.01V.43m6.633 9.944c.02.64-.2.95-.38 1.5l-.35.181c-.28.54.03.35-.17.78-.44.39-1.34 1.22-1.62 1.301-.201 0 .14-.25.19-.34-.591.4-.481.6-1.371.85l-.03-.06c-2.221 1.04-5.303-1.02-5.253-3.842-.03.17-.07.13-.12.2a3.551 3.552 0 012.001-3.501 3.361 3.362 0 013.732.48 3.341 3.342 0 00-2.721-1.3c-1.18.01-2.281.76-2.651 1.57-.6.38-.67 1.47-.93 1.661-.361 2.601.66 3.722 2.38 5.042.27.19.08.21.12.35a4.702 4.702 0 01-1.53-1.16c.23.33.47.66.8.91-.55-.18-1.27-1.3-1.48-1.35.93 1.66 3.78 2.921 5.261 2.3a6.203 6.203 0 01-2.33-.28c-.33-.16-.77-.51-.7-.57a5.802 5.803 0 005.902-.84c.44-.35.93-.94 1.07-.95-.2.32.04.16-.12.44.44-.72-.2-.3.46-1.24l.24.33c-.09-.6.74-1.321.66-2.262.19-.3.2.3 0 .97.29-.74.08-.85.15-1.46.08.2.18.42.23.63-.18-.7.2-1.2.28-1.6-.09-.05-.28.3-.32-.53 0-.37.1-.2.14-.28-.08-.05-.26-.32-.38-.861.08-.13.22.33.34.34-.08-.42-.2-.75-.2-1.08-.34-.68-.12.1-.4-.3-.34-1.091.3-.25.34-.74.54.77.84 1.96.981 2.46-.1-.6-.28-1.2-.49-1.76.16.07-.26-1.241.21-.37A7.823 7.824 0 0017.702 1.6c.18.17.42.39.33.42-.75-.45-.62-.48-.73-.67-.61-.25-.65.02-1.06 0C15.082.73 14.862.8 13.8.4l.05.23c-.77-.25-.9.1-1.73 0-.05-.04.27-.14.53-.18-.741.1-.701-.14-1.431.03.17-.13.36-.21.55-.32-.6.04-1.44.35-1.18.07C9.6.68 7.847 1.3 6.867 2.22L6.838 2c-.45.54-1.96 1.611-2.08 2.311l-.131.03c-.23.4-.38.85-.57 1.261-.3.52-.45.2-.4.28-.6 1.22-.9 2.251-1.16 3.102.18.27 0 1.65.07 2.76-.3 5.463 3.84 10.776 8.363 12.006.67.23 1.65.23 2.49.25-.99-.28-1.12-.15-2.08-.49-.7-.32-.85-.7-1.34-1.13l.2.35c-.971-.34-.57-.42-1.361-.67l.21-.27c-.31-.03-.83-.53-.97-.81l-.34.01c-.41-.501-.63-.871-.61-1.161l-.111.2c-.13-.21-1.52-1.901-.8-1.511-.13-.12-.31-.2-.5-.55l.14-.17c-.35-.44-.64-1.02-.62-1.2.2.24.32.3.45.33-.88-2.172-.93-.12-1.601-2.202l.15-.02c-.1-.16-.18-.34-.26-.51l.06-.6c-.63-.74-.18-3.102-.09-4.402.07-.54.53-1.1.88-1.981l-.21-.04c.4-.71 2.341-2.872 3.241-2.761.43-.55-.09 0-.18-.14.96-.991 1.26-.7 1.901-.88.7-.401-.6.16-.27-.151 1.2-.3.85-.7 2.421-.85.16.1-.39.14-.52.26 1-.49 3.151-.37 4.562.27 1.63.77 3.461 3.011 3.531 5.132l.08.02c-.04.85.13 1.821-.17 2.711l.2-.42M9.54 13.236l-.05.28c.26.35.47.73.8 1.01-.24-.47-.42-.66-.75-1.3m.62-.02c-.14-.15-.22-.34-.31-.52.08.32.26.6.43.88l-.12-.36m10.945-2.382l-.07.15c-.1.76-.34 1.511-.69 2.212.4-.73.65-1.541.75-2.362M12.45.12c.27-.1.66-.05.95-.12-.37.03-.74.05-1.1.1l.15.02M3.006 5.142c.07.57-.43.8.11.42.3-.66-.11-.18-.1-.42m-.64 2.661c.12-.39.15-.62.2-.84-.35.44-.17.53-.2.83',
    ubuntu: 'M17.61.455a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zM12.92.8C8.923.777 5.137 2.941 3.148 6.451a4.5 4.5 0 0 1 .26-.007 4.92 4.92 0 0 1 2.585.737A8.316 8.316 0 0 1 12.688 3.6 4.944 4.944 0 0 1 13.723.834 11.008 11.008 0 0 0 12.92.8zm9.226 4.994a4.915 4.915 0 0 1-1.918 2.246 8.36 8.36 0 0 1-.273 8.303 4.89 4.89 0 0 1 1.632 2.54 11.156 11.156 0 0 0 .559-13.089zM3.41 7.932A3.41 3.41 0 0 0 0 11.342a3.41 3.41 0 0 0 3.41 3.409 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41zm2.027 7.866a4.908 4.908 0 0 1-2.915.358 11.1 11.1 0 0 0 7.991 6.698 11.234 11.234 0 0 0 2.422.249 4.879 4.879 0 0 1-.999-2.85 8.484 8.484 0 0 1-.836-.136 8.304 8.304 0 0 1-5.663-4.32zm11.405.928a3.41 3.41 0 0 0-3.41 3.41 3.41 3.41 0 0 0 3.41 3.41 3.41 3.41 0 0 0 3.41-3.41 3.41 3.41 0 0 0-3.41-3.41z',
    almalinux: 'M23.994 15.133c.079 1.061-.668 1.927-1.69 2.005a1.8 1.8 0 0 1-1.928-1.651c-.078-1.062.63-1.849 1.691-1.967 1.023-.078 1.849.59 1.927 1.613zm-12.623 4.955c-.944 0-1.73.786-1.73 1.809 0 1.14.747 1.848 1.887 1.848.904-.04 1.691-.865 1.691-1.809 0-.983-.904-1.848-1.848-1.848zm1.061-9.675c-.039-.865-.078-1.73.08-2.556.156-.944.314-1.887.904-2.674.707-.983 1.809-.944 2.399.118.314.511.432 1.062.471 1.652 0 .354.158.432.472.393.944-.157 1.888-.157 2.792.197.118.039.236.118.394 0 .314-.276.393-1.652.196-2.006-.354-.63-.904-.55-1.455-.55-.629.039-1.18-.158-1.612-.67-.393-.471-.511-1.06-.59-1.65-.04-.276-.079-.512-.315-.709-.55-.55-1.809-.432-2.477.118-2.556 2.045-2.989 5.467-1.534 8.18.04.118.118.236.275.157zm7.984 3.658c.354-.511.865-.747 1.415-.983a.973.973 0 0 0 .59-.472c.354-.669-.078-1.81-.747-2.36-2.595-2.006-5.938-1.612-8.18.433-.118.078-.157.196-.078.314.786-.236 1.612-.472 2.477-.51.905-.08 1.848-.158 2.753.235 1.14.472 1.337 1.534.472 2.36-.393.393-.905.668-1.455.825-.315.08-.354.236-.236.551.354.865.59 1.77.472 2.753-.04.157-.079.275.078.393.354.236 1.691 0 1.967-.275.511-.472.314-1.023.196-1.534-.157-.63-.078-1.219.276-1.73zm-7.197-2.045c-.118-.079-.197-.118-.315 0 .472.708.905 1.455 1.259 2.241.314.866.668 1.73.55 2.714-.118 1.18-1.1 1.69-2.123 1.101-.511-.275-.905-.669-1.22-1.14-.196-.276-.393-.276-.629-.08-.747.63-1.533 1.102-2.516 1.26-.158 0-.315 0-.394.157-.118.393.472 1.612.826 1.809.59.354 1.062 0 1.534-.276.55-.314 1.101-.432 1.73-.236.59.197.983.63 1.337 1.102.158.196.315.353.63.432.747.197 1.77-.59 2.084-1.376 1.18-3.028-.157-6.135-2.753-7.708zm-2.556 2.438c.472-.669.826-1.416.983-2.202-.157-.04-.197.04-.315.078-.904.944-1.848 1.849-3.067 2.478-.472.236-.983.433-1.534.433-.865 0-1.376-.551-1.298-1.416a2.92 2.92 0 0 1 .787-1.849c.236-.275.236-.432-.04-.668-.786-.55-1.494-1.22-1.848-2.124-.078-.275-.275-.275-.51-.157a4.293 4.293 0 0 0-.434.236c-1.022.63-1.14 1.416-.275 2.28.63.63.944 1.338.708 2.203-.118.433-.354.747-.63 1.101a.95.95 0 0 0-.235.787c.079.747.826 1.494 1.73 1.573 2.517.236 4.562-.63 5.978-2.753zm-4.68-5.152c1.376 1.18 3.067 1.455 4.837 1.377.157 0 .315 0 .354-.118.04-.197-.157-.197-.275-.236-.826-.354-1.691-.63-2.438-1.14S6.848 8.25 6.534 7.266c-.236-.747.078-1.415.825-1.651.669-.236 1.337-.236 1.967 0 .393.157.55.078.629-.354.118-.747.354-1.455.826-2.085.55-.786.55-.865-.354-1.376-.04 0-.04-.04-.079-.04-.865-.471-1.534-.196-1.848.709-.472 1.376-1.377 1.887-2.832 1.612-.196-.04-.393-.079-.472-.079-.747.118-1.18.55-1.297 1.14-.158 1.81.786 3.107 2.084 4.17zm-2.32 3.658c-.079-.944-1.023-1.652-2.045-1.534-.905.079-1.691 1.022-1.613 1.966.08.983 1.023 1.77 1.967 1.652 1.14-.079 1.73-1.18 1.69-2.084zm15.18-8.298c.943-.079 1.73-.983 1.651-1.927-.078-.983-1.022-1.77-2.005-1.691-1.023.079-1.73.983-1.652 1.966s.983 1.73 2.006 1.652zm-12.27-.826c1.062-.157 1.77-1.023 1.652-2.045C8.107.897 7.163.149 6.18.267c-1.062.118-1.691.944-1.573 2.085.118.865 1.061 1.612 1.966 1.494z',
    rockylinux: 'M23.332 15.957c.433-1.239.668-2.57.668-3.957 0-6.627-5.373-12-12-12S0 5.373 0 12c0 3.28 1.315 6.251 3.447 8.417L15.62 8.245l3.005 3.005zm-2.192 3.819l-5.52-5.52L6.975 22.9c1.528.706 3.23 1.1 5.025 1.1 3.661 0 6.94-1.64 9.14-4.224z',
    centos: 'M12.076.066L8.883 3.28H3.348v5.434L0 12.01l3.349 3.298v5.39h5.374l3.285 3.236 3.285-3.236h5.43v-5.374L24 12.026l-3.232-3.252V3.321H15.31zm0 .749l2.49 2.506h-1.69v6.441l-.8.805-.81-.815V3.28H9.627zm-8.2 2.991h4.483L6.485 5.692l4.253 4.279v.654H9.94L5.674 6.423l-1.798 1.77zm5.227 0h1.635v5.415l-3.509-3.53zm4.302.043h1.687l1.83 1.842-3.517 3.539zm2.431 0h4.404v4.394l-1.83-1.842-4.241 4.267h-.764v-.69l4.261-4.287zm2.574 3.3l1.83 1.843v1.676h-5.327zm-12.735.013l3.515 3.462H3.876v-1.69zM3.348 9.454v1.697h6.377l.871.858-.782.77H3.35v1.786L.753 12.01zm17.42.068l2.488 2.503-2.533 2.55v-1.796h-6.41l-.75-.754.825-.83h6.38zm-9.502.978l.81.815.186-.188.614-.618v.686h.768l-.825.83.75.754h-.719v.808l-.842-.83-.741.73v-.707h-.7l.781-.77-.188-.186-.682-.672h.788zm-7.39 2.807h5.402l-3.603 3.55-1.798-1.772zm6.154 0h.708v.7l-4.404 4.338 1.852 1.824h-4.31v-4.342l1.798 1.77zm3.348 0h.715l4.317 4.343.186-.187 1.599-1.61v4.316h-4.366l1.853-1.825-.188-.185-4.116-4.054zm1.46 0h5.357v1.798l-1.785 1.796zm-2.83.191l.842.829v6.37h1.691l-2.532 2.495-2.533-2.495h1.79V14.23zm-1.27 1.251v5.42H8.939l-1.852-1.823zm2.64.097l3.552 3.499-1.853 1.825h-1.7z',
    alpine: 'M5.998 1.607L0 12l5.998 10.393h12.004L24 12 18.002 1.607H5.998zM9.965 7.12L12.66 9.9l1.598 1.595.002-.002 2.41 2.363c-.2.14-.386.252-.563.344a3.756 3.756 0 01-.496.217 2.702 2.702 0 01-.425.111c-.131.023-.25.034-.358.034-.13 0-.242-.014-.338-.034a1.317 1.317 0 01-.24-.072.95.95 0 01-.2-.113l-1.062-1.092-3.039-3.041-1.1 1.053-3.07 3.072a.974.974 0 01-.2.111 1.274 1.274 0 01-.237.073c-.096.02-.209.033-.338.033-.108 0-.227-.009-.358-.031a2.7 2.7 0 01-.425-.114 3.748 3.748 0 01-.496-.217 5.228 5.228 0 01-.563-.343l6.803-6.727zm4.72.785l4.579 4.598 1.382 1.353a5.24 5.24 0 01-.564.344 3.73 3.73 0 01-.494.217 2.697 2.697 0 01-.426.111c-.13.023-.251.034-.36.034-.129 0-.241-.014-.337-.034a1.285 1.285 0 01-.385-.146c-.033-.02-.05-.036-.053-.04l-1.232-1.218-2.111-2.111-.334.334L12.79 9.8l1.896-1.897zm-5.966 4.12v2.529a2.128 2.128 0 01-.356-.035 2.765 2.765 0 01-.422-.116 3.708 3.708 0 01-.488-.214 5.217 5.217 0 01-.555-.34l1.82-1.825Z',
    archlinux: 'M11.39.605C10.376 3.092 9.764 4.72 8.635 7.132c.693.734 1.543 1.589 2.923 2.554-1.484-.61-2.496-1.224-3.252-1.86C6.86 10.842 4.596 15.138 0 23.395c3.612-2.085 6.412-3.37 9.021-3.862a6.61 6.61 0 01-.171-1.547l.003-.115c.058-2.315 1.261-4.095 2.687-3.973 1.426.12 2.534 2.096 2.478 4.409a6.52 6.52 0 01-.146 1.243c2.58.505 5.352 1.787 8.914 3.844-.702-1.293-1.33-2.459-1.929-3.57-.943-.73-1.926-1.682-3.933-2.713 1.38.359 2.367.772 3.137 1.234-6.09-11.334-6.582-12.84-8.67-17.74zM22.898 21.36v-.623h-.234v-.084h.562v.084h-.234v.623h.331v-.707h.142l.167.5.034.107a2.26 2.26 0 01.038-.114l.17-.493H24v.707h-.091v-.593l-.206.593h-.084l-.205-.602v.602h-.091',
    fedora: 'M12.001 0C5.376 0 .008 5.369.004 11.992H.002v9.287h.002A2.726 2.726 0 0 0 2.73 24h9.275c6.626-.004 11.993-5.372 11.993-11.997C23.998 5.375 18.628 0 12 0zm2.431 4.94c2.015 0 3.917 1.543 3.917 3.671 0 .197.001.395-.03.619a1.002 1.002 0 0 1-1.137.893 1.002 1.002 0 0 1-.842-1.175 2.61 2.61 0 0 0 .013-.337c0-1.207-.987-1.672-1.92-1.672-.934 0-1.775.784-1.777 1.672.016 1.027 0 2.046 0 3.07l1.732-.012c1.352-.028 1.368 2.009.016 1.998l-1.748.013c-.004.826.006.677.002 1.093 0 0 .015 1.01-.016 1.776-.209 2.25-2.124 4.046-4.424 4.046-2.438 0-4.448-1.993-4.448-4.437.073-2.515 2.078-4.492 4.603-4.469l1.409-.01v1.996l-1.409.013h-.007c-1.388.04-2.577.984-2.6 2.47a2.438 2.438 0 0 0 2.452 2.439c1.356 0 2.441-.987 2.441-2.437l-.001-7.557c0-.14.005-.252.02-.407.23-1.848 1.883-3.256 3.754-3.256z',
    opensuse: 'M10.724 0a12 12 0 0 0-9.448 4.623c1.464.391 2.5.727 2.81.832.005-.19.037-1.893.037-1.893s.004-.04.025-.06c.026-.026.065-.018.065-.018.385.056 8.602 1.274 12.066 3.292.427.25.638.517.902.786.958.99 2.223 5.108 2.359 5.957.005.033-.036.07-.054.083a5.177 5.177 0 0 1-.313.228c-.82.55-2.708 1.872-5.13 1.656-2.176-.193-5.018-1.44-8.445-3.699.336.79.668 1.58 1 2.371.497.258 5.287 2.7 7.651 2.651 1.904-.04 3.941-.968 4.756-1.458 0 0 .179-.108.257-.048.085.066.061.167.041.27-.05.234-.164.66-.242.863l-.065.165c-.093.25-.183.482-.356.625-.48.436-1.246.784-2.446 1.305-1.855.812-4.865 1.328-7.66 1.31-1.001-.022-1.968-.133-2.817-.232-1.743-.197-3.161-.357-4.026.269A12 12 0 0 0 10.724 24a12 12 0 0 0 12-12 12 12 0 0 0-12-12zM13.4 6.963a3.503 3.503 0 0 0-2.521.942 3.498 3.498 0 0 0-1.114 2.449 3.528 3.528 0 0 0 3.39 3.64 3.48 3.48 0 0 0 2.524-.946 3.504 3.504 0 0 0 1.114-2.446 3.527 3.527 0 0 0-3.393-3.64zm-.03 1.035a2.458 2.458 0 0 1 2.368 2.539 2.43 2.43 0 0 1-.774 1.706 2.456 2.456 0 0 1-1.762.659 2.461 2.461 0 0 1-2.364-2.542c.02-.655.3-1.26.777-1.707a2.419 2.419 0 0 1 1.756-.655zm.402 1.23c-.602 0-1.087.325-1.087.727 0 .4.485.725 1.087.725.6 0 1.088-.326 1.088-.725 0-.402-.487-.726-1.088-.726Z',
    kali: 'M12.778 5.943s-1.97-.13-5.327.92c-3.42 1.07-5.36 2.587-5.36 2.587s5.098-2.847 10.852-3.008zm7.351 3.095l.257-.017s-1.468-1.78-4.278-2.648c1.58.642 2.954 1.493 4.021 2.665zm.42.74c.039-.068.166.217.263.337.004.024.01.039-.045.027-.005-.025-.013-.032-.013-.032s-.135-.08-.177-.137c-.041-.057-.049-.157-.028-.195zm3.448 8.479s.312-3.578-5.31-4.403a18.277 18.277 0 0 0-2.524-.187c-4.506.06-4.67-5.197-1.275-5.462 1.407-.116 3.087.643 4.73 1.408-.007.204.002.385.136.552.134.168.648.35.813.445.164.094.691.43 1.014.85.07-.131.654-.512.654-.512s-.14.003-.465-.119c-.326-.122-.713-.49-.722-.511-.01-.022-.015-.055.06-.07.059-.049-.072-.207-.13-.265-.058-.058-.445-.716-.454-.73-.009-.016-.012-.031-.04-.05-.085-.027-.46.04-.46.04s-.575-.283-.774-.893c.003.107-.099.224 0 .469-.3-.127-.558-.344-.762-.88-.12.305 0 .499 0 .499s-.707-.198-.82-.85c-.124.293 0 .469 0 .469s-1.153-.602-3.069-.61c-1.283-.118-1.55-2.374-1.43-2.754 0 0-1.85-.975-5.493-1.406-3.642-.43-6.628-.065-6.628-.065s6.45-.31 11.617 1.783c.176.785.704 2.094.989 2.723-.815.563-1.733 1.092-1.876 2.97-.143 1.878 1.472 3.53 3.474 3.58 1.9.102 3.214.116 4.806.942 1.52.84 2.766 3.4 2.89 5.703.132-1.709-.509-5.383-3.5-6.498 4.181.732 4.549 3.832 4.549 3.832zM12.68 5.663l-.15-.485s-2.484-.441-5.822-.204C3.37 5.211 0 6.38 0 6.38s6.896-1.735 12.68-.717Z',
  };
  // distroMark 生成一块品牌色底板 + 官方图形的标识，px 为底板边长。
  function distroMark(id, px) {
    const size = px || 32;
    const path = DISTRO_LOGO[id];
    const ico = Math.round(size * 0.6);
    const inner = path
      ? `<svg viewBox="0 0 24 24" width="${ico}" height="${ico}" aria-hidden="true"><path d="${path}" fill="#fff"/></svg>`
      : `<span style="font-size:${Math.round(size * 0.34)}px;font-weight:800;color:#fff">${esc(String(id || '?').slice(0, 2).toUpperCase())}</span>`;
    return `<div class="distro-mark" style="background:${DISTRO_TILE[id] || '#334155'};`
      + `width:${size}px;height:${size}px;border-radius:${Math.round(size * 0.3)}px">${inner}</div>`;
  }

  // 从容器 os-release 的 PRETTY_NAME 猜出发行版，好给详情页配上同一套官方标识。
  const OS_HINT = [
    [/kali/i, 'kali'], [/ubuntu/i, 'ubuntu'], [/debian/i, 'debian'], [/alma/i, 'almalinux'],
    [/rocky/i, 'rockylinux'], [/centos/i, 'centos'], [/alpine/i, 'alpine'],
    [/arch/i, 'archlinux'], [/fedora/i, 'fedora'], [/suse|tumbleweed|leap/i, 'opensuse'],
  ];
  function osDistro(os) {
    const t = String(os || '');
    for (const item of OS_HINT) if (item[0].test(t)) return item[1];
    return '';
  }

  async function renderCreate() {
    if (!S.distros) {
      $('#view').innerHTML = skeletonView();
      try {
        S.distros = await req('distros');
      } catch (e) {
        $('#view').innerHTML = `<div class="card card-pad">读取模板列表失败：${esc(e.message)}</div>`;
        return;
      }
    }
    if (!S.create) {
      const dd = S.distros.distros[0];
      S.create = {
        distro: dd.id, release: dd.default, name: 'lxc-' + dd.id + '-' + Math.random().toString(36).slice(2, 5),
        memory_mb: 1024, cpus: 1, autostart: false, server: (S.settings && S.settings.image_host) || S.distros.servers[0].host,
        start_after: true, network: 'nat', ipv6: 'off', disk_gb: 0,
      };
    }
    const c = S.create;
    const distros = S.distros.distros;
    const cur = distros.find((d) => d.id === c.distro) || distros[0];
    const ready = S.env && S.env.installed && S.env.has_download;

    $('#view').innerHTML = `
      ${ready ? '' : `<div class="card card-pad" style="margin-bottom:16px;background:var(--warning-soft);border-color:color-mix(in srgb,var(--warning) 40%,var(--border))">
        <b style="font-size:13px">${icon('alert')} 创建前需要先补全 LXC 运行环境</b>
        <div class="muted" style="font-size:12px;margin-top:4px">缺少 lxc 命令或 lxc-download 模板，前往「运行环境」一键安装后再回来。</div>
        <button class="btn sm" style="margin-top:10px" onclick="window.__go('env')">打开运行环境</button>
      </div>`}

      <div class="grid two">
        <div class="card card-pad">
          <div class="section-title">1 · 选择发行版</div>
          <div class="distro-grid">
            ${distros.map((d) => `
              <div class="distro ${d.id === c.distro ? 'active' : ''}" data-distro="${d.id}">
                ${distroMark(d.id, 32)}
                <div><div class="distro-name">${esc(d.name)}</div><div class="distro-sub">${d.releases.length} 个版本</div></div>
              </div>`).join('')}
          </div>

          <div class="section-title" style="margin-top:22px">2 · 版本</div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            ${arr(cur.releases).map((r) => {
              const rid = typeof r === 'string' ? r : r.id;
              const label = typeof r === 'string' ? r : (r.label || r.id);
              return `<button class="btn sm ${rid === c.release ? 'primary' : ''}" data-release="${esc(rid)}">${esc(label)}</button>`;
            }).join('')}
          </div>

          <div class="section-title" style="margin-top:22px">3 · 基础参数</div>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px">
            <label class="field"><span>容器名称</span>
              <input class="input" id="c-name" value="${esc(c.name)}" spellcheck="false">
              <div class="tip">仅限字母、数字、点、下划线、中划线</div>
            </label>
            <label class="field"><span>镜像源</span>
              <select class="select" id="c-server">
                ${S.distros.servers.map((s) => `<option value="${esc(s.host)}" ${s.host === c.server ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}
              </select>
              <div class="tip">国内网络建议选清华 / 中科大</div>
            </label>
            <label class="field"><span>内存上限</span>
              <select class="select" id="c-mem">
                ${[512, 1024, 2048, 4096, 8192].map((m) => `<option value="${m}" ${m === c.memory_mb ? 'selected' : ''}>${m >= 1024 ? m / 1024 + ' GB' : m + ' MB'}</option>`).join('')}
              </select>
            </label>
            <label class="field"><span>CPU 上限（核）</span>
              <select class="select" id="c-cpu">
                ${[0.5, 1, 2, 4, 8].map((v) => `<option value="${v}" ${v === c.cpus ? 'selected' : ''}>${v} 核</option>`).join('')}
              </select>
            </label>
            <label class="field" style="grid-column:1/-1"><span>磁盘容量</span>
              <select class="select" id="c-disk">
                ${[0, 2, 5, 10, 20, 50, 100].map((g) => `<option value="${g}" ${g === (c.disk_gb || 0) ? 'selected' : ''}>${g === 0 ? '不限制（与宿主机共用分区）' : g + ' GB'}</option>`).join('')}
              </select>
              <div class="tip">指定容量会创建一份 ext4 镜像（loop 存储），容器根分区即为该容量；不限制则 rootfs 直接落在宿主机分区上。</div>
            </label>
            <label class="field"><span>网络</span>
              <select class="select" id="c-net">
                <option value="nat" ${c.network === 'none' ? '' : 'selected'}>NAT 桥接（可出网）</option>
                <option value="none" ${c.network === 'none' ? 'selected' : ''}>无网络（仅 lo）</option>
              </select>
              <div class="tip">NAT 桥接走 lxcbr0，容器可访问外网；宿主机端口映射到容器请用左侧「端口转发」。</div>
            </label>
            <label class="field"><span>IPv6</span>
              <select class="select" id="c-ipv6">
                <option value="off" ${c.ipv6 === 'on' ? '' : 'selected'}>不启用</option>
                <option value="on" ${c.ipv6 === 'on' ? 'selected' : ''}>启用（自动分配内网地址）</option>
              </select>
              <div class="tip">分配 fd42:6c78:63::/64 私有地址，网关是宿主机桥接的 ::1；宿主机没有 IPv6 也能用。</div>
            </label>
          </div>

          <label class="switch" style="margin-bottom:10px">
            <input type="checkbox" id="c-auto" ${c.autostart ? 'checked' : ''}><span class="track"></span>
            <span style="font-size:13px">开机自启</span>
          </label>
          <label class="switch">
            <input type="checkbox" id="c-start" ${c.start_after ? 'checked' : ''}><span class="track"></span>
            <span style="font-size:13px">创建完成后立即启动</span>
          </label>

          <div style="display:flex;gap:8px;margin-top:22px">
            <button class="btn primary" id="c-submit" ${ready ? '' : 'disabled'}>${icon('plus')} 开始创建</button>
            <button class="btn" onclick="window.__go('containers')">取消</button>
          </div>
        </div>

        <div>
          <div class="card">
            <div class="card-head"><h3>创建进度</h3><span class="hint" id="taskHint">等待开始</span></div>
            <div class="card-pad">
              <pre class="log" id="createLog" style="margin:0;min-height:180px">选择模板后点击「开始创建」。\n首次创建会从镜像源下载 rootfs，通常需要 1-3 分钟。</pre>
            </div>
          </div>
          <div class="card card-pad" style="margin-top:14px">
            <div class="section-title">${icon('info')} 说明</div>
            <div class="muted" style="font-size:12.5px;line-height:1.8">
              · 使用 <span class="mono">lxc-create -t download</span> 拉取官方镜像<br>
              · 自动生成一个随机 root 密码，创建后显示在容器列表里，可复制、可随时改<br>
              · 内存 / CPU 上限写入 cgroup v2 配置，创建即生效<br>
              · 磁盘选“不限制”时 rootfs 落在宿主机分区；指定容量则创建独立 ext4 镜像（loop），容器内根分区即为该大小<br>
              · IPv6 启用后容器拿到 fd42:6c78:63::/64 内网地址，宿主机侧自动打开 IPv6 转发<br>
              · 创建日志会落盘到应用数据目录，便于排查失败原因
            </div>
          </div>
        </div>
      </div>`;

    $$('[data-distro]').forEach((el) => (el.onclick = () => {
      const id = el.dataset.distro;
      const d = S.distros.distros.find((x) => x.id === id);
      S.create.distro = id;
      S.create.release = d.default;
      S.create.name = 'lxc-' + id + '-' + Math.random().toString(36).slice(2, 5);
      renderCreate();
    }));
    $$('[data-release]').forEach((el) => (el.onclick = () => { S.create.release = el.dataset.release; renderCreate(); }));
    $('#c-name').oninput = (e) => { S.create.name = e.target.value; };
    $('#c-server').onchange = (e) => { S.create.server = e.target.value; };
    $('#c-mem').onchange = (e) => { S.create.memory_mb = +e.target.value; };
    $('#c-cpu').onchange = (e) => { S.create.cpus = +e.target.value; };
    $('#c-disk').onchange = (e) => { S.create.disk_gb = +e.target.value; };
    $('#c-net').onchange = (e) => { S.create.network = e.target.value; renderCreate(); };
    $('#c-ipv6').onchange = (e) => { S.create.ipv6 = e.target.value; renderCreate(); };
    $('#c-auto').onchange = (e) => { S.create.autostart = e.target.checked; };
    $('#c-start').onchange = (e) => { S.create.start_after = e.target.checked; };
    $('#c-submit').onclick = submitCreate;

    if (S.taskES) restoreTaskLog();
  }

  async function submitCreate() {
    const btn = $('#c-submit');
    btn.disabled = true;
    $('#taskHint').textContent = '提交中…';
    $('#createLog').textContent = '';
    try {
      const r = await req('containers', { method: 'POST', body: S.create });
      if (r.root_pass) {
        $('#createLog').textContent = '将自动生成 root 密码：' + r.root_pass + '\n可在容器列表查看/复制，也可在容器详情里修改。\n\n';
        toast('root 密码：' + r.root_pass, 'ok');
        if (S.create) S.create._pass = r.root_pass;
      }
      streamTask(r.task_id, $('#createLog'), $('#taskHint'));
    } catch (e) {
      btn.disabled = false;
      $('#taskHint').textContent = '创建失败';
      $('#createLog').textContent = e.message;
      toast(e.message, 'err');
    }
  }

  /* =========================================================
     任务日志流
     ========================================================= */
  function streamTask(id, logEl, hintEl) {
    if (S.taskES) { S.taskES.close(); S.taskES = null; }
    S.taskId = id;
    const es = new EventSource(api('tasks/' + id + '/stream'));
    S.taskES = es;
    if (hintEl) hintEl.textContent = '进行中…';
    es.onmessage = (ev) => {
      try {
        const o = JSON.parse(ev.data);
        if (o.line != null) {
          logEl.textContent += o.line + '\n';
          logEl.scrollTop = logEl.scrollHeight;
        }
      } catch (_) { /* ignore */ }
    };
    es.addEventListener('done', (ev) => {
      let st = 'success', err = '';
      try { const o = JSON.parse(ev.data); st = o.status; err = o.error || ''; } catch (_) { }
      es.close();
      S.taskES = null;
      if (hintEl) hintEl.textContent = st === 'success' ? '已完成' : '失败';
      if (st === 'success') {
        toast('操作完成', 'ok');
        const btn = $('#c-submit');
        if (btn) { btn.disabled = false; }
        setTimeout(() => { loadOverview(); if (S.view === 'create') renderCreate(); }, 600);
      } else {
        toast(err || '任务失败', 'err');
        const btn = $('#c-submit');
        if (btn) btn.disabled = false;
      }
    });
    es.onerror = () => { /* browser will retry; keep quiet */ };
  }

  function restoreTaskLog() {
    const logEl = $('#createLog');
    if (!logEl || !S.taskId) return;
    fetch(api('tasks/' + S.taskId)).then((r) => r.json()).then((o) => {
      if (o && o.output) {
        logEl.textContent = arr(o.output).join('\n') + '\n';
        logEl.scrollTop = logEl.scrollHeight;
      }
    }).catch(() => { });
  }

  /* =========================================================
     运行环境
     ========================================================= */
  function checkRow(ok, title, desc, level) {
    const cls = ok ? 'ok' : (level || 'bad');
    return `<div class="check-item">
      <div class="check-ico ${cls}">${icon(ok ? 'check' : cls === 'warn' ? 'alert' : 'x')}</div>
      <div class="check-txt"><b>${title}</b><span>${desc}</span></div>
    </div>`;
  }

  function renderEnv() {
    const e = S.env;
    if (!e) {
      $('#view').innerHTML = skeletonView();
      req('env').then((v) => { S.env = v; if (S.view === 'env') renderEnv(); })
        .catch((err) => { $('#view').innerHTML = `<div class="card card-pad">${esc(err.message)}</div>`; });
      return;
    }
    const needInstall = !e.installed || !e.has_download;
    $('#view').innerHTML = `
      <div class="grid two">
        <div class="card">
          <div class="card-head"><h3>环境自检</h3>
            <span class="hint">${arr(e.problems).length ? arr(e.problems).length + ' 项待处理' : '全部通过'}</span></div>
          <div class="card-pad">
            ${checkRow(e.installed, 'LXC 命令', e.installed ? `已检测到 lxc 工具集，版本 ${esc(e.version || '未知')}` : '未检测到 lxc-ls / lxc-info / lxc-create，需要安装', 'bad')}
            ${checkRow(e.has_download, 'download 模板', e.has_download ? '支持从官方镜像在线创建发行版容器' : '缺少 lxc-download，无法在线创建容器，请安装 lxc-templates', 'bad')}
            ${checkRow(e.state_writable, '容器存储目录', `${esc(e.state_dir)} ${e.state_writable ? '可写' : '不可写'}`, 'bad')}
            ${checkRow(e.cache_writable, '镜像缓存目录', `${esc(e.cache_dir || '')} ${e.cache_writable ? '可写' : '不可写'}`, 'bad')}
            ${checkRow(e.cgroup_version === 2, 'cgroup', e.cgroup_version === 2 ? 'cgroup v2 已挂载' + (e.cgroup_writable ? '且可写' : '，但当前不可写') : `cgroup v${e.cgroup_version}`, e.cgroup_writable ? 'ok' : 'warn')}
            ${checkRow(e.root, '运行权限', e.root ? '服务以 root 身份运行，可管理容器' : '当前非 root，LXC 无法正常工作', 'bad')}
            ${checkRow(!!e.pkg_manager, '包管理器', e.pkg_manager ? `检测到 ${esc(e.pkg_manager)}，可自动安装依赖` : '未找到 apt/dnf/apk，需要手动安装', 'warn')}
          </div>
        </div>

        <div>
          <div class="card card-pad">
            <div class="section-title">${icon('wrench')} 一键安装</div>
            <div class="muted" style="font-size:12.5px;line-height:1.7;margin-bottom:12px">
              执行 <span class="mono">${esc(e.pkg_manager ? installHint(e.pkg_manager) : '手动安装 lxc lxc-templates')}</span>。安装过程会实时输出日志，通常 30-90 秒完成。
            </div>
            <button class="btn primary" id="btnInstall" ${needInstall && e.pkg_manager ? '' : 'disabled'}>
              ${icon('layers')} ${needInstall ? '安装 LXC 运行环境' : '环境已就绪'}
            </button>
            <button class="btn" style="margin-left:8px" onclick="window.__refresh()">${icon('refresh')} 重新检测</button>
            <pre class="log" id="envLog" style="margin-top:14px;min-height:120px;display:none"></pre>
          </div>

          <div class="card card-pad" style="margin-top:14px">
            <div class="section-title">${icon('info')} 系统信息</div>
            <dl class="kv">
              <dt>系统</dt><dd>${esc(e.os_name || '—')}</dd>
              <dt>内核</dt><dd class="mono">${esc(e.kernel || '—')}</dd>
              <dt>架构</dt><dd>${esc(e.arch)}</dd>
              <dt>模板数量</dt><dd>${arr(e.templates).length} 个</dd>
              <dt>缓存目录</dt><dd class="mono" style="font-size:11.5px">${esc(e.cache_dir || '—')}</dd>
              <dt>容器数</dt><dd>${e.containers}</dd>
            </dl>
            ${arr(e.hints).length ? `<div class="muted" style="font-size:12px;margin-top:12px;line-height:1.7">${arr(e.hints).map((h) => '· ' + esc(h)).join('<br>')}</div>` : ''}
          </div>
        </div>
      </div>

      <div class="card" style="margin-top:14px">
        <div class="card-head"><h3>容器出网</h3><span class="hint" id="netHint">检测中…</span></div>
        <div class="card-pad" id="netBody"><div class="skeleton" style="height:90px"></div></div>
      </div>`;

    loadHostNet();

    const btn = $('#btnInstall');
    if (btn) btn.onclick = async () => {
      const logEl = $('#envLog');
      logEl.style.display = 'block';
      logEl.textContent = '';
      btn.disabled = true;
      try {
        const r = await req('env/install', { method: 'POST' });
        streamTask(r.task_id, logEl, null);
        const t = setInterval(async () => {
          await loadOverview();
          if (S.env && S.env.installed) { clearInterval(t); renderEnv(); }
        }, 3000);
      } catch (err) {
        toast(err.message, 'err');
        btn.disabled = false;
      }
    };
  }

  function installHint(pm) {
    if (pm === 'apt') return 'apt-get install -y lxc lxc-templates';
    if (pm === 'dnf') return 'dnf install -y lxc lxc-templates';
    if (pm === 'apk') return 'apk add lxc lxc-templates';
    return '';
  }

  /* ---------- 容器出网（宿主机侧） ---------- */
  async function loadHostNet(fix, proxy) {
    const body = $('#netBody');
    if (!body) return;
    try {
      let r;
      if (fix) {
        const opt = { method: 'POST' };
        if (proxy !== undefined) opt.body = { proxy };
        r = await req('hostnet', opt);
      } else {
        r = await req('hostnet');
      }
      const st = fix ? r.status : r;
      S.hostnet = st;
      const cur = (st && st.proxy) || '';
      const bad = arr(st && st.checks).filter((c) => !c.ok).length;
      const hint = $('#netHint');
      if (hint) {
        hint.textContent = bad === 0 ? '一切就绪' : (st && st.ok ? `有 ${bad} 项建议处理` : '有项目需要修复');
      }
      body.innerHTML = `
        <div class="net-head">
          <div>
            <div style="font-size:12.5px">网桥 <b class="mono">${esc((st && st.bridge) || 'lxcbr0')}</b> 网关 <b class="mono">${esc((st && st.gateway) || '10.0.3.1')}</b> 网段 <b class="mono">${esc((st && st.subnet) || '10.0.3.0/24')}</b></div>
            <div class="muted" style="font-size:12px;margin-top:4px">容器访问外网依赖：桥接地址 → 内核转发 → NAT 出网 → DHCP 分配地址，任一缺失就会 ping 不通。</div>
          </div>
          <button class="btn" id="btnNetFixAll">${icon('wrench')} 一键修复</button>
        </div>
        ${arr(st && st.checks).map((c) => checkRow(c.ok, esc(c.label), esc(c.detail) + (c.hint ? ' · ' + esc(c.hint) : ''), 'warn')).join('')}

        <div class="proxy-box">
          <div class="proxy-head">
            <div>
              <div style="font-size:12.5px;font-weight:600">容器出网代理${cur ? '<span class="pill ok" style="margin-left:8px">已启用</span>' : ''}</div>
              <div class="muted" style="font-size:12px;margin-top:3px">
                <b class="mono">ping</b> 通、<b class="mono">apt</b> 能下，但 <b class="mono">curl https://…</b> 报 <span class="mono">Connection reset</span> / <span class="mono">GnuTLS</span> 错误 —— 这是国际站点的 TLS 握手被重置，不是容器网络没配好。让容器走宿主机代理即可。
              </div>
            </div>
          </div>
          <div class="proxy-form">
            <input class="input mono" id="netProxy" spellcheck="false"
              placeholder="http://10.0.3.1:7890（留空 = 关闭代理）" value="${esc(cur)}">
            <button class="btn sm" id="btnProxySave">保存并应用</button>
          </div>
          <div class="muted" style="font-size:11.5px;line-height:1.7">
            宿主机跑 mihomo / clash 时：内核设置里打开 <span class="mono">allow-lan: true</span>，让它的 HTTP 端口监听在 <span class="mono">0.0.0.0:7890</span>，
            容器里填 <span class="mono">http://10.0.3.1:7890</span>（10.0.3.1 就是网桥网关）即可。<br>
            保存后会写入容器的 <span class="mono">/etc/environment</span>、<span class="mono">/etc/apt/apt.conf.d/99lxcpanel-proxy</span> 与 <span class="mono">/etc/profile.d/lxcpanel-proxy.sh</span>，
            之后新建和启动的容器都会自动带上。
          </div>
        </div>

        ${arr(st && st.tips).length ? `<div class="muted" style="font-size:12px;margin-top:12px;line-height:1.7">${arr(st.tips).map((t) => '· ' + esc(t)).join('<br>')}</div>` : ''}
        <pre class="log" id="netFixLog" style="display:${fix ? 'block' : 'none'};margin-top:12px;max-height:200px"></pre>`;
      const logEl = $('#netFixLog');
      if (fix && logEl) logEl.textContent = arr(r.logs).concat(r.error ? ['失败：' + r.error] : []).join('\n') || '完成';
      $('#btnNetFixAll').onclick = async () => {
        const b = $('#btnNetFixAll');
        b.disabled = true;
        b.innerHTML = '修复中…';
        await loadHostNet(true);
        toast('宿主机网络已检查并修复', 'ok');
      };
      const pSave = $('#btnProxySave');
      if (pSave) pSave.onclick = async () => {
        const val = ($('#netProxy').value || '').trim();
        pSave.disabled = true;
        pSave.textContent = '应用中…';
        await loadHostNet(true, val);
        toast(val ? '代理已保存并下发到运行中的容器' : '容器代理已关闭', 'ok');
      };
    } catch (e) {
      body.innerHTML = `<div class="empty">${icon('alert')}<h4>读取失败</h4><p>${esc(e.message)}</p></div>`;
    }
  }

  /* =========================================================
     设置 / API
     ========================================================= */
  async function renderSettings() {
    if (!S.settings) {
      $('#view').innerHTML = skeletonView();
      try { S.settings = await req('settings'); } catch (e) { $('#view').innerHTML = `<div class="card card-pad">${esc(e.message)}</div>`; return; }
    }
    const st = S.settings;
    const sys = S.data ? S.data.system : {};
    const sv = st.storage || {};
    const ca = st.cache || {};
    const host = (sys.hostname || 'nas') + ':' + st.port;
    const sample = `curl -s -H "X-API-Token: ${st.api_token}" \\\n  http://${host}/api/containers | jq`;

    $('#view').innerHTML = `
      <div class="grid two">
        <div>
          <div class="card">
            <div class="card-head"><h3>API 访问</h3><span class="hint">REST · JSON</span></div>
            <div class="card-pad">
              <label class="field"><span>API Token</span>
                <div style="display:flex;gap:8px">
                  <input class="input mono" id="tok" readonly value="${esc(st.api_token)}" style="flex:1">
                  <button class="btn" id="btnCopy">${icon('copy')} 复制</button>
                  <button class="btn danger" id="btnRotate">重置</button>
                </div>
                <div class="tip">带此 Token 可用 X-API-Token 请求头、Bearer 或 ?token= 参数调用全部接口。忘记 Token 时可在服务器执行 <code>sudo cat ${esc(st.token_file || '')}</code> 查看。</div>
              </label>

              <div class="section-title" style="margin-top:18px">示例</div>
              <div class="code">${esc(sample)}</div>
              <div class="code" style="margin-top:8px">${esc(`# 启动容器\ncurl -s -X POST -H "X-API-Token: ${st.api_token}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"action":"start"}' \\\n  http://${host}/api/containers/mybox/action`)}</div>

              <div class="section-title" style="margin-top:18px">主要接口</div>
              <div class="table-wrap"><table class="tbl">
                <thead><tr><th>方法</th><th>路径</th><th>说明</th></tr></thead>
                <tbody>
                  ${[
        ['GET', '/api/overview', '系统状态 + 容器列表'],
        ['GET', '/api/containers', '容器列表'],
        ['POST', '/api/containers', '创建容器（异步任务）'],
        ['POST', '/api/containers/{name}/action', 'start/stop/restart/freeze/unfreeze'],
        ['DELETE', '/api/containers/{name}', '删除容器（异步任务）'],
        ['GET', '/api/containers/{name}/logs', '容器日志'],
        ['PUT', '/api/containers/{name}/limits', '内存 / CPU / 自启'],
        ['GET', '/api/tasks/{id}/stream', '任务日志 SSE 流'],
        ['POST', '/api/containers/{name}/disk', '调整磁盘容量（异步任务）'],
        ['GET', '/api/containers/{name}/diag', '导出诊断报告（排查系统/内存/CPU 读取为空）'],
        ['POST', '/api/containers/{name}/ipv6', '开启 / 关闭容器内网 IPv6'],
        ['GET', '/api/portmap', '端口转发规则与宿主机转发状态'],
        ['POST', '/api/portmap', '新增端口转发（host_port → target_port）'],
        ['POST', '/api/portmap/sync', '按容器当前 IP 重建全部 iptables 规则'],
        ['DELETE', '/api/portmap/{id}', '删除端口转发规则'],
        ['POST', '/api/storage/switch', '切换容器 / 缓存存储位置（异步任务）'],
        ['GET', '/api/env', '环境自检结果'],
      ].map((r) => `<tr style="cursor:default"><td><span class="pill ${r[0] === 'GET' ? 'info' : r[0] === 'DELETE' ? 'bad' : 'ok'}">${r[0]}</span></td>
        <td class="mono">${r[1]}</td><td class="muted">${r[2]}</td></tr>`).join('')}
                </tbody></table></div>
            </div>
          </div>
        </div>

        <div>
          <div class="card card-pad" style="margin-bottom:14px">
            <div class="section-title">${icon('shield')} 账户安全</div>
            <label class="field"><span>原密码${st.has_password ? '' : '（尚未设置，可留空）'}</span>
              <input class="input" id="s-old" type="password" placeholder="当前管理密码" autocomplete="current-password">
            </label>
            <label class="field"><span>新密码</span>
              <input class="input" id="s-new" type="password" placeholder="至少 6 位" autocomplete="new-password">
            </label>
            <div style="display:flex;gap:8px">
              <button class="btn primary" id="btnPass">修改密码</button>
              <button class="btn" id="btnLogout">退出登录</button>
            </div>
            <div class="tip">密码以 PBKDF2-HMAC-SHA256 哈希保存在设备上，不会明文存储。忘记后可在服务器执行 <code>-set-pass</code> 重置。</div>
          </div>

          <div class="card card-pad">
            <div class="section-title">${icon('gear')} 面板设置</div>
            <label class="switch" style="margin:6px 0 12px">
              <input type="checkbox" id="s-trust" ${st.lan_trust ? 'checked' : ''}><span class="track"></span>
              <span style="font-size:13px">同一局域网内免密码登录</span>
            </label>
            <div class="tip" style="margin:-4px 0 14px">开启后，内网设备与飞牛窗口可直接打开面板；关闭后，除服务器本机外都需要输入管理密码。</div>
            <label class="switch" style="margin:6px 0 16px">
              <input type="checkbox" id="s-lan" ${st.allow_lan ? 'checked' : ''}><span class="track"></span>
              <span style="font-size:13px">允许局域网访问</span>
            </label>
            <label class="field"><span>默认镜像源</span>
              <select class="select" id="s-host">
                ${(st.servers || []).map((s) => `<option value="${esc(s.host)}" ${s.host === st.image_host ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}
              </select>
            </label>
            <label class="field"><span>监控采样间隔</span>
              <select class="select" id="s-sample">
                ${[5, 10, 30, 60].map((v) => `<option value="${v}" ${v === st.sample_secs ? 'selected' : ''}>${v} 秒</option>`).join('')}
              </select>
              <div class="tip">曲线图最多保留最近 360 个采样点</div>
            </label>
            <button class="btn primary" id="btnSave">保存设置</button>
          </div>

          <div class="card card-pad" style="margin-top:14px">
            <div class="section-title">${icon('disk')} 容器存储位置
              <span class="pill ${sv.custom ? 'ok' : 'info'}" style="margin-left:auto">${sv.custom ? '自定义' : '默认'}</span>
            </div>
            <dl class="kv" style="margin:0 0 12px">
              <dt>当前目录</dt><dd class="mono" style="font-size:11.5px">${esc(sv.dir || '')}</dd>
              <dt>分区使用</dt><dd>${sv.total ? fmtBytes(sv.total - sv.free) + ' / ' + fmtBytes(sv.total) : '未知'}</dd>
              <dt>容器数量</dt><dd>${sv.containers || 0} 个</dd>
            </dl>
            ${sv.legacy_count ? `<div class="tip" style="margin:0 0 12px;color:var(--danger)">旧目录 <span class="mono">${esc(sv.legacy_dir)}</span> 里还有 ${sv.legacy_count} 个容器，面板当前不显示它们，可迁移过来。</div>` : ''}
            <label class="field"><span>新的存储目录</span>
              <input class="input mono" id="s-store" placeholder="/vol1/lxc" autocomplete="off">
              <div class="tip">容器默认放在系统根分区的 <span class="mono">/var/lib/lxc</span>。系统盘偏小时可改到数据盘（如 <span class="mono">/vol1/lxc</span>）：面板会把已有容器一并迁移，并自动修正各容器配置里的 rootfs 路径。</div>
            </label>
            <div style="display:flex;gap:8px;flex-wrap:wrap">
              <button class="btn primary" id="btnStore">迁移并切换</button>
              <button class="btn ghost sm" id="btnStoreOnly">仅切换路径</button>
              ${sv.custom ? '<button class="btn ghost sm" id="btnStoreReset">恢复默认</button>' : ''}
            </div>
            <pre class="log" id="storeLog" style="display:none;margin-top:10px;max-height:180px"></pre>
          </div>

          <div class="card card-pad" style="margin-top:14px">
            <div class="section-title">${icon('layers')} 模板缓存位置
              <span class="pill ${ca.custom ? 'ok' : 'info'}" style="margin-left:auto">${ca.custom ? '自定义' : '默认'}</span>
            </div>
            <dl class="kv" style="margin:0 0 12px">
              <dt>当前目录</dt><dd class="mono" style="font-size:11.5px">${esc(ca.dir || '')}</dd>
              <dt>所在分区</dt><dd>${ca.total ? fmtBytes(ca.total - ca.free) + ' / ' + fmtBytes(ca.total) : '未知'}</dd>
              <dt>缓存体积</dt><dd>${ca.bytes ? fmtBytes(ca.bytes) : '0 B'}（${ca.entries || 0} 项）</dd>
            </dl>
            ${ca.legacy_count ? `<div class="tip" style="margin:0 0 12px;color:var(--danger)">默认目录 <span class="mono">${esc(ca.legacy_dir)}</span> 里还有 ${ca.legacy_count} 项缓存，新容器不会再用到它们，可以迁移过来。</div>` : ''}
            <label class="field"><span>新的缓存目录</span>
              <input class="input mono" id="s-cache" placeholder="/vol1/lxc-cache" autocomplete="off">
              <div class="tip">创建容器时 <span class="mono">lxc-create -t download</span> 会把镜像压缩包缓存在这里，默认 <span class="mono">/var/cache/lxc</span>（系统盘）。换到数据盘可省系统盘空间，换发行版或重建同类容器时仍能复用缓存。</div>
            </label>
            <div style="display:flex;gap:8px;flex-wrap:wrap">
              <button class="btn primary" id="btnCache">迁移并切换</button>
              <button class="btn ghost sm" id="btnCacheOnly">仅切换路径</button>
              ${ca.custom ? '<button class="btn ghost sm" id="btnCacheReset">恢复默认</button>' : ''}
            </div>
            <pre class="log" id="cacheLog" style="display:none;margin-top:10px;max-height:180px"></pre>
          </div>

          <div class="card card-pad" style="margin-top:14px">
            <div class="section-title">${icon('info')} 关于</div>
            <dl class="kv">
              <dt>面板版本</dt><dd>v${esc(st.version)}</dd>
              <dt>服务端口</dt><dd class="mono">${st.port}</dd>
              <dt>数据目录</dt><dd class="mono">${esc(st.data_dir)}</dd>
              <dt>主机</dt><dd class="mono">${esc(host)}</dd>
              <dt>前端组件</dt><dd>xterm.js 5.3.0（MIT）</dd>
            </dl>
            <div class="muted" style="font-size:12px;line-height:1.7;margin-top:12px">
              面板通过飞牛应用网关以 iframe 方式嵌入桌面窗口，容器操作全部调用宿主机 lxc 工具链。
            </div>
          </div>
        </div>
      </div>`;

    $('#btnCopy').onclick = async () => {
      try {
        await navigator.clipboard.writeText(st.api_token);
        toast('Token 已复制', 'ok');
      } catch (_) {
        $('#tok').select();
        document.execCommand('copy');
        toast('Token 已复制', 'ok');
      }
    };
    $('#btnRotate').onclick = async () => {
      if (!confirm('重置后旧 Token 立即失效，正在使用它的脚本需要更新。继续？')) return;
      try {
        const r = await req('token/rotate', { method: 'POST' });
        S.settings.api_token = r.api_token;
        renderSettings();
        toast('Token 已重置', 'ok');
      } catch (e) { toast(e.message, 'err'); }
    };
    $('#btnSave').onclick = async () => {
      try {
        await req('settings', {
          method: 'PUT', body: {
            allow_lan: $('#s-lan').checked,
            lan_trust: $('#s-trust').checked,
            image_host: $('#s-host').value,
            sample_secs: +$('#s-sample').value,
          }
        });
        S.settings.allow_lan = $('#s-lan').checked;
        S.settings.lan_trust = $('#s-trust').checked;
        S.settings.image_host = $('#s-host').value;
        S.settings.sample_secs = +$('#s-sample').value;
        toast('设置已保存', 'ok');
      } catch (e) { toast(e.message, 'err'); }
    };
    $('#btnPass').onclick = async () => {
      const oldPw = $('#s-old').value;
      const newPw = $('#s-new').value;
      if (newPw.length < 6) { toast('新密码至少 6 位', 'err'); return; }
      if (newPw !== newPw.trim()) { toast('密码首尾不要留空格', 'err'); return; }
      try {
        await req('auth/password', { method: 'POST', body: { old: oldPw, new: newPw } });
        $('#s-old').value = '';
        $('#s-new').value = '';
        S.settings.has_password = true;
        toast('管理密码已更新', 'ok');
        renderSettings();
      } catch (e) { toast(e.message, 'err'); }
    };
    $('#btnLogout').onclick = async () => {
      try { await req('auth/logout', { method: 'POST' }); } catch (_) { /* ignore */ }
      location.reload();
    };

    /* ---------- 容器存储位置 / 模板缓存位置 ---------- */
    const curDir = () => (st.storage && st.storage.dir) || '/var/lib/lxc';
    const curCacheDir = () => (st.cache && st.cache.dir) || '/var/cache/lxc';
    const storeBtn = $('#btnStore');
    const storeOnly = $('#btnStoreOnly');
    const storeLog = $('#storeLog');
    const cacheBtn = $('#btnCache');
    const cacheOnly = $('#btnCacheOnly');
    const cacheLog = $('#cacheLog');

    function lockStore(on) {
      [storeBtn, storeOnly, $('#btnStoreReset')].forEach((b) => { if (b) b.disabled = on; });
    }
    function lockCache(on) {
      [cacheBtn, cacheOnly, $('#btnCacheReset')].forEach((b) => { if (b) b.disabled = on; });
    }

    // kind: 'containers' | 'cache'
    async function switchDir(kind, path, migrate, logEl, lock, okText) {
      lock(true);
      if (logEl) { logEl.style.display = 'block'; logEl.textContent = ''; }
      const say = (line) => {
        if (!logEl) return;
        logEl.textContent += line + '\n';
        logEl.scrollTop = logEl.scrollHeight;
      };
      try {
        const body = { path: path, migrate: migrate };
        if (kind === 'cache') body.kind = 'cache';
        const r = await req('storage/switch', { method: 'POST', body: body });
        say('任务已提交，等待执行…');
        const es = new EventSource(api('tasks/' + r.task_id + '/stream'));
        es.onmessage = (ev) => {
          try { const o = JSON.parse(ev.data); if (o.line != null) say(o.line); } catch (_) { /* ignore */ }
        };
        es.addEventListener('done', (ev) => {
          es.close();
          let ok = true, err = '';
          try { const o = JSON.parse(ev.data); ok = o.status === 'success'; err = o.error || ''; } catch (_) { /* ignore */ }
          if (ok) {
            say('完成。');
            toast(okText, 'ok');
            S.settings = null;
            setTimeout(() => { renderSettings(); loadOverview(); }, 400);
          } else {
            toast('切换失败：' + err, 'err');
            lock(false);
          }
        });
        es.onerror = () => { /* 浏览器会自动重连 */ };
      } catch (e) {
        toast(e.message, 'err');
        lock(false);
      }
    }

    if (storeBtn) {
      storeBtn.onclick = () => {
        const to = ($('#s-store').value || '').trim();
        if (!to) { toast('请填写目标目录', 'err'); $('#s-store').focus(); return; }
        if (to === curDir()) { toast('与当前目录相同', 'err'); return; }
        const n = (st.storage && st.storage.containers) || 0;
        const msg = n
          ? '将把 ' + n + ' 个容器从\n' + curDir() + '\n迁移到\n' + to + '\n\n要求：所有容器已停止，目标分区空间充足。\n迁移期间请不要操作容器。继续？'
          : '当前没有容器，将直接把存储位置切换为\n' + to + '\n\n继续？';
        if (confirm(msg)) switchDir('containers', to, true, storeLog, lockStore, '容器存储位置已切换');
      };
    }
    if (storeOnly) {
      storeOnly.onclick = () => {
        const to = ($('#s-store').value || '').trim();
        if (!to) { toast('请填写目标目录', 'err'); $('#s-store').focus(); return; }
        if (to === curDir()) { toast('与当前目录相同', 'err'); return; }
        const msg = '仅切换路径，不移动任何文件。\n\n' + curDir() + ' 下的容器将不再显示在面板中'
          + '（数据不会丢失，把路径改回来即可恢复）。\n\n确认切换到 ' + to + '？';
        if (confirm(msg)) switchDir('containers', to, false, storeLog, lockStore, '容器存储位置已切换');
      };
    }
    if ($('#btnStoreReset')) {
      $('#btnStoreReset').onclick = () => {
        const to = (st.storage && st.storage.default) || '/var/lib/lxc';
        if (!confirm('将把容器迁移回默认目录\n' + to + '\n\n继续？')) return;
        switchDir('containers', to, true, storeLog, lockStore, '容器存储位置已恢复默认');
      };
    }

    if (cacheBtn) {
      cacheBtn.onclick = () => {
        const to = ($('#s-cache').value || '').trim();
        if (!to) { toast('请填写目标目录', 'err'); $('#s-cache').focus(); return; }
        if (to === curCacheDir()) { toast('与当前目录相同', 'err'); return; }
        const sz = (st.cache && st.cache.bytes) ? fmtBytes(st.cache.bytes) : '0 B';
        const msg = '将把镜像缓存从\n' + curCacheDir() + '\n迁移到\n' + to
          + '\n\n待迁移约 ' + sz + '，目标分区需有足够空间。缓存迁移不影响运行中的容器。继续？';
        if (confirm(msg)) switchDir('cache', to, true, cacheLog, lockCache, '模板缓存位置已切换');
      };
    }
    if (cacheOnly) {
      cacheOnly.onclick = () => {
        const to = ($('#s-cache').value || '').trim();
        if (!to) { toast('请填写目标目录', 'err'); $('#s-cache').focus(); return; }
        if (to === curCacheDir()) { toast('与当前目录相同', 'err'); return; }
        const msg = '仅切换缓存路径，不移动已有缓存。\n\n之后新下载的镜像写入 ' + to
          + '，' + curCacheDir() + ' 里的旧缓存不再复用。\n\n继续？';
        if (confirm(msg)) switchDir('cache', to, false, cacheLog, lockCache, '模板缓存位置已切换');
      };
    }
    if ($('#btnCacheReset')) {
      $('#btnCacheReset').onclick = () => {
        const to = (st.cache && st.cache.default) || '/var/cache/lxc';
        if (!confirm('将把镜像缓存迁回默认目录\n' + to + '\n\n继续？')) return;
        switchDir('cache', to, true, cacheLog, lockCache, '模板缓存位置已恢复默认');
      };
    }
  }

  /* =========================================================
     端口转发
     ========================================================= */
  async function renderPorts() {
    $('#view').innerHTML = skeletonView();
    let d;
    try {
      d = await req('portmap');
    } catch (e) {
      $('#view').innerHTML = `<div class="card card-pad">读取端口转发失败：${esc(e.message)}</div>`;
      return;
    }
    const rules = arr(d.rules);
    const st = d.status || {};
    const cs = (S.data && arr(S.data.containers)) || [];
    const badge = $('#navPorts');
    if (badge) badge.textContent = rules.length;
    const chip = (ok, on, off) => `<span class="pill ${ok ? 'ok' : 'warn'}">${esc(ok ? on : off)}</span>`;
    const hosts = arr(st.host_ipv4);
    const hosts6 = arr(st.host_ipv6);
    const hostPorts = rules.map((r) => r.host_port);
    const wrap6 = (ip) => (String(ip).includes(':') ? '[' + ip + ']' : ip);
    const addrRow = (ip) => `<div class="addr-row">
        <span class="mono addr-ip" data-copy="${esc(wrap6(ip))}" title="点击复制">${esc(wrap6(ip))}</span>
        <span class="addr-ports">${hostPorts.map((x) => `<span class="addr-port">:${x}</span>`).join('')}</span>
      </div>`;

    $('#view').innerHTML = `
      <div class="card card-pad" style="margin-bottom:14px">
        <div class="section-title">${icon('net')} 转发状态</div>
        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:10px">
          ${chip(st.iptables, 'iptables 可用', '缺少 iptables')}
          ${chip(st.ipv4_forward, 'IPv4 转发已开', 'IPv4 转发未开')}
          ${chip(st.ipv6_forward, 'IPv6 转发已开', 'IPv6 转发未开')}
          ${chip(st.out_chain, '本机访问已放行', '本机访问规则缺失')}
          ${chip(st.hairpin, 'NAT 回环已开', 'NAT 回环缺失')}
          ${hosts6.length ? chip(st.ip6tables, 'IPv6 端口转发可用', '缺少 ip6tables') : ''}
          <span class="pill">${rules.length} 条规则</span>
        </div>
        <div class="muted" style="font-size:12.5px;line-height:1.8">
          · DNAT 规则不绑定具体地址，命中条件是<span class="mono">目标是本机的任意地址</span>，
          所以下面这些地址配上宿主端口都能进；IPv4 与 IPv6 同时生成，容器 v4 / v6 地址各自转发<br>
          · 宿主机自己访问（<span class="mono">127.0.0.1</span> / 宿主 IP）走的是 OUTPUT 链，已单独放行，
          回程经 NAT 回环改写，不会出现「容器收到请求但客户端收不到响应」<br>
          · 容器启动、面板启动、点「重新应用」都会按容器当前 IP 重建规则，DHCP 换 IP 也能跟上
        </div>
        ${(hosts.length || hosts6.length) ? `<div class="addr-list">
          <div class="addr-head">可访问地址 <span class="muted" style="font-weight:400">（点地址复制，端口依次为 ${hostPorts.length ? esc(hostPorts.join(' / ')) : '—'}）</span></div>
          ${hosts.map(addrRow).join('')}
          ${hosts6.map(addrRow).join('')}
        </div>` : ''}
        <div style="display:flex;gap:8px;margin-top:12px">
          <button class="btn" id="pfSync">${icon('refresh')} 重新应用全部规则</button>
          <button class="btn ghost" id="pfRefresh">${icon('refresh')} 刷新列表</button>
        </div>
        <pre class="log" id="pfLog" style="display:none;margin-top:10px;max-height:170px"></pre>
      </div>

      <div class="card card-pad" style="margin-bottom:14px">
        <div class="section-title">${icon('plus')} 新增转发</div>
        <div style="display:grid;grid-template-columns:1.6fr .8fr 1fr 1fr auto;gap:10px;align-items:end">
          <label class="field" style="margin:0"><span>容器</span>
            <select class="select" id="pfContainer">
              ${cs.length ? cs.map((c) => `<option value="${esc(c.name)}">${esc(c.name)}</option>`).join('') : '<option value="">（还没有容器）</option>'}
            </select>
          </label>
          <label class="field" style="margin:0"><span>协议</span>
            <select class="select" id="pfProto"><option value="tcp">TCP</option><option value="udp">UDP</option></select>
          </label>
          <label class="field" style="margin:0"><span>宿主端口</span>
            <input class="input" id="pfHost" type="number" min="1" max="65535" placeholder="8080">
          </label>
          <label class="field" style="margin:0"><span>容器端口</span>
            <input class="input" id="pfTarget" type="number" min="1" max="65535" placeholder="80">
          </label>
          <button class="btn primary" id="pfAdd" style="height:38px" ${cs.length ? '' : 'disabled'}>添加</button>
        </div>
        <div class="tip" style="margin-top:8px">宿主端口重复会提示冲突；同端口不同协议（TCP / UDP）可以共存。</div>
      </div>

      <div class="card">
        <div class="card-head"><h3>规则列表</h3><span class="hint">共 ${rules.length} 条</span></div>
        <div class="table-wrap"><table class="tbl">
          <thead><tr><th>容器</th><th>协议</th><th class="right">宿主端口</th><th class="right">容器端口</th><th>转发到</th><th>状态</th><th></th></tr></thead>
          <tbody>
            ${rules.length ? rules.map((r) => {
              const targets = [];
              if (r.target_ip) targets.push(r.target_ip);
              if (r.target_v6) targets.push(r.target_v6);
              const okPill = r.state === '已启用'
                ? (r.target_ok
                    ? '<span class="pill slim ok" title="宿主机直连容器端口成功">服务在听</span>'
                    : `<span class="pill slim warn" title="${esc(r.reach || '')}">${esc(r.proto === 'udp' ? 'UDP 不探测' : '服务未监听')}</span>`)
                : '';
              return `<tr>
              <td><div class="cell-name">${esc(r.container)}</div></td>
              <td>${esc(String(r.proto || 'tcp').toUpperCase())}</td>
              <td class="right mono">${r.host_port}</td>
              <td class="right mono">${r.target_port}</td>
              <td class="mono muted">${targets.length ? targets.map((t) => `<div>${esc(t)}</div>`).join('') : '—'}</td>
              <td>${r.state === '已启用' ? '<span class="pill ok">已启用</span>' : `<span class="pill warn">${esc(r.state || '—')}</span>`} ${okPill}</td>
              <td style="width:1%;white-space:nowrap">
                <button class="btn icon sm" data-pfprobe="${esc(r.id)}" title="检测连通性">${icon('check')}</button>
                <button class="btn icon sm" data-pfdel="${esc(r.id)}" title="删除">${icon('trash')}</button>
              </td>
            </tr>`;
            }).join('') : `<tr><td colspan="7"><div class="empty">${icon('net')}<h4>还没有端口转发</h4><p>把宿主机端口映射到容器里的服务，例如宿主 8080 → 容器 80。</p></div></td></tr>`}
          </tbody>
        </table></div>
        <div class="probe-list" id="pfProbe" style="display:none;margin:0 18px 18px"></div>
      </div>`;

    const pfLog = $('#pfLog');
    const showLogs = (logs) => {
      if (!pfLog) return;
      const lines = arr(logs);
      if (!lines.length) { pfLog.style.display = 'none'; return; }
      pfLog.style.display = 'block';
      pfLog.textContent = lines.join('\n');
    };

    $('#pfRefresh').onclick = () => { loadOverview().then(() => renderPorts()); };
    $('#pfSync').onclick = async () => {
      $('#pfSync').disabled = true;
      try {
        const r = await req('portmap/sync', { method: 'POST' });
        showLogs(r.logs);
        if (r.error) toast('部分规则未生效：' + r.error, 'err');
        else toast('规则已重新应用', 'ok');
      } catch (e) { toast(e.message, 'err'); }
      $('#pfSync').disabled = false;
    };
    if ($('#pfAdd')) {
      $('#pfAdd').onclick = async () => {
        const body = {
          container: $('#pfContainer').value,
          proto: $('#pfProto').value,
          host_port: +$('#pfHost').value || 0,
          target_port: +$('#pfTarget').value || 0,
        };
        if (!body.container) { toast('请选择容器', 'err'); return; }
        if (!body.host_port || !body.target_port) { toast('端口不能为空', 'err'); return; }
        $('#pfAdd').disabled = true;
        try {
          const r = await req('portmap', { method: 'POST', body: body });
          showLogs(r.logs);
          if (r.ok === false) { toast('规则已保存，但生效失败：' + (r.error || ''), 'err'); }
          else { toast('已添加：' + body.host_port + ' → ' + body.target_port, 'ok'); }
          renderPorts();
        } catch (e) {
          toast(e.message, 'err');
          $('#pfAdd').disabled = false;
        }
      };
    }
    $$('[data-pfprobe]').forEach((el) => (el.onclick = async () => {
      const box = $('#pfProbe');
      const rule = rules.find((x) => x.id === el.dataset.pfprobe);
      box.style.display = 'flex';
      box.innerHTML = '<div class="muted" style="font-size:12px">正在从宿主机逐层探测：容器端口 → 本机回环 → 各网卡地址…</div>';
      el.disabled = true;
      try {
        const r = await req('portmap/' + encodeURIComponent(el.dataset.pfprobe) + '/probe', { method: 'POST' });
        const list = arr(r.probes);
        const head = rule ? `<div class="probe-head" style="padding:0 0 2px">${icon('net')} ${esc(rule.container)} · 宿主 ${rule.host_port} → 容器 ${rule.target_port}</div>` : '';
        const fail = list.filter((x) => !x.ok && x.scope !== 'skip');
        const tip = !fail.length
          ? '<div class="probe-hint">全部通过。如果外部设备仍连不上，检查宿主机防火墙（飞牛「安全」页）是否放行了该端口，以及客户端与宿主机是否在同一网段。</div>'
          : `<div class="probe-hint">断点：${esc(fail[0].label)}。${probeHint(fail)}</div>`;
        box.innerHTML = head + list.map((x) => `
          <div class="probe ${x.ok ? 'ok' : 'bad'}">
            <div class="probe-head">
              <span class="pill ${x.ok ? 'ok' : (x.scope === 'skip' ? 'warn' : 'bad')}">${x.ok ? '通过' : (x.scope === 'skip' ? '跳过' : '失败')}</span>
              <span>${esc(x.label)}</span>
            </div>
            <div class="probe-hint" style="margin-top:5px">${esc(x.detail || '')}</div>
          </div>`).join('') + tip;
      } catch (e) {
        box.innerHTML = `<div class="probe bad"><div class="probe-head"><span class="pill bad">失败</span><span>${esc(e.message)}</span></div></div>`;
      } finally {
        el.disabled = false;
      }
    }));

    $$('[data-pfdel]').forEach((el) => (el.onclick = async () => {
      if (!confirm('删除这条转发规则？容器里的服务不受影响。')) return;
      try {
        await req('portmap/' + encodeURIComponent(el.dataset.pfdel), { method: 'DELETE' });
        toast('已删除', 'ok');
        renderPorts();
      } catch (e) { toast(e.message, 'err'); }
    }));
  }

  /* 按第一个失败项判断断在哪一层，给出具体动作 */
  function probeHint(fails) {
    const scope = (fails[0] && fails[0].scope) || '';
    if (scope === 'container') return '容器里的服务没起来：进容器确认进程在跑、监听地址是 0.0.0.0 而不是 127.0.0.1。';
    if (scope === 'loopback') return '本机访问都不通，说明 DNAT 规则没生效：点「重新应用全部规则」，仍不行就查看下方日志里 iptables 的报错。';
    if (scope === 'host') return '容器通了但经宿主 IPv4 不通，通常是宿主机防火墙拦了 FORWARD；点「重新应用全部规则」会把 LXCPANEL 链顶到 FORWARD 第一行。';
    if (scope === 'host6') return 'IPv6 不通：确认宿主机这块网卡确实有公网 IPv6、容器已启用内网 IPv6，并检查 ip6tables 是否放行。';
    return '';
  }

  /* =========================================================
     容器详情抽屉
     ========================================================= */
  function openScrim(on) {
    $('#scrim').classList.toggle('show', on);
  }
  $('#scrim').onclick = () => {
    if ($('#modal').classList.contains('show')) closeTerm();
    else closeDrawer();
  };
  $('#drawerClose').onclick = closeDrawer;
  $('#modalClose').onclick = closeTerm;

  function closeDrawer() {
    $('#drawer').classList.remove('show');
    openScrim(false);
    S.drawer = null;
    if (S.drawerTimer) { clearInterval(S.drawerTimer); S.drawerTimer = null; }
  }

  async function openContainer(name) {
    S.drawer = name;
    // 抽屉每 5 秒自动刷新，重绘会清空这几个面板，缓存在 S 里好还原
    S.drawerAux = { name: name, cfg: '', diag: '', log: '', net: '', login: '' };
    $('#drawerTitle').innerHTML = `${esc(name)}`;
    $('#drawerBody').innerHTML = `<div class="skeleton" style="height:200px"></div>`;
    $('#drawerFoot').innerHTML = '';
    $('#drawer').classList.add('show');
    openScrim(true);
    await refreshDrawer();
    if (S.drawerTimer) clearInterval(S.drawerTimer);
    S.drawerTimer = setInterval(() => { if (S.drawer) refreshDrawer(); }, 5000);
  }

  // 把登录条件渲染成「到底哪一环拦住了」的清单。
  // 密码写进 /etc/shadow 只是第一步，sshd / PermitRootLogin / shell 任何一环不对都登不上。
  function renderLogin(st) {
    if (!st) return '';
    const chip = (ok, text) => `<span class="pill ${ok ? 'ok' : 'bad'}">${esc(text)}</span>`;
    const chips = [];
    chips.push(chip(st.hash_set && !st.locked,
      st.locked ? 'root 已锁定' : (st.hash_set ? '密码 ' + (st.hash_algo || '已设置') : 'root 无密码')));
    chips.push(chip(st.shell_ok, 'shell ' + (st.shell || '未知')));
    chips.push(chip(st.ssh_installed, st.ssh_installed ? 'sshd 已安装' : 'sshd 未安装'));
    if (st.ssh_installed) {
      chips.push(chip(st.ssh_running, st.ssh_running ? 'sshd 运行中' : 'sshd 没在跑'));
      chips.push(chip(st.permit_root_login === 'yes', 'PermitRootLogin ' + (st.permit_root_login || '未知')));
      chips.push(chip(st.password_auth, 'PasswordAuthentication ' + (st.password_auth ? 'yes' : 'no')));
    }
    const issues = arr(st.issues).map((s) => `
      <div class="login-issue ${st.ready ? 'ok' : 'bad'}">
        <span class="dot"></span><span>${esc(s)}</span>
      </div>`).join('');
    const tips = arr(st.tips).map((s) => `<div class="probe-hint">${esc(s)}</div>`).join('');
    const foot = st.ready ? '' :
      '<div class="probe-hint">密码写进去 ≠ 能登录。红色那几条才是真正拦住你的环节，「一键修好登录」会逐项补齐。</div>';
    return `<div class="login-chips">${chips.join('')}</div>${issues}${tips}${foot}`;
  }

  async function refreshDrawer() {
    const name = S.drawer;
    if (!name) return;
    // 避免轮询覆盖用户正在编辑的输入
    const ae = document.activeElement;
    if (ae && ae !== document.body && $('#drawerBody').contains(ae) &&
      (ae.tagName === 'INPUT' || ae.tagName === 'SELECT' || ae.tagName === 'TEXTAREA')) return;
    let c, pts = [];
    try {
      c = await req('containers/' + encodeURIComponent(name));
      const st = await req('containers/' + encodeURIComponent(name) + '/stats');
      pts = st.points || [];
    } catch (e) {
      $('#drawerBody').innerHTML = `<div class="empty">${icon('alert')}<h4>读取失败</h4><p>${esc(e.message)}</p></div>`;
      return;
    }
    const st = stateClass(c.state);
    const memPct = c.mem_limit ? Math.min(100, (c.mem_bytes / c.mem_limit) * 100) : 0;
    const last = pts.length ? pts[pts.length - 1] : null;

    const actions = [];
    if (c.state === 'RUNNING') {
      actions.push(['stop', 'stop', '停止'], ['restart', 'restart', '重启']);
    } else if (c.state === 'FROZEN') {
      actions.push(['unfreeze', 'play', '解冻'], ['stop', 'stop', '停止']);
    } else {
      actions.push(['start', 'play', '启动']);
    }

    // 抽屉每 5 秒重绘，这些面板的内容存在 S.drawerAux 里，重绘时还原
    const aux = (S.drawerAux && S.drawerAux.name === c.name) ? S.drawerAux : { cfg: '', diag: '', log: '', net: '', login: '' };

    $('#drawerBody').innerHTML = `
      <div style="display:flex;align-items:center;gap:12px;margin-bottom:16px">
        ${osDistro(c.os)
          ? distroMark(osDistro(c.os), 40)
          : `<div class="distro-mark" style="width:40px;height:40px;border-radius:12px;background:linear-gradient(145deg,var(--primary),var(--violet));font-size:13px">
          ${esc((c.name || '?').slice(0, 2).toUpperCase())}
        </div>`}
        <div style="flex:1;min-width:0">
          <div style="font-weight:650;font-size:15px">${esc(c.name)}</div>
          <div class="muted" style="font-size:12px">${esc(c.os || '未知系统')} ${c.arch ? '· ' + esc(c.arch) : ''}</div>
        </div>
        <span class="state ${st}"><i></i>${STATE_TEXT[c.state] || c.state}</span>
      </div>

      <div class="actions-grid" style="margin-bottom:18px">
        ${actions.filter((a, i, arr) => arr.findIndex((x) => x[0] === a[0]) === i).map(([act, ic, label]) =>
      `<button class="btn" onclick="window.__act('${esc(c.name)}','${act}',true)">${icon(ic)} ${label}</button>`).join('')}
        <button class="btn" ${c.state === 'RUNNING' ? '' : 'disabled'} onclick="window.__term('${esc(c.name)}')">${icon('term')} 终端</button>
        <button class="btn danger" onclick="window.__del('${esc(c.name)}',true)">${icon('trash')} 删除</button>
      </div>

      <div class="card card-pad" style="margin-bottom:14px;background:var(--surface-2)">
        <div class="stat-line"><span class="lbl">CPU 使用率</span><span class="val">${c.state === 'RUNNING' ? (last ? last.cpu.toFixed(2) : c.cpu_percent.toFixed(2)) + '%' : '—'}</span></div>
        <div class="stat-line"><span class="lbl">内存</span><span class="val">${fmtBytes(c.mem_bytes)}${c.mem_limit ? ' / ' + fmtBytes(c.mem_limit) : ''}</span></div>
        <div class="bar ${pctClass(memPct)}" style="margin:8px 0 14px"><i style="width:${memPct.toFixed(1)}%"></i></div>
        <div class="chart-wrap"><svg class="chart" id="chartCpu" viewBox="0 0 300 100" preserveAspectRatio="none"></svg></div>
        <div class="chart-legend"><span><i style="background:var(--primary)"></i>CPU %</span><span><i style="background:var(--violet)"></i>内存</span></div>
      </div>

      <div class="section-title">${icon('info')} 基本信息</div>
      <dl class="kv" style="margin-bottom:18px">
        <dt>IP 地址</dt><dd class="mono">${(c.ipv4 || []).join(', ') || '—'}</dd>
        <dt>PID</dt><dd>${c.pid || '—'}</dd>
        <dt>CPU 累计</dt><dd>${c.cpu_seconds ? c.cpu_seconds.toFixed(1) + ' 秒' : '—'}</dd>
        <dt>网络流量</dt><dd>↓ ${fmtBytes(c.rx_bytes)} · ↑ ${fmtBytes(c.tx_bytes)}</dd>
        <dt>根文件系统</dt><dd class="mono" style="font-size:11.5px">${esc(c.rootfs)}</dd>
        <dt>权限模式</dt><dd>${c.privileged ? '特权容器' : '非特权'}</dd>
        <dt>创建时间</dt><dd>${fmtDate(c.created_at)}</dd>
      </dl>

      <div class="section-title" style="margin-top:20px">${icon('shield')} root 密码</div>
      <div class="card card-pad" style="background:var(--surface-2);margin-bottom:14px">
        <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
          <code class="mono passbig" id="passText">${esc(c.root_pass || '未设置')}</code>
          <button class="btn sm" id="btnCopyPass" ${c.root_pass ? '' : 'disabled'}>${icon('copy')} 复制</button>
        </div>
        <div style="display:flex;gap:8px;margin-top:10px;flex-wrap:wrap">
          <input class="input" id="passNew" placeholder="新密码（至少 6 位）" style="flex:1;min-width:150px" spellcheck="false">
          <button class="btn sm" id="btnSetPass">${icon('check')} 保存</button>
          <button class="btn sm ghost" id="btnRandPass">${icon('refresh')} 随机生成</button>
        </div>
        <div style="display:flex;gap:8px;margin-top:8px;flex-wrap:wrap">
          <button class="btn sm ghost" id="btnLoginCheck">${icon('shield')} 检查登录</button>
          <button class="btn sm ghost" id="btnLoginFix">${icon('wrench')} 一键修好登录</button>
        </div>
        <div class="tip" style="margin-top:8px">创建容器时自动生成；改完立刻写进容器并回读 /etc/shadow 校验。容器没运行时会临时启动再停回。</div>
        <div class="login-box" id="loginBox" style="${aux.login ? '' : 'display:none'}">${aux.login || ''}</div>
      </div>

      <div class="section-title" style="margin-top:20px">${icon('net')} 网络</div>
      <dl class="kv" style="margin-bottom:10px">
        <dt>网卡</dt><dd>${c.net_off ? '未配置（无网络）' : 'veth → ' + esc(c.bridge || 'lxcbr0')}</dd>
        <dt>MAC</dt><dd class="mono">${esc(c.mac || '—')}</dd>
        <dt>IPv6</dt><dd class="mono">${arr(c.ipv6).join(', ') || (arr(c.ipv6_cfg).length ? '已配置（重启后生效）' : '未启用')}</dd>
      </dl>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        ${c.net_off ? '' : `<button class="btn sm" id="btnV6">${arr(c.ipv6_cfg).length ? '关闭 IPv6' : '启用内网 IPv6'}</button>`}
        ${c.net_off ? '' : `<button class="btn sm" id="btnNetDiag">${icon('net')} 网络诊断</button>`}
        ${c.net_off ? '' : `<button class="btn sm ghost" id="btnNetFix">${icon('wrench')} 一键修复出网</button>`}
        <button class="btn sm ghost" onclick="window.__go('ports')">${icon('net')} 端口转发</button>
      </div>
      <div class="tip" style="margin:10px 0 18px">ping 不通外网时先点「网络诊断」定位卡在哪一层：网卡无地址 → 宿主机没 DHCP；网关通但公网不通 → 缺 ip_forward 或 NAT。「一键修复出网」会自动补齐桥接、转发、NAT 和 DHCP。</div>
      <div class="probe-list" id="netLog" style="display:none"></div>
      <div class="tip" style="margin:0 0 18px">启用 IPv6 后分配 fd42:6c78:63::/64 内网地址，宿主机自动打开 IPv6 转发；改动需要重启容器生效。</div>
      <pre class="log" id="v6Log" style="display:none;margin-top:10px;max-height:150px"></pre>

      <div class="section-title">${icon('cpu')} 资源限制</div>
      <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:10px">
        <label class="field" style="margin:0"><span>内存（MB，0 = 不限）</span>
          <input class="input" id="lim-mem" type="number" min="0" step="128" value="${c.mem_limit ? Math.round(c.mem_limit / 1048576) : 0}">
        </label>
        <label class="field" style="margin:0"><span>CPU（核，0 = 不限）</span>
          <input class="input" id="lim-cpu" type="number" min="0" step="0.5" value="${c.cpu_limit || 0}">
        </label>
      </div>
      <label class="switch" style="margin-bottom:12px">
        <input type="checkbox" id="lim-auto" ${c.autostart ? 'checked' : ''}><span class="track"></span>
        <span style="font-size:13px">开机自动启动</span>
      </label>
      <button class="btn" id="btnLimits">${icon('check')} 保存并应用</button>

      <div class="section-title" style="margin-top:22px">${icon('disk')} 磁盘容量</div>
      <dl class="kv" style="margin-bottom:10px">
        <dt>存储方式</dt><dd>${c.disk_mode === 'loop' ? 'loop 镜像（独立 ext4）' : '目录（与宿主机共用分区）'}</dd>
        <dt>容量上限</dt><dd>${c.disk_mode === 'loop' && c.disk_bytes ? fmtBytes(c.disk_bytes) : '不限制'}</dd>
      </dl>
      <label class="field" style="margin:0 0 10px"><span>容量（GB，0 = 不限制）</span>
        <input class="input" id="disk-gb" type="number" min="0" max="1024" step="1"
          value="${c.disk_mode === 'loop' && c.disk_bytes ? Math.round(c.disk_bytes / 1073741824) : 0}">
        <div class="tip">需先停止容器。${c.disk_mode === 'loop' ? 'loop 镜像只能扩容，填 0 会把内容搬回目录并取消限制。' : '填大于 0 的容量会创建一块 ext4 镜像并把现有数据复制进去。'}${c.state === 'STOPPED' ? '' : '（当前容器正在运行，停止后才能调整）'}</div>
      </label>
      <button class="btn" id="btnDisk" ${c.state === 'STOPPED' ? '' : 'disabled'}>${icon('check')} 应用磁盘容量</button>
      <pre class="log" id="diskLog" style="display:none;margin-top:10px;max-height:160px"></pre>

      <div class="section-title" style="margin-top:22px">${icon('term')} 容器日志</div>
      <pre class="log" id="cLog" style="max-height:200px"><span class="muted">点击下方按钮加载</span></pre>
      <button class="btn sm" style="margin-top:8px" id="btnLoadLog">加载最近 200 行</button>
      <button class="btn sm ghost" id="btnRawCfg" style="margin-left:6px">查看原始配置</button>
      <button class="btn sm ghost" id="btnDiag" style="margin-left:6px" title="系统名、内存或 CPU 显示为空时，用这份报告定位原因">诊断信息</button>
      <pre class="log" id="cCfg" style="display:none;max-height:240px;margin-top:10px"></pre>
      <pre class="log" id="cDiag" style="display:none;max-height:300px;margin-top:10px"></pre>`;

    $('#drawerFoot').innerHTML = `<button class="btn" style="flex:1" onclick="window.__term('${esc(c.name)}')" ${c.state === 'RUNNING' ? '' : 'disabled'}>${icon('term')} 打开终端</button>
      <button class="btn ghost" onclick="window.__refreshDrawer()">${icon('refresh')} 刷新</button>`;

    drawChart($('#chartCpu'), pts);

    if (aux.log) { $('#cLog').textContent = aux.log; }
    if (aux.cfg) { $('#cCfg').style.display = 'block'; $('#cCfg').textContent = aux.cfg; }
    if (aux.diag) { $('#cDiag').style.display = 'block'; $('#cDiag').textContent = aux.diag; }
    if (aux.net) { $('#netLog').style.display = 'flex'; $('#netLog').innerHTML = aux.net; }

    $('#btnLimits').onclick = async () => {
      try {
        await req('containers/' + encodeURIComponent(c.name) + '/limits', {
          method: 'PUT', body: {
            memory_mb: +$('#lim-mem').value || 0,
            cpus: +$('#lim-cpu').value || 0,
            autostart: $('#lim-auto').checked,
          }
        });
        toast('已应用资源限制', 'ok');
        refreshDrawer();
      } catch (e) { toast(e.message, 'err'); }
    };
    $('#btnDisk').onclick = async () => {
      const gb = Math.max(0, Math.min(1024, Math.round(+$('#disk-gb').value || 0)));
      const curGb = c.disk_mode === 'loop' && c.disk_bytes ? Math.round(c.disk_bytes / 1073741824) : 0;
      if (gb === curGb) { toast('容量没有变化', 'err'); return; }
      const msg = gb === 0
        ? '将取消磁盘限制：把镜像里的内容复制回普通目录并删除镜像，数据保留。\n\n需要容器已停止，宿主分区要有足够剩余空间。继续？'
        : '将把容器磁盘设为 ' + gb + ' GB。\n\n'
        + (c.disk_mode === 'loop' ? 'loop 镜像只能扩容，不支持缩小。' : '会创建一份 ' + gb + ' GB 的 ext4 镜像，并把现有 rootfs 复制进去。')
        + '\n需要容器已停止，过程中请勿操作该容器。继续？';
      if (!confirm(msg)) return;
      const logEl = $('#diskLog');
      logEl.style.display = 'block';
      logEl.textContent = '';
      const say = (line) => { logEl.textContent += line + '\n'; logEl.scrollTop = logEl.scrollHeight; };
      $('#btnDisk').disabled = true;
      try {
        const r = await req('containers/' + encodeURIComponent(c.name) + '/disk', { method: 'POST', body: { size_gb: gb } });
        say('任务已提交，等待执行…');
        const es = new EventSource(api('tasks/' + r.task_id + '/stream'));
        es.onmessage = (ev) => {
          try { const o = JSON.parse(ev.data); if (o.line != null) say(o.line); } catch (_) { /* ignore */ }
        };
        es.addEventListener('done', (ev) => {
          es.close();
          let ok = true, err = '';
          try { const o = JSON.parse(ev.data); ok = o.status === 'success'; err = o.error || ''; } catch (_) { /* ignore */ }
          if (ok) {
            say('完成。');
            toast('磁盘容量已更新', 'ok');
            refreshDrawer();
          } else {
            toast('调整失败：' + err, 'err');
            const b = $('#btnDisk');
            if (b) b.disabled = false;
          }
        });
        es.onerror = () => { /* 浏览器会自动重连 */ };
      } catch (e) {
        toast(e.message, 'err');
        const b = $('#btnDisk');
        if (b) b.disabled = false;
      }
    };
    $('#btnLoadLog').onclick = async () => {
      try {
        const r = await req('containers/' + encodeURIComponent(c.name) + '/logs?lines=200');
        const txt = r.log || '（无日志）';
        if (S.drawerAux) S.drawerAux.log = txt;
        $('#cLog').textContent = txt;
        $('#cLog').scrollTop = $('#cLog').scrollHeight;
      } catch (e) { toast(e.message, 'err'); }
    };
    $('#btnRawCfg').onclick = async () => {
      try {
        const r = await req('containers/' + encodeURIComponent(c.name) + '/config');
        const el = $('#cCfg');
        el.style.display = 'block';
        el.textContent = r.config;
        if (S.drawerAux) S.drawerAux.cfg = r.config;
      } catch (e) { toast(e.message, 'err'); }
    };
    if ($('#btnV6')) {
      $('#btnV6').onclick = async () => {
        const enable = !arr(c.ipv6_cfg).length;
        const el = $('#v6Log');
        el.style.display = 'block';
        el.textContent = '处理中…';
        try {
          const r = await req('containers/' + encodeURIComponent(c.name) + '/ipv6', { method: 'POST', body: { enable: enable } });
          const logs = arr(r.logs);
          if (r.ok === false) {
            el.textContent = logs.concat(['失败：' + (r.error || '')]).join('\n');
            toast('操作失败：' + (r.error || ''), 'err');
            return;
          }
          el.textContent = logs.join('\n') || '完成';
          toast(enable ? 'IPv6 已配置' + (r.need_restart ? '，重启容器后生效' : '') : 'IPv6 已关闭（重启后生效）', 'ok');
          setTimeout(() => refreshDrawer(), 900);
        } catch (e) {
          el.textContent = '失败：' + e.message;
        }
      };
    }

    if ($('#btnCopyPass')) {
      $('#btnCopyPass').onclick = () => {
        if (c.root_pass) copyText(c.root_pass).then((ok) => toast(ok ? '密码已复制' : '复制失败，请手动选中', ok ? 'ok' : 'err'));
      };
    }
    if ($('#btnSetPass')) {
      const loginBox = $('#loginBox');
      const paintLogin = (html) => {
        if (!loginBox) return;
        loginBox.style.display = 'flex';
        loginBox.innerHTML = html;
        if (S.drawerAux) S.drawerAux.login = html;
      };

      const checkLogin = async (silent) => {
        paintLogin('<div class="muted" style="font-size:12px">正在容器内检查密码、登录 shell 和 sshd …</div>');
        try {
          const st = await req('containers/' + encodeURIComponent(c.name) + '/login');
          paintLogin(renderLogin(st));
          if (!silent && st && !st.ready) {
            toast(`有 ${arr(st.issues).length} 项拦着登录，看下面的清单`, 'info');
          }
          return st;
        } catch (e) {
          paintLogin(`<div class="probe bad"><div class="probe-head"><span class="pill bad">检查失败</span><span>${esc(e.message)}</span></div></div>`);
          return null;
        }
      };

      const fixLogin = async (install) => {
        if (install) {
          paintLogin('<pre class="log" style="max-height:180px;margin:0"></pre>');
          const pre = loginBox ? loginBox.querySelector('pre') : null;
          const say = (line) => { if (pre) { pre.textContent += line + '\n'; pre.scrollTop = pre.scrollHeight; } };
          try {
            const r = await req('containers/' + encodeURIComponent(c.name) + '/login', { method: 'POST', body: { install: true } });
            say('开始安装 openssh-server，可能要一两分钟…');
            const es = new EventSource(api('tasks/' + r.task_id + '/stream'));
            es.onmessage = (ev) => {
              try { const o = JSON.parse(ev.data); if (o.line != null) say(o.line); } catch (_) { /* ignore */ }
            };
            es.addEventListener('done', (ev) => {
              es.close();
              let ok = true, err = '';
              try { const o = JSON.parse(ev.data); ok = o.status === 'success'; err = o.error || ''; } catch (_) { /* ignore */ }
              toast(ok ? 'SSH 已安装并开启' : '安装失败：' + err, ok ? 'ok' : 'err');
              checkLogin(true);
            });
            es.onerror = () => { /* 浏览器会自动重连 */ };
          } catch (e) {
            paintLogin(`<div class="probe bad"><div class="probe-head"><span class="pill bad">失败</span><span>${esc(e.message)}</span></div></div>`);
          }
          return;
        }
        paintLogin('<div class="muted" style="font-size:12px">正在修复：登录 shell、root 锁定、sshd 配置与启动 …</div>');
        try {
          const r = await req('containers/' + encodeURIComponent(c.name) + '/login', { method: 'POST', body: { install: false } });
          paintLogin(renderLogin(r.status));
          if (r.need_install) toast('容器里还没有 sshd，需要安装一次', 'info');
          else toast(r.ok ? '登录条件已修复' : '还有问题：' + (r.error || ''), r.ok ? 'ok' : 'err');
        } catch (e) {
          paintLogin(`<div class="probe bad"><div class="probe-head"><span class="pill bad">失败</span><span>${esc(e.message)}</span></div></div>`);
        }
      };

      if ($('#btnLoginCheck')) $('#btnLoginCheck').onclick = () => checkLogin(false);
      if ($('#btnLoginFix')) {
        $('#btnLoginFix').onclick = async () => {
          const st = await checkLogin(true);
          if (st && st.need_install) {
            if (!confirm('容器里没有 openssh-server，现在安装吗？\n\n需要容器能访问软件源（apt / apk / dnf），装完会自动允许 root 密码登录。')) return;
            fixLogin(true);
            return;
          }
          fixLogin(false);
        };
      }

      const applyPass = async (body, okMsg) => {
        const btn = $('#btnSetPass');
        btn.disabled = true;
        $('#btnSetPass').innerHTML = '写入中…';
        try {
          const r = await req('containers/' + encodeURIComponent(c.name) + '/password', { method: 'PUT', body });
          const w = r.write || {};
          if (w.verified) {
            toast(okMsg + '：' + r.password + '（已写入 /etc/shadow 并校验，' + (w.algo || '') + '）', 'ok');
          } else {
            toast(okMsg + '：' + r.password, 'ok');
          }
          if ($('#passText')) { $('#passText').textContent = r.password; }
          if ($('#btnCopyPass')) $('#btnCopyPass').disabled = false;
          // 写进去了不等于登得上：直接把登录条件摊开
          if (r.login) paintLogin(renderLogin(r.login));
          else await checkLogin(true);
          if (w.locked) toast('注意：root 仍处于锁定状态，点「一键修好登录」解开', 'err');
          await loadOverview();
          refreshDrawer();
        } catch (e) {
          toast('修改失败：' + e.message, 'err');
          btn.disabled = false;
          btn.innerHTML = icon('check') + ' 保存';
        }
      };
      $('#btnSetPass').onclick = () => {
        const v = ($('#passNew').value || '').trim();
        if (v.length < 6) { toast('密码至少 6 位', 'err'); return; }
        applyPass({ password: v }, '密码已更新');
      };
      $('#btnRandPass').onclick = () => applyPass({ random: true }, '已生成随机密码');
    }

    if ($('#btnNetDiag')) {
      const paintProbes = (probes) => {
        const el = $('#netLog');
        el.style.display = 'flex';
        const html = arr(probes).map((p) => `
          <div class="probe ${p.ok ? 'ok' : 'bad'}">
            <div class="probe-head">
              <span class="pill ${p.ok ? 'ok' : 'bad'}">${p.ok ? '通过' : '异常'}</span>
              <span>${esc(p.label)}</span>
            </div>
            <pre class="probe-out">${esc(p.output)}</pre>
            ${p.hint && !p.ok ? `<div class="probe-hint">${esc(p.hint)}</div>` : ''}
          </div>`).join('');
        el.innerHTML = html || '<div class="muted" style="font-size:12px">没有采集到结果</div>';
        if (S.drawerAux) S.drawerAux.net = el.innerHTML;
      };
      $('#btnNetDiag').onclick = async () => {
        const el = $('#netLog');
        el.style.display = 'flex';
        el.innerHTML = '<div class="muted" style="font-size:12px">正在容器内检查（网卡、路由、DNS、网关、公网、HTTPS）…</div>';
        try {
          const r = await req('containers/' + encodeURIComponent(c.name) + '/netdiag');
          paintProbes(r.probes);
        } catch (e) {
          el.innerHTML = `<div class="probe bad"><div class="probe-head"><span class="pill bad">失败</span><span>${esc(e.message)}</span></div></div>`;
        }
      };
    }
    if ($('#btnNetFix')) {
      $('#btnNetFix').onclick = async () => {
        const el = $('#netLog');
        const btn = $('#btnNetFix');
        btn.disabled = true;
        el.style.display = 'flex';
        el.innerHTML = '<div class="muted" style="font-size:12px">正在修复宿主机网络（桥接 / 转发 / NAT / MSS / DHCP）…</div>';
        try {
          const r = await req('containers/' + encodeURIComponent(c.name) + '/netfix', { method: 'POST' });
          const lines = arr(r.logs);
          if (r.error) lines.push('失败：' + r.error);
          el.innerHTML = `<pre class="log" style="margin:0">${esc(lines.join('\n') || '完成')}</pre>`;
          if (S.drawerAux) S.drawerAux.net = el.innerHTML;
          toast(r.error ? '修复未完成：' + r.error : '宿主机网络已修复，可再次诊断验证', r.error ? 'err' : 'ok');
        } catch (e) {
          el.innerHTML = `<pre class="log" style="margin:0">修复失败：${esc(e.message)}</pre>`;
        } finally {
          btn.disabled = false;
        }
      };
    }

    $('#btnDiag').onclick = async () => {
      const el = $('#cDiag');
      el.style.display = 'block';
      el.textContent = '正在采集…';
      let txt;
      try {
        const r = await req('containers/' + encodeURIComponent(c.name) + '/diag');
        txt = r.text;
      } catch (e) {
        txt = '采集失败：' + e.message;
      }
      if (S.drawerAux) S.drawerAux.diag = txt;
      el.textContent = txt;
    };
  }

  function drawChart(svg, pts) {
    if (!svg) return;
    if (!pts || pts.length < 2) {
      svg.innerHTML = `<text x="150" y="52" text-anchor="middle" fill="var(--muted)" font-size="7">采集中，稍后显示曲线</text>`;
      return;
    }
    const n = pts.length;
    const maxCpu = Math.max(10, ...pts.map((p) => p.cpu));
    const maxMem = Math.max(1, ...pts.map((p) => p.mem));
    const x = (i) => (i / (n - 1)) * 300;
    const yCpu = (v) => 100 - (v / maxCpu) * 92 - 4;
    const yMem = (v) => 100 - (v / maxMem) * 92 - 4;

    const line = (arr, yf) => arr.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${yf(v).toFixed(1)}`).join(' ');
    const area = (arr, yf) => `${line(arr, yf)} L300,100 L0,100 Z`;

    svg.innerHTML = `
      <defs>
        <linearGradient id="gCpu" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="var(--primary)" stop-opacity=".28"/>
          <stop offset="100%" stop-color="var(--primary)" stop-opacity="0"/>
        </linearGradient>
      </defs>
      <path d="${area(pts.map((p) => p.cpu), yCpu)}" fill="url(#gCpu)"/>
      <path d="${line(pts.map((p) => p.mem), yMem)}" fill="none" stroke="var(--violet)" stroke-width="1.2" stroke-opacity=".55" vector-effect="non-scaling-stroke"/>
      <path d="${line(pts.map((p) => p.cpu), yCpu)}" fill="none" stroke="var(--primary)" stroke-width="1.8" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>`;
  }

  /* =========================================================
     操作
     ========================================================= */
  async function act(name, action, drawer) {
    const label = { start: '启动', stop: '停止', restart: '重启', freeze: '冻结', unfreeze: '解冻', kill: '强制停止' }[action] || action;
    if (action === 'stop' || action === 'restart') {
      if (!confirm(`确认${label}容器 ${name}？`)) return;
    }
    toast(`${label}中…`, 'info');
    try {
      await req('containers/' + encodeURIComponent(name) + '/action', { method: 'POST', body: { action } });
      toast(`${name} ${label}成功`, 'ok');
      await loadOverview();
      if (drawer && S.drawer === name) refreshDrawer();
    } catch (e) {
      toast(`${label}失败：` + e.message, 'err');
    }
  }

  async function del(name, drawer) {
    if (!confirm(`删除容器 ${name}？\n\n容器内的全部数据将被永久删除，此操作不可恢复。`)) return;
    try {
      const r = await req('containers/' + encodeURIComponent(name), { method: 'DELETE' });
      toast('已开始删除…', 'info');
      if (drawer) closeDrawer();
      const es = new EventSource(api('tasks/' + r.task_id + '/stream'));
      es.addEventListener('done', (ev) => {
        es.close();
        let ok = true, err = '';
        try { const o = JSON.parse(ev.data); ok = o.status === 'success'; err = o.error || ''; } catch (_) { }
        if (ok) { toast('容器已删除', 'ok'); loadOverview(); }
        else toast('删除失败：' + err, 'err');
      });
    } catch (e) { toast(e.message, 'err'); }
  }

  /* =========================================================
     终端
     ========================================================= */
  async function openTerm(name) {
    if (S.term) closeTerm(true);
    if (!window.Terminal) { toast('终端组件加载失败', 'err'); return; }
    $('#modalTitle').innerHTML = `${icon('term')} ${esc(name)} · 终端`;
    $('#modal').classList.add('show');
    openScrim(true);
    $('#termHost').innerHTML = '';

    const term = new window.Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      theme: {
        background: '#0b0f17', foreground: '#dbe3f0', cursor: '#5b8cff',
        selectionBackground: 'rgba(91,140,255,.32)',
      },
      allowProposedApi: true,
    });
    const fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open($('#termHost'));
    fit.fit();
    term.focus();

    let sessId = null;
    let es = null;
    const enc = new TextEncoder();

    try {
      const r = await req('containers/' + encodeURIComponent(name) + '/terminal', { method: 'POST' });
      sessId = r.id;
    } catch (e) {
      term.writeln('\r\n\x1b[31m无法打开终端：' + e.message + '\x1b[0m');
      term.writeln('\x1b[90m请确认容器处于运行状态。\x1b[0m');
      S.term = { term, fit, es: null, sessId: null, name };
      return;
    }

    es = new EventSource(api('term/' + sessId + '/stream'));
    es.onmessage = (ev) => {
      try {
        const o = JSON.parse(ev.data);
        if (!o.d) return;
        const bin = atob(o.d);
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        term.write(bytes);
      } catch (_) { /* ignore */ }
    };
    es.addEventListener('end', () => {
      term.writeln('\r\n\x1b[90m[会话已结束]\x1b[0m');
    });

    term.onData((data) => {
      if (!sessId) return;
      const bytes = enc.encode(data);
      let bin = '';
      for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
      fetch(api('term/' + sessId + '/input'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ d: btoa(bin) }),
      }).catch(() => { });
    });

    const doResize = () => {
      try {
        fit.fit();
        if (sessId) {
          fetch(api('term/' + sessId + '/resize'), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cols: term.cols, rows: term.rows }),
          }).catch(() => { });
        }
      } catch (_) { }
    };
    const ro = new ResizeObserver(() => doResize());
    ro.observe($('#termHost'));
    setTimeout(doResize, 120);

    S.term = { term, fit, es, sessId, name, ro };
  }

  function closeTerm(silent) {
    const t = S.term;
    S.term = null;
    $('#modal').classList.remove('show');
    if (!silent) openScrim(false);
    if (!t) return;
    try { t.ro && t.ro.disconnect(); } catch (_) { }
    try { t.es && t.es.close(); } catch (_) { }
    if (t.sessId) fetch(api('term/' + t.sessId), { method: 'DELETE' }).catch(() => { });
    try { t.term.dispose(); } catch (_) { }
    $('#termHost').innerHTML = '';
  }

  /* =========================================================
     polling / boot
     ========================================================= */
  function startPolling() {
    if (S.pollTimer) clearInterval(S.pollTimer);
    S.pollTimer = setInterval(() => {
      if (document.hidden) return;
      loadOverview();
    }, 4000);
  }

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if ($('#modal').classList.contains('show')) closeTerm();
      else if ($('#drawer').classList.contains('show')) closeDrawer();
    }
  });

  $('#btnRefresh').onclick = () => { S._errShown = false; loadOverview().then(() => toast('已刷新', 'ok')); };
  document.addEventListener('visibilitychange', () => { if (!document.hidden) loadOverview(); });

  /* 通用「点一下复制」：给任意元素加 data-copy 即可，避免每处都绑一次 */
  document.addEventListener('click', (e) => {
    const el = e.target.closest('[data-copy]');
    if (!el) return;
    const text = el.getAttribute('data-copy');
    if (!text) return;
    copyText(text).then((ok) => toast(ok ? '已复制 ' + text : '复制失败，请手动选中', ok ? 'ok' : 'err'));
  });

  /* expose handlers used in inline attributes */
  window.__go = (v) => go(v);
  window.__openContainer = (n) => openContainer(n);
  window.__act = (n, a, d) => act(n, a, d);
  window.__copyPass = (n) => {
    const c = arr(S.containers).find((x) => x.name === n);
    if (!c || !c.root_pass) { toast('暂无密码，可在容器详情里设置', 'err'); return; }
    copyText(c.root_pass).then((ok) => toast(ok ? '密码已复制' : '复制失败，请手动选中', ok ? 'ok' : 'err'));
  };
  window.__del = (n, d) => del(n, d);
  window.__term = (n) => openTerm(n);
  window.__refresh = () => { S._errShown = false; loadOverview().then(() => { render(); toast('已刷新', 'ok'); }); };
  window.__refreshDrawer = () => refreshDrawer();

  initTheme();
  loadOverview().then(() => {
    S.view = null;
    go(viewFromHash());
    startPolling();
    if (S.env && !S.env.installed) {
      setTimeout(() => toast('未检测到 LXC 运行环境，请前往「运行环境」完成安装', 'err'), 900);
    }
    // 深链：?open=<容器名> 直接打开详情抽屉
    const openName = new URLSearchParams(location.search).get('open');
    if (openName) setTimeout(() => openContainer(openName), 350);
  });
  window.addEventListener('hashchange', () => go(viewFromHash()));
})();
