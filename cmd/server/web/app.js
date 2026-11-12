'use strict';
(() => {
/* =====================================================================
   Utilidades
   ===================================================================== */
const KEY = new URLSearchParams(location.search).get('key'); // chave somente leitura (modo TV)
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const view = $('#view');

const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const nf = (v, d = 0) => Number(v || 0).toLocaleString('pt-BR', { minimumFractionDigits: d, maximumFractionDigits: d });
const fmtBytes = b => { b = +b || 0; const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']; let i = 0; while (b >= 1024 && i < 5) { b /= 1024; i++; } return nf(b, i === 0 || b >= 100 ? 0 : 1) + ' ' + u[i]; };
const fmtRate = b => fmtBytes(b) + '/s';
const pairRate = (a, b) => { const i = Math.min(3, Math.floor(Math.log(Math.max(a, b, 1)) / Math.log(1024))); const f = v => nf(v / 1024 ** i, i ? 1 : 0); return `↓${f(a)} ↑${f(b)} ${['B/s', 'KB/s', 'MB/s', 'GB/s'][i]}`; };
const fmtUp = s => { const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60); return d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`; };
const dur = s => { s = Math.max(0, Math.floor(s)); if (s < 60) return `${s} s`; if (s < 3600) return `${Math.floor(s / 60)} min`; if (s < 86400) return `${Math.floor(s / 3600)} h ${Math.floor(s % 3600 / 60)} min`; return `${Math.floor(s / 86400)} d ${Math.floor(s % 86400 / 3600)} h`; };
let skew = 0; // diferença entre o relógio do servidor e o do navegador
const nowS = () => Date.now() / 1000 + skew;
const ago = ts => { const s = Math.max(0, nowS() - ts); return s < 5 ? 'agora' : 'há ' + dur(s); };
const hhmm = ts => new Date(ts * 1000).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
const lvl = (v, w = 75, c = 90) => v >= c ? 'crit' : v >= w ? 'warn' : '';
const STATUS = { ok: 'Normal', warn: 'Atenção', crit: 'Crítico', offline: 'Sem resposta' };
const pill = s => `<span class="pill ${s}"><i></i>${STATUS[s] || s}</span>`;
const SEV = { crit: 'Crítico', warn: 'Atenção', info: 'Info' };
const sevPill = s => `<span class="pill ${s}"><i></i>${SEV[s] || s}</span>`;
const LV = { crit: 'crítico', error: 'erro', warn: 'aviso', info: 'info' };

// A API pode devolver null/ausente para listas vazias; sempre trabalhamos com arrays.
const arr = v => Array.isArray(v) ? v : [];
function fixOverview(o) {
  o.hosts = arr(o.hosts); o.apps = arr(o.apps); o.alerts = arr(o.alerts); o.events = arr(o.events);
  o.hosts.forEach(h => { h.services = arr(h.services); h.ips = arr(h.ips); h.load = arr(h.load).length === 3 ? h.load : [0, 0, 0]; });
  return o;
}
function fixHost(d) {
  d.points = arr(d.points); d.alerts = arr(d.alerts); d.logs = arr(d.logs); d.disk_eta = d.disk_eta || {};
  d.summary.services = arr(d.summary.services); d.summary.ips = arr(d.summary.ips);
  const sn = d.snap;
  if (sn) {
    ['procs', 'groups', 'disks', 'listen', 'conns', 'probes'].forEach(k => sn[k] = arr(sn[k]));
    sn.cpu.cores = arr(sn.cpu.cores); sn.host.ips = arr(sn.host.ips); sn.net.ifaces = arr(sn.net.ifaces);
  }
  return d;
}

async function api(path, opts = {}) {
  const u = path + (KEY ? (path.includes('?') ? '&' : '?') + 'key=' + encodeURIComponent(KEY) : '');
  const r = await fetch(u, { cache: 'no-store', ...opts });
  if (!r.ok) { const e = new Error('HTTP ' + r.status); e.status = r.status; throw e; }
  return r.json();
}

/* =====================================================================
   Gráficos (Chart.js)
   ===================================================================== */
const C = { cobalt: '#1457d6', azure: '#3b8cf0', navy: '#0a2c6b', sky: '#a9cdff', teal: '#19a7a0', amber: '#e0a100', red: '#d23a3a', grey: '#8a97b1', ok: '#138a5b' };
const PAL = [C.cobalt, C.azure, C.teal, C.amber, '#7aa7e8', C.navy, C.grey];
Chart.defaults.font.family = '"Plex", system-ui, sans-serif';
Chart.defaults.font.size = 12;
Chart.defaults.color = '#5a6a86';
Chart.defaults.borderColor = '#e6eefb';
Chart.defaults.maintainAspectRatio = false;
Chart.defaults.animation.duration = 450;
Chart.defaults.plugins.legend.labels.usePointStyle = true;
Chart.defaults.plugins.legend.labels.pointStyle = 'rectRounded';
Chart.defaults.plugins.legend.labels.boxWidth = 9;
Chart.defaults.plugins.legend.labels.boxHeight = 9;
Chart.defaults.plugins.tooltip.backgroundColor = '#0a2c6b';
Chart.defaults.plugins.tooltip.padding = 10;
Chart.defaults.plugins.tooltip.cornerRadius = 4;
Chart.defaults.plugins.tooltip.titleFont = { weight: '600' };

Chart.register({
  id: 'centerText',
  afterDraw(ch, _a, o) {
    if (!o || !o.text) return;
    const { ctx, chartArea: { left, right, top, bottom } } = ch;
    const x = (left + right) / 2, y = (top + bottom) / 2;
    ctx.save(); ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
    ctx.fillStyle = '#0f1f3d'; ctx.font = '600 28px Plex, sans-serif'; ctx.fillText(o.text, x, y - 7);
    ctx.fillStyle = '#5a6a86'; ctx.font = '12px Plex, sans-serif'; ctx.fillText(o.sub || '', x, y + 15);
    ctx.restore();
  }
});

let charts = {};
function mk(id, cfg) {
  const cv = document.getElementById(id); if (!cv) return;
  let c = charts[id];
  if (c && c.canvas !== cv) { c.destroy(); c = null; }
  if (c) { c.data = cfg.data; c.options = cfg.options; c.update('none'); }
  else charts[id] = new Chart(cv, cfg);
}
const killCharts = () => { Object.values(charts).forEach(c => c.destroy()); charts = {}; };

const rgba = (hex, a) => { const n = parseInt(hex.slice(1), 16); return `rgba(${n >> 16 & 255},${n >> 8 & 255},${n & 255},${a})`; };
const grad = color => ctx => {
  const a = ctx.chart.chartArea; if (!a) return rgba(color, .15);
  const g = ctx.chart.ctx.createLinearGradient(0, a.top, 0, a.bottom);
  g.addColorStop(0, rgba(color, .34)); g.addColorStop(1, rgba(color, .02)); return g;
};

function lineCfg(labels, sets, o = {}) {
  return {
    type: 'line',
    data: { labels, datasets: sets.map(s => ({ label: s.label, data: s.data, borderColor: s.color, backgroundColor: s.fill ? grad(s.color) : 'transparent', fill: !!s.fill, borderWidth: s.w || 1.8, borderDash: s.dash, pointRadius: 0, pointHitRadius: 8, tension: .25, spanGaps: true })) },
    options: {
      responsive: true, interaction: { mode: 'index', intersect: false },
      plugins: { legend: { display: sets.length > 1, position: 'bottom' }, tooltip: { callbacks: { label: c => ` ${c.dataset.label}: ${o.fmt ? o.fmt(c.parsed.y) : nf(c.parsed.y, 1)}` } } },
      scales: {
        x: { grid: { display: false }, ticks: { maxTicksLimit: 7, maxRotation: 0 } },
        y: { beginAtZero: true, max: o.max, grid: { color: '#edf3fc' }, ticks: { maxTicksLimit: 6, callback: v => o.fmt ? o.fmt(v) : v } }
      }
    }
  };
}

function barCfg(labels, sets, o = {}) {
  const va = o.h ? 'x' : 'y', la = o.h ? 'y' : 'x';
  return {
    type: 'bar',
    data: { labels, datasets: sets.map(s => ({ label: s.label, data: s.data, backgroundColor: s.color, borderRadius: 2, maxBarThickness: o.thick || 34 })) },
    options: {
      indexAxis: o.h ? 'y' : 'x', responsive: true,
      plugins: { legend: { display: sets.length > 1, position: 'bottom' }, tooltip: { callbacks: { label: c => ` ${c.dataset.label}: ${o.fmt ? o.fmt(c.raw) : nf(c.raw, 1)}`, afterLabel: o.after } } },
      scales: {
        [la]: { grid: { display: false }, ticks: { autoSkip: false, maxRotation: o.h ? 0 : 35, minRotation: 0, callback(v) { const l = this.getLabelForValue(v); const m = o.h ? 22 : 16; return l.length > m ? l.slice(0, m - 1) + '…' : l; } } },
        [va]: { beginAtZero: true, max: o.max, grid: { color: '#edf3fc' }, ticks: { maxTicksLimit: o.h ? 4 : 6, callback: v => o.fmt ? o.fmt(v) : v } }
      }
    }
  };
}

function doughCfg(labels, data, colors, o = {}) {
  return {
    type: 'doughnut',
    data: { labels, datasets: [{ data, backgroundColor: colors, borderColor: '#fff', borderWidth: 2 }] },
    options: {
      responsive: true, cutout: o.cut ?? '64%',
      plugins: { legend: { position: 'right', labels: { padding: 12 } }, tooltip: { callbacks: { label: c => ` ${c.label}: ${o.fmt ? o.fmt(c.raw) : c.raw}` } }, centerText: o.center || {} }
    }
  };
}

/* =====================================================================
   Componentes
   ===================================================================== */
const bar = (l, v) => `<div class="bar"><span class="l">${l}</span><div class="track"><i class="${lvl(v)}" style="width:${Math.min(100, v)}%"></i></div><span class="v">${nf(v)}%</span></div>`;

function chips(list, max = 6) {
  const shown = list.slice(0, max).map(s => `<span class="chip${s.exposed ? '' : ' loc'}" title="${s.exposed ? 'acessível pela rede' : 'somente local'}">${esc(s.name)} :${s.port}</span>`).join('');
  return shown + (list.length > max ? `<span class="chip loc">+${list.length - max}</span>` : '');
}

function hostCard(h) {
  const off = h.status === 'offline';
  const body = off
    ? `<div class="empty" style="padding:10px 0 14px">Sem resposta ${ago(h.last_seen)}. O agente parou de enviar dados.</div>`
    : bar('CPU', h.cpu) + bar('Memória', h.mem) + bar('Disco', h.disk) +
      `<div class="h-line"><span>Load <b>${nf(h.load[0], 1)} · ${nf(h.load[1], 1)} · ${nf(h.load[2], 1)}</b></span><span><b>${pairRate(h.rx, h.tx)}</b></span></div>`;
  const al = [h.crit ? `<span class="a-c">${h.crit} crítico${h.crit > 1 ? 's' : ''}</span>` : '', h.warn ? `<span class="a-w">${h.warn} em atenção</span>` : ''].filter(Boolean).join(' · ') || '<span>Sem alertas</span>';
  return `<a class="host s-${h.status}" href="#/host/${encodeURIComponent(h.name)}">
    <div class="h-head"><span class="h-name">${esc(h.name)}</span>${pill(h.status)}</div>
    <div class="h-os">${esc(h.os || '—')} · ${h.cores} núcleos · ${fmtBytes(h.mem_total)} · ${esc((h.ips || [])[0] || '')}</div>
    ${body}
    <div class="chips">${chips(h.services || [])}</div>
    <div class="h-foot">${al}<span>${off ? '' : 'visto ' + ago(h.last_seen)}</span></div>
  </a>`;
}

function alertRow(a, o = {}) {
  const when = a.rule === 'event' ? ago(a.since) : a.active ? `ativo há ${dur(nowS() - a.since)}` : `resolvido ${ago(a.resolved)}`;
  const dis = o.dismiss && a.rule.startsWith('svc:') ? ` · <button class="act" data-act="dismiss" data-id="${a.id}" title="Remove o alerta e deixa de monitorar esta porta">Dispensar</button>` : '';
  return `<div class="al"><div>${sevPill(a.sev)}</div><div class="m">${o.host === false ? '' : `<a href="#/host/${encodeURIComponent(a.host)}">${esc(a.host)}</a> — `}${esc(a.msg)}</div><div class="w">${when}${dis}</div></div>`;
}

function logLines(list, withHost) {
  if (!list.length) return '<div class="empty">Nenhuma mensagem com esse filtro.</div>';
  return list.map(l => `<div class="ln ${withHost ? 'g ' : ''}${l.level}"><span class="t" title="${new Date(l.t).toLocaleString('pt-BR')}">${new Date(l.t).toLocaleTimeString('pt-BR')}</span><span class="lv">${LV[l.level] || l.level}</span>${withHost ? `<a class="h" href="#/host/${encodeURIComponent(l.host)}">${esc(l.host)}</a>` : ''}<span class="src">${esc(l.src)}</span><span class="msg">${esc(l.msg)}</span></div>`).join('');
}

function meter(label, segs, value) {
  return `<div class="meter"><span class="m-l">${label}</span><div class="m-bar">${segs.map(g => `<i class="${g.c}" style="width:${Math.min(100, Math.max(0, g.p))}%"></i>`).join('')}</div><span class="m-v">${value}</span></div>`;
}

const kpi = (l, v, s, cls = '') => `<div class="kpi ${cls}"><div class="k-l">${l}</div><div class="k-v">${v}</div><div class="k-s">${s}</div></div>`;

function installHelp() {
  const o = location.origin;
  return `<div class="card install"><h3>Nenhum servidor conectado ainda</h3>
    <p>Em cada servidor Linux que você quer monitorar, execute como administrador:</p>
    <pre id="cmd">curl -fsSL ${esc(o)}/install.sh | sudo TOKEN=SEU_TOKEN bash</pre>
    <button class="btn ghost" data-act="copy">Copiar comando</button>
    <p style="margin-top:18px">Em servidores <b>Windows</b>, no PowerShell como Administrador:</p>
    <pre id="cmdw">$env:LYNX_TOKEN='SEU_TOKEN'; irm ${esc(o)}/install.ps1 | iex</pre>
    <button class="btn ghost" data-act="copyw">Copiar comando Windows</button>
    <p style="margin-top:18px">O agente usa pouca CPU e memória, e o servidor aparece aqui em poucos segundos. O token é o valor de <b>TOKEN</b> configurado no servidor do painel.</p></div>`;
}

/* =====================================================================
   Estado global e roteador
   ===================================================================== */
let OV = null, cur = null, busy = false;
let hostRange = '1h', procSort = 'cpu', alState = 'active', logLevel = '', logQ = '', logHost = '';
let hostLogLevel = '', hostLogQ = '';

const myTitle = document.title;
function setLive(ok) {
  const el = $('#live'); if (!el) return;
  el.classList.toggle('off', !ok);
  $('#live-t').textContent = ok ? 'atualizado ' + new Date().toLocaleTimeString('pt-BR') : 'sem conexão com o servidor';
}
function updateHeader() {
  const t = OV.totals, c = t.alerts_crit, w = t.alerts_warn;
  $('#nav-badge').innerHTML = c ? `<span class="badge">${c}</span>` : w ? `<span class="badge w">${w}</span>` : '';
  document.title = (c ? `(${c}) ` : '') + myTitle;
}

async function poll() {
  if (busy) return; busy = true;
  try {
    OV = fixOverview(await api('/api/overview'));
    skew = OV.now - Date.now() / 1000;
    setLive(true); updateHeader();
    const my = cur; if (my) await my.update();
  } catch (e) {
    setLive(false);
    if (cur && cur.fail) cur.fail(e);
  } finally { busy = false; }
}

function go() {
  if (cur && cur.stop) cur.stop();
  killCharts();
  const parts = location.hash.replace(/^#\/?/, '').split('/');
  const r = parts[0] || '';
  document.body.classList.toggle('tv', r === 'tv');
  $$('#nav a').forEach(a => a.classList.toggle('on', a.dataset.r === r || (r === 'host' && a.dataset.r === '')));
  window.scrollTo(0, 0);
  cur = r === 'host' ? hostView(decodeURIComponent(parts[1] || ''))
      : r === 'alerts' ? alertsView()
      : r === 'logs' ? logsView()
      : r === 'tv' ? tvView()
      : overviewView();
  cur.build();
  if (OV) cur.update(); else poll();
  if (OV) poll();
}

/* =====================================================================
   Visão geral
   ===================================================================== */
function overviewView() {
  return {
    build() {
      view.innerHTML = `
        <section class="hero"><h1 id="hero-h">Carregando…</h1><p id="hero-p">&nbsp;</p></section>
        <div class="kpis" id="kpis"></div>
        <div class="sec"><h2>Servidores</h2><span class="hint">Clique em um servidor para ver o detalhe completo</span></div>
        <div class="hosts" id="hosts"></div>
        <div class="row r-fleet">
          <div class="card"><h3>CPU e memória por servidor</h3><div class="chart"><canvas id="c-fleet"></canvas></div></div>
          <div class="card"><h3>Situação dos servidores</h3><div class="chart"><canvas id="c-status"></canvas></div></div>
          <div class="card"><h3>Maiores consumidores de memória</h3><div class="chart"><canvas id="c-apps"></canvas></div></div>
        </div>
        <div class="row r-2">
          <div class="card"><h3>Alertas ativos</h3><div id="ov-alerts"></div></div>
          <div class="card"><h3>Eventos recentes</h3><div id="ov-events"></div></div>
        </div>`;
    },
    update() {
      const t = OV.totals, hosts = OV.hosts;
      const bad = t.crit + t.warn + t.offline;
      let h1, p;
      if (!t.total) { h1 = 'Nenhum servidor conectado'; p = 'Instale o agente em um servidor Linux para começar.'; }
      else if (!bad) { h1 = 'Tudo certo'; p = `${t.total} ${t.total > 1 ? 'servidores operando' : 'servidor operando'} dentro dos limites.`; }
      else {
        h1 = `${bad} de ${t.total} servidores ${bad > 1 ? 'precisam' : 'precisa'} de atenção`;
        p = [t.crit && `${t.crit} crítico${t.crit > 1 ? 's' : ''}`, t.offline && `${t.offline} sem resposta`, t.warn && `${t.warn} em atenção`].filter(Boolean).join(' · ');
      }
      $('#hero-h').textContent = h1; $('#hero-p').textContent = p;

      $('#kpis').innerHTML =
        kpi('Servidores online', `${t.online}<small>/ ${t.total}</small>`, t.offline ? `${t.offline} sem resposta` : 'todos respondendo', t.offline ? 'bad' : '') +
        kpi('Alertas críticos', nf(t.alerts_crit), 'ativos agora', t.alerts_crit ? 'bad' : '') +
        kpi('Alertas de atenção', nf(t.alerts_warn), 'ativos agora', t.alerts_warn ? 'warn' : '') +
        kpi('CPU média', `${nf(t.cpu_avg)}<small>%</small>`, 'servidores online') +
        kpi('Memória média', `${nf(t.mem_avg)}<small>%</small>`, 'servidores online') +
        kpi('Disco mais cheio', `${nf(t.worst_disk)}<small>%</small>`, t.worst_disk_host ? `${esc(t.worst_disk_host)} ${esc(t.worst_disk_mount)}` : '—', lvl(t.worst_disk, 80, 92) === 'crit' ? 'bad' : lvl(t.worst_disk, 80, 92) ? 'warn' : '');

      $('#hosts').innerHTML = hosts.length ? hosts.map(hostCard).join('') : installHelp();
      $$('#view .row').forEach(r => r.style.display = hosts.length ? '' : 'none'); // sem servidores: esconde gráficos vazios

      const live = hosts.filter(h => h.status !== 'offline');
      mk('c-fleet', barCfg(live.map(h => h.name), [{ label: 'CPU %', data: live.map(h => h.cpu), color: C.azure }, { label: 'Memória %', data: live.map(h => h.mem), color: C.navy }], { max: 100, fmt: v => nf(v) + '%' }));
      const st = [['Normal', t.ok, C.ok], ['Atenção', t.warn, C.amber], ['Crítico', t.crit, C.red], ['Sem resposta', t.offline, C.grey]].filter(x => x[1] > 0);
      mk('c-status', doughCfg(st.map(x => x[0]), st.map(x => x[1]), st.map(x => x[2]), { center: { text: String(t.total), sub: t.total === 1 ? 'servidor' : 'servidores' } }));
      mk('c-apps', barCfg(OV.apps.map(a => `${a.name} · ${a.host}`), [{ label: 'Memória', data: OV.apps.map(a => a.rss), color: C.cobalt }], { h: true, fmt: fmtBytes, thick: 18 }));

      $('#ov-alerts').innerHTML = OV.alerts.length ? `<div class="alist">${OV.alerts.slice(0, 8).map(a => alertRow(a, { dismiss: true })).join('')}</div>${OV.alerts.length > 8 ? `<p class="hint" style="margin:10px 0 0"><a href="#/alerts">Ver todos os ${OV.alerts.length} alertas</a></p>` : ''}` : '<div class="empty">Nenhum alerta ativo.</div>';
      $('#ov-events').innerHTML = OV.events.length ? `<div class="alist">${OV.events.slice(0, 8).map(a => alertRow(a)).join('')}</div>` : '<div class="empty">Nenhum evento registrado.</div>';
    },
    stop() { }
  };
}

/* =====================================================================
   Detalhe do servidor
   ===================================================================== */
function hostView(name) {
  let d = null;
  const render = () => {
    const s = d.summary, sn = d.snap, pts = d.points;
    $('#h-title').textContent = s.name;
    $('#h-crumb').textContent = s.name;
    $('#h-status').innerHTML = pill(s.status);
    if (!sn) return;
    $('#h-sub').textContent = `${s.os} · kernel ${s.kernel} · ${sn.host.cpu_model} · ${(s.ips || []).join(', ')}`;

    // alertas ativos
    const act = d.alerts.filter(a => a.active);
    $('#h-alerts').innerHTML = act.length ? `<div class="banner ${act.some(a => a.sev === 'crit') ? '' : 'w'}"><div class="alist">${act.map(a => alertRow(a, { host: false, dismiss: true })).join('')}</div></div>` : '';

    // KPIs
    const cpuC = lvl(s.cpu), memC = lvl(s.mem, 80, 92), dskC = lvl(s.disk, 80, 92);
    const lpc = s.load[1] / Math.max(1, s.cores);
    $('#h-kpis').innerHTML =
      kpi('CPU', `${nf(s.cpu, 1)}<small>%</small>`, `${s.cores} núcleos · iowait ${nf(sn.cpu.iowait, 1)}%`, cpuC === 'crit' ? 'bad' : cpuC ? 'warn' : '') +
      kpi('Memória', `${nf(s.mem)}<small>%</small>`, `${fmtBytes(s.mem_used)} de ${fmtBytes(s.mem_total)}`, memC === 'crit' ? 'bad' : memC ? 'warn' : '') +
      kpi('Swap', sn.mem.swap_total ? `${nf(s.swap)}<small>%</small>` : '—', sn.mem.swap_total ? `${fmtBytes(sn.mem.swap_used)} de ${fmtBytes(sn.mem.swap_total)}` : 'sem swap', lvl(s.swap, 40, 75) ? 'warn' : '') +
      kpi('Load average', `${nf(s.load[0], 2)}`, `5 min ${nf(s.load[1], 2)} · 15 min ${nf(s.load[2], 2)}`, lpc >= 3 ? 'bad' : lpc >= 1.5 ? 'warn' : '') +
      kpi('Disco mais cheio', `${nf(s.disk)}<small>%</small>`, esc(s.disk_mount || '—'), dskC === 'crit' ? 'bad' : dskC ? 'warn' : '') +
      kpi('Rede', `<span style="font-size:21px">↓ ${fmtRate(s.rx)}</span>`, `↑ ${fmtRate(s.tx)} · ${sn.tasks.total} processos`);

    // painel htop
    const cores = sn.cpu.cores.map((c, i) => meter(String(i), [{ c: 'u', p: c.u }, { c: 's', p: c.s }, { c: 'w', p: c.w }], nf(c.u + c.s + c.w, 1) + '%')).join('');
    const m = sn.mem, T = m.total || 1;
    $('#htop-l').innerHTML =
      `<h3>Processadores</h3><div class="cores">${cores}</div>
       <div class="legend"><span style="--c:${C.azure}">usuário</span><span style="--c:${C.navy}">sistema</span><span style="--c:#e6a100">espera de disco</span></div>
       <h3 style="margin-top:16px">Memória</h3>
       ${meter('Mem', [{ c: 'used', p: m.used / T * 100 }, { c: 'buf', p: m.buffers / T * 100 }, { c: 'cache', p: m.cache / T * 100 }], `${fmtBytes(m.total - m.available)}/${fmtBytes(m.total)}`)}
       ${meter('Swp', [{ c: 'swp', p: m.swap_total ? m.swap_used / m.swap_total * 100 : 0 }], m.swap_total ? `${fmtBytes(m.swap_used)}/${fmtBytes(m.swap_total)}` : 'sem swap')}
       <div class="legend"><span style="--c:${C.cobalt}">em uso</span><span style="--c:#6fa8f5">buffers</span><span style="--c:#b9d5fb">cache</span></div>
       <dl class="stats">
         <dt>Processos</dt><dd>${sn.tasks.total} (${sn.tasks.running} ativos, ${sn.tasks.zombie} zumbis) · ${sn.tasks.threads} threads</dd>
         <dt>Load average</dt><dd>${nf(sn.load[0], 2)} ${nf(sn.load[1], 2)} ${nf(sn.load[2], 2)}</dd>
         <dt>Tempo ligado</dt><dd>${fmtUp(sn.host.uptime)}</dd>
         <dt>Agente</dt><dd>v${esc(sn.version)} · envia a cada ${sn.interval} s</dd>
       </dl>`;

    const rows = [...sn.procs].sort((a, b) => procSort === 'mem' ? b.rss - a.rss : b.cpu - a.cpu).slice(0, 14);
    const maxC = Math.max(100, ...rows.map(p => p.cpu)), maxM = Math.max(1, ...rows.map(p => p.rss));
    $('#proc-body').innerHTML = rows.map(p => `<tr>
      <td class="n">${p.pid}</td><td>${esc(p.user)}</td>
      <td class="n fill ${p.cpu >= 80 ? 'hot' : ''}" style="--p:${p.cpu / maxC * 100}">${nf(p.cpu, 1)}</td>
      <td class="n fill" style="--p:${p.rss / maxM * 100}">${nf(p.mem, 1)}</td>
      <td class="n">${fmtBytes(p.rss)}</td><td class="n">${p.threads}</td><td class="m">${esc(p.state)}</td>
      <td class="cmd" title="${esc(p.cmd)}">${esc(p.cmd)}</td></tr>`).join('');
    $$('#seg-proc button').forEach(b => b.classList.toggle('on', b.dataset.v === procSort));
    $$('#seg-range button').forEach(b => b.classList.toggle('on', b.dataset.v === hostRange));

    // séries temporais
    const lab = pts.map(p => hhmm(p.t));
    mk('c-cpu', lineCfg(lab, [{ label: 'CPU', data: pts.map(p => p.cpu), color: C.azure, fill: true }, { label: 'Espera de disco', data: pts.map(p => p.iow), color: C.amber, w: 1.4 }], { max: 100, fmt: v => nf(v, 0) + '%' }));
    mk('c-mem', lineCfg(lab, [{ label: 'Memória', data: pts.map(p => p.mem), color: C.cobalt, fill: true }, { label: 'Swap', data: pts.map(p => p.swap), color: C.grey, w: 1.4, dash: [4, 3] }], { max: 100, fmt: v => nf(v, 0) + '%' }));
    mk('c-load', lineCfg(lab, [{ label: '1 min', data: pts.map(p => p.l1), color: C.azure, w: 2 }, { label: '5 min', data: pts.map(p => p.l5), color: C.cobalt }, { label: '15 min', data: pts.map(p => p.l15), color: C.navy }, { label: `Capacidade (${s.cores} núcleos)`, data: pts.map(() => s.cores), color: C.red, w: 1.2, dash: [5, 4] }], { fmt: v => nf(v, 2) }));
    mk('c-net', lineCfg(lab, [{ label: 'Recebido', data: pts.map(p => p.rx), color: C.azure, fill: true }, { label: 'Enviado', data: pts.map(p => p.tx), color: C.teal }], { fmt: fmtRate }));
    mk('c-dio', lineCfg(lab, [{ label: 'Leitura', data: pts.map(p => p.dr), color: C.cobalt }, { label: 'Escrita', data: pts.map(p => p.dw), color: C.amber }], { fmt: fmtRate }));

    // composição
    mk('c-memp', doughCfg(['Em uso', 'Buffers', 'Cache', 'Livre'], [m.used, m.buffers, m.cache, m.free], [C.cobalt, '#6fa8f5', '#b9d5fb', '#e8f0fc'], { fmt: fmtBytes, center: { text: nf(s.mem) + '%', sub: 'em uso' } }));
    mk('c-apps', barCfg(sn.groups.slice(0, 8).map(g => g.name + (g.count > 1 ? ` ×${g.count}` : '')), [{ label: 'Memória', data: sn.groups.slice(0, 8).map(g => g.rss), color: C.cobalt }], { h: true, fmt: fmtBytes, thick: 16 }));
    const dk = sn.disks;
    mk('c-disk', {
      ...barCfg(dk.map(x => x.mount), [{ label: 'Uso', data: dk.map(x => x.pct), color: dk.map(x => x.pct >= 92 ? C.red : x.pct >= 80 ? C.amber : C.azure) }], { max: 100, fmt: v => nf(v) + '%', thick: 46, after: c => `${fmtBytes(dk[c.dataIndex].used)} de ${fmtBytes(dk[c.dataIndex].total)} (${dk[c.dataIndex].fs})` })
    });
    const eta = Object.entries(d.disk_eta || {}).filter(([, h]) => h < 72);
    $('#disk-eta').innerHTML = eta.length ? eta.map(([mnt, h]) => `<div class="hint" style="color:#946100;margin-top:6px"><b>${esc(mnt)}</b> deve encher em ~${h < 1 ? Math.round(h * 60) + ' min' : h < 48 ? Math.round(h) + ' h' : Math.round(h / 24) + ' dias'} no ritmo atual.</div>`).join('') : '';
    const cs = {}; sn.conns.forEach(c => { const k = c.service || c.proc || 'outros'; cs[k] = (cs[k] || 0) + c.count; });
    const ce = Object.entries(cs).sort((a, b) => b[1] - a[1]); const top = ce.slice(0, 6), rest = ce.slice(6).reduce((a, x) => a + x[1], 0);
    if (rest) top.push(['Outros', rest]);
    const totC = ce.reduce((a, x) => a + x[1], 0);
    mk('c-conn', doughCfg(top.map(x => x[0]), top.map(x => x[1]), PAL, { cut: '0%', fmt: v => v + ' conexões' }));
    $('#conn-n').textContent = totC ? `${totC} conexões ativas` : 'sem conexões ativas';

    // testes web
    $('#probes').innerHTML = sn.probes.length ? sn.probes.map(p => {
      const bad = p.err || p.status >= 500, mid = !bad && p.status >= 400;
      return `<div class="probe ${bad ? 'bad' : mid ? 'mid' : ''}"><div class="code">${p.err ? 'Falha' : p.status}</div><div class="u">${esc(p.url)}</div><div class="ms">${p.err ? esc(p.err) : p.ms + ' ms'}${p.proc ? ' · ' + esc(p.proc) : ''}</div></div>`;
    }).join('') : '<div class="empty">Nenhum servidor web (portas 80, 443, 8080…) detectado nesta máquina.</div>';

    // portas e conexões
    const ls = [...sn.listen].sort((a, b) => (b.exposed - a.exposed) || a.port - b.port).slice(0, 80);
    $('#ports-body').innerHTML = ls.map(l => `<tr><td class="m">${l.proto}</td><td class="n">${l.port}</td><td>${esc(l.service || '—')}</td><td class="m">${esc(l.proc || '—')}</td><td class="m">${esc(l.addr === '*' ? 'todas' : l.addr)}</td><td><span class="tag ${l.exposed ? '' : 'loc'}">${l.exposed ? 'rede' : 'local'}</span></td></tr>`).join('');
    const cn = [...sn.conns].sort((a, b) => b.count - a.count).slice(0, 40);
    $('#conns-body').innerHTML = cn.map(c => `<tr><td class="m">${esc(c.proc || '—')}</td><td class="dir">${c.dir === 'out' ? 'conecta em' : 'recebe de'}</td><td class="m">${esc(c.remote)}${c.dir === 'out' ? ':' + c.rport : ''}</td><td>${esc(c.service || (c.dir === 'in' ? 'porta ' + c.lport : '—'))}</td><td class="n">${c.count}</td></tr>`).join('') || '<tr><td colspan="5" class="empty">Sem conexões TCP estabelecidas.</td></tr>';

    // logs
    const L = d.logs.filter(l => (!hostLogLevel || (hostLogLevel === 'error' ? (l.level === 'error' || l.level === 'crit') : l.level !== 'info')) && (!hostLogQ || (l.msg + ' ' + l.src).toLowerCase().includes(hostLogQ.toLowerCase()))).reverse();
    const box = $('#h-logs'), top0 = box.scrollTop;
    box.innerHTML = logLines(L, false); box.scrollTop = top0;
    $('#h-log-n').textContent = d.logs.length ? `${d.logs.length} mensagens guardadas` : '';

    const hist = d.alerts.filter(a => !a.active).slice(0, 8);
    $('#h-hist').innerHTML = hist.length ? `<div class="alist">${hist.map(a => alertRow(a, { host: false })).join('')}</div>` : '<div class="empty">Nenhum evento anterior.</div>';
  };

  return {
    build() {
      view.innerHTML = `
        <div class="crumb"><a href="#/">Visão geral</a> / <span id="h-crumb">${esc(name)}</span></div>
        <div class="hhead">
          <div><h1 id="h-title">${esc(name)}</h1><div class="h-sub" id="h-sub">Carregando…</div></div>
          <div class="h-tools"><span id="h-status"></span>
            <div class="seg" id="seg-range"><button data-act="range" data-v="1h">Última hora</button><button data-act="range" data-v="24h">24 horas</button></div>
            <button class="btn" data-act="forget" data-host="${esc(name)}" title="Remove este servidor do painel (o agente volta a aparecer se continuar enviando)">Remover</button>
          </div>
        </div>
        <div id="h-alerts"></div>
        <div class="kpis" id="h-kpis"></div>

        <div class="sec"><h2>Monitor do sistema</h2><span class="hint">Equivalente ao htop, com atualização automática</span></div>
        <div class="card htop">
          <div id="htop-l"></div>
          <div>
            <div class="ptools"><h3>Processos</h3><div class="seg" id="seg-proc"><button data-act="psort" data-v="cpu">Por CPU</button><button data-act="psort" data-v="mem">Por memória</button></div></div>
            <div class="scroll"><table class="tbl"><thead><tr><th class="n">PID</th><th>Usuário</th><th class="n">CPU %</th><th class="n">Mem %</th><th class="n">Residente</th><th class="n">Thr</th><th>S</th><th>Comando</th></tr></thead><tbody id="proc-body"></tbody></table></div>
          </div>
        </div>

        <div class="sec"><h2>Desempenho no tempo</h2><span class="hint" id="range-hint"></span></div>
        <div class="row r-2 flush">
          <div class="card"><h3>CPU</h3><div class="chart sm"><canvas id="c-cpu"></canvas></div></div>
          <div class="card"><h3>Memória</h3><div class="chart sm"><canvas id="c-mem"></canvas></div></div>
        </div>
        <div class="row r-3">
          <div class="card"><h3>Load average</h3><div class="chart sm"><canvas id="c-load"></canvas></div></div>
          <div class="card"><h3>Rede</h3><div class="chart sm"><canvas id="c-net"></canvas></div></div>
          <div class="card"><h3>Disco: leitura e escrita</h3><div class="chart sm"><canvas id="c-dio"></canvas></div></div>
        </div>

        <div class="sec"><h2>Quem consome o quê</h2></div>
        <div class="row r-4 flush">
          <div class="card"><h3>Memória</h3><div class="chart sm"><canvas id="c-memp"></canvas></div></div>
          <div class="card"><h3>Memória por aplicação <small>soma de processos de mesmo nome</small></h3><div class="chart sm"><canvas id="c-apps"></canvas></div></div>
          <div class="card"><h3>Uso dos discos</h3><div class="chart sm"><canvas id="c-disk"></canvas></div><div id="disk-eta"></div></div>
          <div class="card"><h3>Conexões por serviço <small id="conn-n"></small></h3><div class="chart sm"><canvas id="c-conn"></canvas></div></div>
        </div>

        <div class="sec"><h2>Servidores web</h2><span class="hint">Teste de resposta feito pelo próprio agente em cada porta web local</span></div>
        <div class="probes" id="probes"></div>

        <div class="sec"><h2>Portas e conexões</h2><span class="hint">O que está aberto nesta máquina e com quem ela conversa</span></div>
        <div class="row r-ports flush">
          <div class="card"><h3>Portas escutando</h3><div class="scroll"><table class="tbl"><thead><tr><th>Proto</th><th class="n">Porta</th><th>Serviço</th><th>Processo</th><th>Endereço</th><th>Acesso</th></tr></thead><tbody id="ports-body"></tbody></table></div></div>
          <div class="card"><h3>Conexões ativas</h3><div class="scroll"><table class="tbl"><thead><tr><th>Processo</th><th></th><th>Endereço</th><th>Serviço</th><th class="n">Qtd</th></tr></thead><tbody id="conns-body"></tbody></table></div></div>
        </div>

        <div class="sec"><h2>Logs</h2><span class="hint" id="h-log-n"></span></div>
        <div class="card">
          <div class="logbar" style="margin-bottom:10px">
            <div class="seg" id="seg-hlog"><button data-act="hlog" data-v="">Tudo</button><button data-act="hlog" data-v="warn">Avisos e erros</button><button data-act="hlog" data-v="error">Só erros</button></div>
            <input id="h-log-q" type="search" placeholder="Buscar nas mensagens" value="${esc(hostLogQ)}">
          </div>
          <div class="logs" id="h-logs"></div>
        </div>

        <div class="sec"><h2>Histórico de alertas</h2></div>
        <div class="card" id="h-hist"></div>`;
      $$('#seg-hlog button').forEach(b => b.classList.toggle('on', b.dataset.v === hostLogLevel));
      $('#h-log-q').addEventListener('input', e => { hostLogQ = e.target.value; if (d) render(); });
    },
    async update() {
      const my = cur;
      try {
        const r = await api(`/api/hosts/${encodeURIComponent(name)}?range=${hostRange}`);
        if (cur !== my) return;
        d = fixHost(r); render();
        $('#range-hint').textContent = hostRange === '24h' ? 'Médias por minuto nas últimas 24 horas' : 'Amostras a cada poucos segundos na última hora';
      } catch (e) {
        if (e.status === 404) { view.innerHTML = `<div class="hero"><h1>Servidor não encontrado</h1><p>“${esc(name)}” não está mais no painel. <a href="#/" style="color:#fff;text-decoration:underline">Voltar à visão geral</a></p></div>`; cur = { update() { }, stop() { } }; }
        else if (e.status === 401) { $('#h-sub').textContent = 'Acesso negado: entre com usuário e senha para ver o detalhe do servidor.'; }
        else throw e;
      }
    },
    stop() { }
  };
}

/* =====================================================================
   Alertas
   ===================================================================== */
function alertsView() {
  return {
    build() {
      view.innerHTML = `
        <section class="hero"><h1>Alertas</h1><p>Problemas detectados e previsões de risco, com origem e duração.</p></section>
        <div class="card">
          <div class="logbar" style="margin-bottom:8px"><div class="seg" id="seg-al"><button data-act="alstate" data-v="active">Ativos</button><button data-act="alstate" data-v="all">Ativos e histórico</button></div><span class="hint" id="al-n"></span></div>
          <div id="al-list"></div>
        </div>`;
    },
    async update() {
      const my = cur;
      const list = arr(await api('/api/alerts' + (alState === 'active' ? '?state=active' : '')));
      if (cur !== my) return;
      $$('#seg-al button').forEach(b => b.classList.toggle('on', b.dataset.v === alState));
      $('#al-n').textContent = `${list.length} ${list.length === 1 ? 'registro' : 'registros'}`;
      $('#al-list').innerHTML = list.length ? `<div class="alist">${list.map(a => alertRow(a, { dismiss: true })).join('')}</div>` : '<div class="empty">Nenhum alerta ativo. Tudo dentro dos limites.</div>';
    },
    stop() { }
  };
}

/* =====================================================================
   Logs de todos os servidores
   ===================================================================== */
function logsView() {
  const load = async () => {
    const my = cur;
    const p = new URLSearchParams({ limit: 400 });
    if (logHost) p.set('host', logHost); if (logLevel) p.set('level', logLevel); if (logQ) p.set('q', logQ);
    const list = arr(await api('/api/logs?' + p));
    if (cur !== my) return;
    const box = $('#g-logs'), t = box.scrollTop;
    box.innerHTML = logLines(list, true); box.scrollTop = t;
    $('#g-n').textContent = `${list.length} mensagens`;
  };
  return {
    build() {
      view.innerHTML = `
        <section class="hero"><h1>Logs</h1><p>Erros e avisos coletados pelos agentes, de todos os servidores, do mais recente ao mais antigo.</p></section>
        <div class="card">
          <div class="logbar" style="margin-bottom:10px">
            <select id="g-host"><option value="">Todos os servidores</option></select>
            <div class="seg" id="seg-glog"><button data-act="glog" data-v="">Tudo</button><button data-act="glog" data-v="warn">Avisos e erros</button><button data-act="glog" data-v="error">Só erros</button></div>
            <input id="g-q" type="search" placeholder="Buscar nas mensagens" value="${esc(logQ)}">
            <span class="hint" id="g-n"></span>
          </div>
          <div class="logs tall" id="g-logs"></div>
        </div>`;
      $('#g-q').addEventListener('input', e => { logQ = e.target.value; load(); });
      $('#g-host').addEventListener('change', e => { logHost = e.target.value; load(); });
    },
    async update() {
      const sel = $('#g-host');
      if (sel && OV) {
        const names = OV.hosts.map(h => h.name);
        if (sel.options.length - 1 !== names.length) { sel.innerHTML = '<option value="">Todos os servidores</option>' + names.map(n => `<option>${esc(n)}</option>`).join(''); }
        sel.value = logHost;
      }
      $$('#seg-glog button').forEach(b => b.classList.toggle('on', b.dataset.v === logLevel));
      await load();
    },
    stop() { }
  };
}

/* =====================================================================
   Modo TV
   ===================================================================== */
function tvLayout(n, w, h) {
  const gap = 16, minW = 300, minH = 200;
  const maxC = Math.max(1, Math.floor((w + gap) / (minW + gap))), maxR = Math.max(1, Math.floor((h + gap) / (minH + gap)));
  if (n >= maxC * maxR) return { cols: maxC, rows: maxR, per: maxC * maxR };
  let best = null;
  for (let c = 1; c <= maxC; c++) {
    const r = Math.ceil(n / c); if (r > maxR) continue;
    const score = (c * r - n) * 10 + Math.abs((w / c) / (h / r) - 1.7);
    if (!best || score < best.score) best = { cols: c, rows: r, per: n, score };
  }
  return best || { cols: maxC, rows: maxR, per: maxC * maxR };
}

function tvView() {
  let page = 0, timers = [], idleT = null;
  const tbar = (l, v) => `<div class="t-b"><span class="l">${l}</span><div class="track"><i class="${lvl(v)}" style="width:${Math.min(100, v)}%"></i></div><span class="v">${nf(v)}%</span></div>`;
  const tile = h => {
    const off = h.status === 'offline';
    return `<div class="tile s-${h.status}">
      <div class="t-head"><span class="t-name">${esc(h.name)}</span>${pill(h.status)}</div>
      <div class="t-sub">${esc((h.os || '').replace(/ \(.*\)/, ''))} · ${h.cores} núcleos · ${fmtBytes(h.mem_total)}</div>
      ${off ? `<div class="t-off">Sem resposta ${ago(h.last_seen)}</div>` : `<div class="t-bars">${tbar('CPU', h.cpu)}${tbar('Memória', h.mem)}${tbar('Disco', h.disk)}</div>`}
      <div class="t-foot"><div class="chips">${chips((h.services || []).filter(s => s.exposed && s.port !== 22), 2)}</div><span>${off ? '' : 'load ' + nf(h.load[1], 2)}</span></div>
    </div>`;
  };
  const clock = () => {
    const el = $('#tv-clock'); if (!el) return;
    const n = new Date();
    el.innerHTML = `${n.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })}<small>${n.toLocaleDateString('pt-BR', { weekday: 'long', day: 'numeric', month: 'long' })}</small>`;
  };
  const wake = () => { document.body.classList.remove('idle'); clearTimeout(idleT); idleT = setTimeout(() => document.body.classList.add('idle'), 4000); };
  const onResize = () => self.update();
  const self = {
    build() {
      view.innerHTML = `<div class="tvroot">
        <div class="tv-head">
          <a class="brand" href="#/"><img src="logo.png" alt="Lynx" width="46" height="46"><span><b>Lynx</b><small>resumo geral</small></span></a>
          <span class="sp"></span>
          <div class="tv-page" id="tv-page"></div>
          <div class="tv-ctl"><button class="btn" data-act="fs">Tela cheia</button><button class="btn" data-act="exit">Sair do modo TV</button></div>
          <div class="tv-clock" id="tv-clock"></div>
        </div>
        <div class="tv-kpis" id="tv-kpis"></div>
        <div class="tv-grid" id="tv-grid"></div>
        <div class="tv-foot" id="tv-foot"></div>
      </div>`;
      clock(); timers.push(setInterval(clock, 1000));
      timers.push(setInterval(() => { page++; self.update(); }, 12000));
      window.addEventListener('resize', onResize);
      document.addEventListener('mousemove', wake); document.addEventListener('keydown', wake);
      wake();
    },
    update() {
      if (!OV || !$('#tv-grid')) return;
      const t = OV.totals, hosts = OV.hosts;
      const k = (v, l, cls = '') => `<div class="tv-k ${cls}"><div class="v">${v}</div><div class="l">${l}</div></div>`;
      $('#tv-kpis').innerHTML =
        k(`${t.online}<small>/ ${t.total}</small>`, 'servidores online', t.offline ? 'bad' : '') +
        k(nf(t.alerts_crit), 'alertas críticos', t.alerts_crit ? 'bad' : '') +
        k(nf(t.alerts_warn), 'alertas de atenção', t.alerts_warn ? 'warn' : '') +
        k(`${nf(t.cpu_avg)}<small>%</small>`, 'CPU média') +
        k(`${nf(t.mem_avg)}<small>%</small>`, 'memória média') +
        k(`${nf(t.worst_disk)}<small>%</small>`, t.worst_disk_host ? `disco mais cheio · ${esc(t.worst_disk_host)}` : 'disco mais cheio', lvl(t.worst_disk, 80, 92) === 'crit' ? 'bad' : lvl(t.worst_disk, 80, 92) ? 'warn' : '');

      const g = $('#tv-grid');
      if (!hosts.length) { g.style.cssText = 'display:block'; g.innerHTML = `<div class="tile" style="max-width:760px;margin:6vh auto 0;padding:2vh 2vw">${installHelp()}</div>`; }
      else {
        const lay = tvLayout(hosts.length, g.clientWidth, g.clientHeight);
        const pages = Math.max(1, Math.ceil(hosts.length / lay.per));
        page = page % pages;
        g.style.gridTemplateColumns = `repeat(${lay.cols},1fr)`; g.style.gridTemplateRows = `repeat(${lay.rows},1fr)`;
        g.innerHTML = hosts.slice(page * lay.per, (page + 1) * lay.per).map(tile).join('');
        $('#tv-page').innerHTML = pages > 1 ? `<div class="dots" title="Página ${page + 1} de ${pages}">${Array.from({ length: pages }, (_, i) => `<i class="${i === page ? 'on' : ''}"></i>`).join('')}</div>` : '';
      }
      const al = OV.alerts.filter(a => a.sev !== 'info');
      const shown = al.slice(0, 3);
      $('#tv-foot').innerHTML = shown.map((a, i) => `<div class="t-al ${a.sev}"><b>${esc(a.host)}</b><span>${esc(a.msg)}</span>${i === 2 && al.length > 3 ? `<em class="t-more">+ ${al.length - 3} alertas</em>` : ''}</div>`).join('') ||
        `<div class="t-al none"><span>Nenhum alerta ativo. Tudo dentro dos limites.</span></div>`;
    },
    stop() {
      timers.forEach(clearInterval); timers = [];
      window.removeEventListener('resize', onResize);
      document.removeEventListener('mousemove', wake); document.removeEventListener('keydown', wake);
      clearTimeout(idleT); document.body.classList.remove('tv', 'idle');
      if (document.fullscreenElement) document.exitFullscreen().catch(() => { });
    }
  };
  return self;
}

/* =====================================================================
   Ações (cliques)
   ===================================================================== */
function toggleFS() {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => { });
  else document.documentElement.requestFullscreen?.().catch(() => { });
}
async function copyText(t) {
  try { await navigator.clipboard.writeText(t); return true; } catch { }
  const a = document.createElement('textarea'); a.value = t; a.style.position = 'fixed'; a.style.opacity = '0';
  document.body.appendChild(a); a.select(); let ok = false; try { ok = document.execCommand('copy'); } catch { } a.remove(); return ok;
}

document.addEventListener('click', async e => {
  const b = e.target.closest('[data-act]'); if (!b) return;
  const act = b.dataset.act, v = b.dataset.v;
  if (act === 'tv') { location.hash = '#/tv'; toggleFS(); }
  else if (act === 'exit') { location.hash = '#/'; }
  else if (act === 'fs') toggleFS();
  else if (act === 'range') { hostRange = v; cur.update(); }
  else if (act === 'psort') { procSort = v; cur.update(); }
  else if (act === 'alstate') { alState = v; cur.update(); }
  else if (act === 'glog') { logLevel = v; cur.update(); }
  else if (act === 'hlog') { hostLogLevel = v; $$('#seg-hlog button').forEach(x => x.classList.toggle('on', x.dataset.v === v)); cur.update(); }
  else if (act === 'copyw') { if (await copyText($('#cmdw').textContent)) { b.textContent = 'Copiado'; setTimeout(() => b.textContent = 'Copiar comando Windows', 1500); } }
  else if (act === 'copy') { if (await copyText($('#cmd').textContent)) { b.textContent = 'Copiado'; setTimeout(() => b.textContent = 'Copiar comando', 1500); } }
  else if (act === 'dismiss') {
    if (!confirm('Dispensar este alerta? A porta deixa de ser monitorada até voltar a escutar.')) return;
    await api('/api/alerts/' + b.dataset.id, { method: 'DELETE' }).catch(() => { }); poll();
  } else if (act === 'forget') {
    if (!confirm(`Remover “${b.dataset.host}” do painel? Histórico e alertas deste servidor serão apagados.`)) return;
    await api('/api/hosts/' + encodeURIComponent(b.dataset.host), { method: 'DELETE' }).catch(() => { });
    location.hash = '#/'; poll();
  }
});
document.addEventListener('keydown', e => {
  if (e.target.matches('input,select,textarea')) return;
  if (location.hash === '#/tv') {
    if (e.key === 'f' || e.key === 'F') toggleFS();
    if (e.key === 'Escape' && !document.fullscreenElement) location.hash = '#/';
  }
});

window.addEventListener('hashchange', go);
go();
setInterval(poll, 5000);
})();
