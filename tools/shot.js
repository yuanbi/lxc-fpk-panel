// 用 CDP 驱动 Edge headless 截图：可以等条件、点按钮、按需截全页。
// 比 --virtual-time-budget 可靠（虚拟时间会把异步请求掐断）。
//
// 用法:
//   node tools/shot.js <url> <out.png> [w] [h] [preExpr] [readyExpr] [clickSel] [waitExpr]
//
// 例:
//   node tools/shot.js http://127.0.0.1:8901/index.html out.png 1280 1000 \
//     "window.__openContainer('debian-bookworm')" \
//     "document.querySelector('#btnLoginCheck')" "#btnLoginCheck" \
//     "document.querySelector('#loginBox .login-chips')"
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');

const EDGE = process.env.EDGE_PATH || 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function getJSON(url) {
  return new Promise((resolve, reject) => {
    http.get(url, (res) => {
      let b = '';
      res.on('data', (c) => (b += c));
      res.on('end', () => { try { resolve(JSON.parse(b)); } catch (e) { reject(e); } });
    }).on('error', reject);
  });
}

async function main() {
  const [url, out, wArg, hArg, preExpr, readyExpr, clickSel, waitExpr] = process.argv.slice(2);
  if (!url || !out) {
    console.error('用法: node shot.js <url> <out.png> [w] [h] [preExpr] [readyExpr] [clickSel] [waitExpr]');
    process.exit(2);
  }
  const width = parseInt(wArg || '1280', 10);
  const height = parseInt(hArg || '1000', 10);
  const port = 9300 + Math.floor(Math.random() * 200);
  const profile = path.join(os.tmpdir(), 'edge-shot-' + Date.now());

  const proc = spawn(EDGE, [
    '--headless=new', '--no-proxy-server', '--disable-gpu', '--hide-scrollbars',
    '--remote-debugging-port=' + port,
    '--user-data-dir=' + profile,
    '--window-size=' + width + ',' + height,
    url,
  ], { stdio: 'ignore' });

  try {
    for (let i = 0; i < 80; i++) {
      try { await getJSON(`http://127.0.0.1:${port}/json/version`); break; } catch (_) { await sleep(150); }
    }
    let page = null;
    for (let i = 0; i < 80; i++) {
      const list = await getJSON(`http://127.0.0.1:${port}/json/list`);
      page = list.find((t) => t.type === 'page' && /^https?:/.test(t.url));
      if (page) break;
      await sleep(150);
    }
    if (!page) throw new Error('找不到页面 target');

    const ws = new WebSocket(page.webSocketDebuggerUrl);
    const pending = new Map();
    let seq = 0;
    const send = (method, params) => new Promise((resolve, reject) => {
      const id = ++seq;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ id, method, params: params || {} }));
    });
    ws.addEventListener('message', (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (_) { return; }
      const p = pending.get(m.id);
      if (!p) return;
      pending.delete(m.id);
      if (m.error) p.reject(new Error(m.error.message));
      else p.resolve(m.result);
    });
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve);
      ws.addEventListener('error', reject);
    });

    await send('Page.enable');
    await send('Runtime.enable');
    await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });

    const evalJs = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.exceptionDetails) throw new Error('JS 错误: ' + (r.exceptionDetails.text || '') + ' ' + expr);
      return r.result ? r.result.value : undefined;
    };
    const waitFor = async (expr, label) => {
      for (let i = 0; i < 120; i++) {
        if (await evalJs(`!!(${expr})`)) return true;
        await sleep(180);
      }
      console.error('警告：等待超时 -> ' + label);
      return false;
    };

    await sleep(400);
    if (readyExpr) await waitFor(readyExpr, readyExpr);
    if (preExpr) { await evalJs(preExpr); await sleep(600); }
    if (clickSel) {
      for (const sel of clickSel.split('|').filter(Boolean)) {
        const hit = await evalJs(`(function(){var e=document.querySelector(${JSON.stringify(sel)});if(e){e.click();return true}return false})()`);
        if (!hit) console.error('警告：没找到 ' + sel);
        await sleep(1200);
      }
    }
    if (waitExpr) await waitFor(waitExpr, waitExpr);
    await sleep(600);

    const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
    fs.writeFileSync(out, Buffer.from(shot.data, 'base64'));
    console.log('saved ' + out);
    ws.close();
  } finally {
    try { proc.kill(); } catch (_) { /* ignore */ }
    await sleep(400);
    try { fs.rmSync(profile, { recursive: true, force: true }); } catch (_) { /* ignore */ }
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error(e.message || e); process.exit(1); });
