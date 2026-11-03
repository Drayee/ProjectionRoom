#!/usr/bin/env node
/**
 * verify-avatar-crop.mjs —— 票 #75「头像裁剪器 + 频控 + 管理端重置」的真机验收。
 *
 * # 两道门槛，缺一不可
 *
 * 本票的判据散在**浏览器**与**服务端**两侧，任何一侧的假绿都会让整个结论失真：
 *
 *   - 「裁出来的是正方形、尺寸是 512」只有真浏览器能证 —— `toBlob` 的产物尺寸
 *     取决于 canvas 的像素比与 `drawImage` 的取景参数，node 里算不出来；
 *   - 「每天 5 次」是服务端单条 UPDATE 的原子性 + 那三列的状态，
 *     前端最多只能显示一条 429 文案；
 *   - 「管理端重置之后别人立刻看到字母头」跨了管理端、数据库、公开档案三处。
 *
 * 所以判据分两组：**C 组**（裁剪与降级，mock 域就够）与 **S 组**（服务端行为，
 * 必须真后端）。`--server` 不给时只跑 C 组，S 组整组 SKIP —— 明确说没跑，
 * 而不是假装通过。
 *
 * # 判据
 *
 * ## C 组 · 裁剪器（不需要后端）
 *
 *   C1  `/me?tab=settings` 有「更换头像」按钮，点开出现裁剪子窗体
 *       （`role=dialog` + `aria-modal`，与登录页的人机验证窗同族语言）。
 *   C2  子窗体里**没有图**时「用这张」不可点（禁止空导出）。
 *   C3  选一张 **4000×3000 的非方图**后，画布出现，`.meta` 写明原图尺寸。
 *   C4  缩放滑杆推到最大/最小都**不越界**，且画布始终不出错（拖动取景不会跑到空白）。
 *   C5  点「用这张」导出的 blob 是**正方 512×512**、`image/jpeg` 或 `image/webp`、
 *       且 ≤ 1 MiB。这三条是决议 #54 §2 的**服务端前提**：服务端只验不重编码，
 *       客户端不裁成方图就会让所有人的头像被 `object-fit: cover` 二次裁切。
 *   C6  取消（Esc / 取消按钮）**不发任何请求**、不留半个状态：再点「更换头像」
 *       仍能正常打开（没有卡在 busy）。
 *   C7  源码级：`AVATAR_EXPORT_SIZE` 与 `AVATAR_MAX_BYTES` 只在 `utils/avatarCrop.ts`
 *       定义一次；`exportBlob` 只在 `AvatarCropDialog.vue` 里调用（导出逻辑没有第二份）。
 *
 * ## S 组 · 服务端（需要 --server）
 *
 *   S0  实例就绪：两个账号（A 本人、B 旁观者）。A 上传一张头像并 PATCH 成功。
 *   S1  上传链路：`POST /api/uploads` 回 201 + `{url}`，URL 落在配置的对外前缀下。
 *   S2  **外链注入被拒**：`PATCH /api/me {avatarUrl: "https://example.com/x.jpg"}` → 400
 *       `AVATAR_URL_INVALID`（钉住 `OwnsURL` 只认本桶）。
 *   S3  **三态**：只发 `{displayName}` 时头像原封不动（键缺席 ≠ 清除）。
 *   S4  **每日 5 次**：连改 5 次成功，第 6 次 → 429 `RATE_LIMITED`；
 *       且**幂等重复不扣额度**（同 URL 再发几次仍是 200）。
 *   S5  **清除不扣额度**：`{avatarUrl: null}` 若干次全 200，之后还能再改。
 *   S6  **管理端重置**：管理员 `PATCH /api/admin/users/:id {resetAvatar:true}` → 200；
 *       B 视角 `GET /api/users/:id` 里 `avatarUrl` 消失；审计出现 `user.avatar.reset`。
 *   S7  重置是**幂等**的：对没有头像的人再重置一次仍 200（不 404、不 500）。
 *   S8  未配存储时 `POST /api/uploads` 回 404、`PATCH /api/me {avatarUrl}` 回 503 ——
 *       只能靠**不带 PR_STORAGE_* 的第二个实例**验；本脚本用 `--server-no-storage`
 *       可选参数接上，不给则这一条 SKIP。
 *
 * # 实例要求（不满足时明确 SKIP，退出码 2，绝不假绿）
 *
 *   - `PR_OTP_DEV_ECHO=1`（要注册账号）；
 *   - 注册/登录限速放宽（一轮建三个号 + 多次 PATCH）；
 *   - `PR_STORAGE_*` 六件套配齐并已在启动期建好桶（`EnsureBucket`）；
 *   - 前端产物必须是**真实接口**构建（见用法）。
 *
 * 用法：
 *   # 只跑裁剪器（mock 域，不需要后端）
 *   npm --prefix client run build
 *   node test/script/verify-avatar-crop.mjs
 *
 *   # 全跑（真接口构建 + 真后端）
 *   cd client && VITE_MOCK_USERS=0 VITE_MOCK_POSTS=0 VITE_MOCK_FOLLOWS=0 npx vite build
 *   node test/script/verify-avatar-crop.mjs --server http://127.0.0.1:8899
 *
 * 退出码：全部判据 PASS -> 0；任一 FAIL -> 1；环境不满足 -> 2。
 */
import { createServer, request as httpRequest } from 'node:http'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, openTarget, sleep, startChrome, waitFor } from './lib/browser.mjs'

const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const DIST = join(REPO_ROOT, 'client', 'dist')
const SRC = join(REPO_ROOT, 'client', 'src')
const BASE_PORT = Number(argOf(process.argv, 'port', '9912'))
const SERVER = argOf(process.argv, 'server', '').replace(/\/+$/, '')
const NO_STORAGE_SERVER = argOf(process.argv, 'server-no-storage', '').replace(/\/+$/, '')
/** 服务端每日额度（与 `service.DefaultAvatarChangesPerDay` 对齐；实例可用 PR 变量改）。 */
const DAILY_LIMIT = Number(argOf(process.argv, 'limit', '5'))

function argOf(argv, name, fallback) {
  const i = argv.indexOf('--' + name)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}

let passed = 0
let failed = 0
let skipped = 0

function record(name, pass, detail) {
  if (pass) passed += 1
  else failed += 1
  console.log('[' + (pass ? 'PASS' : 'FAIL') + '] ' + name + (detail ? ' — ' + detail : ''))
}

function skipOne(name, reason) {
  skipped += 1
  console.log('[SKIP] ' + name + ' —— ' + reason)
}

function skipEnv(reason) {
  console.error('[SKIP] 环境不满足：' + reason)
  process.exit(2)
}

/** 读源码并**去掉注释**（注释里提 `object-fit` / `exportBlob` 是允许的，那不该算）。 */
function readSource(rel) {
  const text = readFileSync(join(SRC, rel), 'utf8')
  return text
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1')
}

/** 递归列出 `client/src` 下的全部 `.vue` / `.ts`（跳过 node_modules，本目录本来也没有）。 */
function walkSource(dir, out) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walkSource(full, out)
    else if (name.endsWith('.vue') || name.endsWith('.ts') || name.endsWith('.mjs')) out.push(full)
  }
  return out
}

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
}

/**
 * 把 `/api/*` 逐字节反代到真后端。
 *
 * ⚠️ 必须 `http.request` + `req.pipe(upstream)`：`fetch(url, {body: req})` 会静默
 * 丢掉请求体（写操作全 502，只读页面看着正常）。verify-email-change 踩过，照抄修好的版本。
 */
function proxy(req, res) {
  const target = new URL(SERVER)
  const upstream = httpRequest(
    {
      hostname: target.hostname,
      port: target.port,
      path: req.url,
      method: req.method,
      headers: { ...req.headers, host: target.host, origin: SERVER },
    },
    (up) => {
      res.writeHead(up.statusCode == null ? 502 : up.statusCode, up.headers)
      up.pipe(res)
    },
  )
  upstream.on('error', () => {
    if (!res.headersSent) res.writeHead(502, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ error: 'upstream unreachable' }))
  })
  req.pipe(upstream)
}

/**
 * 静态服务器：托管 `client/dist`，SPA 回退 index.html。
 *
 * 给了 `--server` 时 `/api/*` 反代到真后端；没给就一律 404 JSON
 * （mock 域下前端本来不该打这些请求，真打了要立刻失败而不是把 HTML 当 JSON）。
 *
 * **一个例外**：`POST /api/uploads` 在 mock 态回 **400** 而不是 404。
 * 因为 `probeAvatarUpload()`（`api/avatar.ts`）的判据就是状态码 ——
 * 404 = 这条路由没注册 = 本部署没开对象存储 = 头像块只显示能力提示，
 * 「更换头像」按钮根本不会出现。回 400 表示"路由在、只是这个空请求不合法"，
 * 于是裁剪器那一组判据（C1–C7）在**不需要真后端**的情况下也能跑。
 * 这不是在骗自己：C 组判的是裁剪器的行为，而 S 组会在真后端上重跑整条上传链路。
 */
function startStatic(dir, withProxy) {
  return new Promise((done) => {
    const server = createServer((req, res) => {
      const path = new URL(req.url || '/', 'http://127.0.0.1').pathname
      if (path.startsWith('/api/') || path === '/ws') {
        if (withProxy) {
          proxy(req, res)
          return
        }
        if (path === '/api/uploads') {
          res.writeHead(400, { 'Content-Type': 'application/json' })
          res.end('{"error":"mock: uploads route exists, empty request","code":"upload_empty"}')
          return
        }
        res.writeHead(404, { 'Content-Type': 'application/json' })
        res.end('{"error":"not found"}')
        return
      }
      let file = resolve(dir, '.' + path)
      if (file !== dir && !file.startsWith(dir + sep)) {
        res.writeHead(403)
        res.end()
        return
      }
      if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html')
      if (!existsSync(file) || !statSync(file).isFile()) file = join(dir, 'index.html')
      const body = readFileSync(file)
      res.writeHead(200, {
        'Content-Type': MIME[extname(file)] || 'application/octet-stream',
        'Content-Length': body.length,
      })
      res.end(body)
    })
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      done({ url: 'http://127.0.0.1:' + port, close: () => server.close() })
    })
  })
}

/**
 * 生成一张指定尺寸的 PNG 并写进文件选择框。
 *
 * 为什么在页面里画而不是用仓库里的媒体夹具：这里要的是**确定的尺寸**
 * （4000×3000 的非方图），尺寸决定了 `coverScale` 与 `cropRect` 的输入，
 * 随便一张图会让"裁出来正好 512"变成碰巧成立。
 *
 * 走 `DOM.setFileInputFiles` 需要真实路径，所以先用 canvas 的 `toDataURL`
 * 把 PNG 的 base64 拿回来，写到临时目录，再喂给 input。
 */
const MAKE_PNG = (w, h) => `(async () => {
  const c = document.createElement('canvas')
  c.width = ${w}
  c.height = ${h}
  const g = c.getContext('2d')
  const grad = g.createLinearGradient(0, 0, ${w}, ${h})
  grad.addColorStop(0, '#7a4cff')
  grad.addColorStop(0.5, '#1b1230')
  grad.addColorStop(1, '#ff4c9a')
  g.fillStyle = grad
  g.fillRect(0, 0, ${w}, ${h})
  g.fillStyle = '#ffffff'
  g.font = 'bold ' + Math.round(${h} / 6) + 'px sans-serif'
  g.textAlign = 'center'
  g.textBaseline = 'middle'
  g.fillText('AVATAR', ${w} / 2, ${h} / 2)
  return c.toDataURL('image/png')
})()`

/** 读裁剪子窗体的可观测状态。 */
const READ_DIALOG = `(() => {
  const txt = (el) => (el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : '');
  const q = (id) => document.querySelector('[data-testid=' + id + ']');
  const dlg = q('avatar-crop-dialog');
  const canvas = q('avatar-crop-canvas');
  const zoom = q('avatar-crop-zoom');
  const confirm = q('avatar-crop-confirm');
  return {
    dialog: Boolean(dlg),
    modal: dlg ? dlg.getAttribute('aria-modal') : null,
    role: dlg ? dlg.getAttribute('role') : null,
    hasCanvas: Boolean(canvas),
    canvasW: canvas ? canvas.width : 0,
    canvasH: canvas ? canvas.height : 0,
    zoom: zoom ? Number(zoom.value) : null,
    zoomMin: zoom ? Number(zoom.min) : null,
    zoomMax: zoom ? Number(zoom.max) : null,
    confirmDisabled: confirm ? Boolean(confirm.disabled) : null,
    meta: txt(q('avatar-crop-meta')),
    error: txt(q('avatar-crop-error')),
    hasFileInput: Boolean(q('avatar-file-input')),
  };
})()`

/** 读设置区可观测状态。 */
const READ_SETTINGS = `(() => {
  const txt = (el) => (el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : '');
  const q = (id) => document.querySelector('[data-testid=' + id + ']');
  const change = q('avatar-change');
  const remove = q('avatar-remove');
  return {
    path: location.pathname + location.search,
    hasChange: Boolean(change),
    changeDisabled: change ? Boolean(change.disabled) : null,
    hasRemove: Boolean(remove),
    unavailable: txt(q('avatar-unavailable')),
    notice: txt(q('avatar-notice')),
    failure: txt(q('avatar-failure')),
    hasRetry: Boolean(q('avatar-retry-save')),
    pics: document.querySelectorAll('.avatar-pic').length,
    scrollY: window.scrollY,
  };
})()`

/** 往输入框写值（原生 setter + input 事件，`v-model` 才认）。 */
async function fillInput(cdp, selector, value) {
  await cdp.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)})
    if (!el) return 'NO-EL'
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
    setter.call(el, ${JSON.stringify(value)})
    el.dispatchEvent(new Event('input', { bubbles: true }))
    return 'ok'
  })()`)
}

/**
 * 解人机验证题面（按顺序点图形）。
 *
 * 与 `verify-forgot.mjs` 同一份实现：题面与校验都在前端组件里
 * （`CaptchaChallenge.vue`，目标图形的中文名就在 `.order-item .sr` 里），
 * 所以这不是"破解"，是像人一样照题面点。它的定位本来就是**摩擦**，不是安全边界。
 */
async function solveCaptcha(cdp) {
  const names = await cdp.evaluate(`(() => {
    const d = document.querySelector('[data-testid=captcha-dialog]')
    if (!d) return null
    return [...d.querySelectorAll('.order-item .sr')].map((s) => s.textContent.trim())
  })()`)
  if (!names || names.length === 0) return false
  for (const name of names) {
    const r = await cdp.evaluate(`(() => {
      const cell = [...document.querySelectorAll('[data-testid=captcha-dialog] .cell')]
        .find((c) => (c.getAttribute('aria-label') || '').includes(${JSON.stringify(name)}))
      if (!cell) return 'NO-CELL'
      cell.click()
      return 'ok'
    })()`)
    if (r === 'NO-CELL') return false
    await sleep(120)
  }
  return true
}

// ---------------------------------------------------------------------------
// C 组：裁剪器（mock 域即可）
// ---------------------------------------------------------------------------

const SEED_PROFILE = {
  id: 1,
  username: 'mooncat',
  displayName: '月夜猫',
  email: '',
  role: 'user',
  status: 'active',
  createdAt: '2023-10-18T09:00:00Z',
  unread: 0,
  unreadKnown: false,
}

async function runCropGroup() {
  const chromePath = findChrome()
  if (!chromePath) skipEnv('找不到 Chrome（设置 CHROME_PATH）')
  if (!existsSync(join(DIST, 'index.html'))) skipEnv('client/dist 不存在，先 npm --prefix client run build')

  // C 组**永远**跑 mock 静态服务器（不反代），即使给了 `--server`。
  //
  // 理由：C 组种的是假会话（假 token + 假 profile），一但把 `/api/*` 反代到真后端，
  // `/api/auth/me` 会回 401 → 前端按约定清掉会话 → 设置区根本不渲染头像块，
  // 于是 C 组会以"设置区没出现"这种与裁剪器无关的原因红掉。
  // 真后端那条链路由 S 组用真实账号的 token 直接打接口来判，两组各判各的。
  const site = await startStatic(DIST, false)
  const chrome = await startChrome(chromePath, BASE_PORT, 'avatar-crop')
  let cdp
  try {
    cdp = await openTarget(chrome.port, 'avatar-crop')
    await cdp.send('Page.enable')
    await cdp.send('Runtime.enable')

    // 种子会话：mock 域下 /api/* 全 404，只有明确 401 才清会话，所以登录态成立。
    await cdp.navigate(site.url + '/')
    await cdp.evaluate(
      'sessionStorage.setItem("pr:access", "pr-verify-crop-token");' +
        'localStorage.setItem("pr:profile", ' +
        JSON.stringify(JSON.stringify(SEED_PROFILE)) +
        '); "ok"',
    )
    await cdp.navigate(site.url + '/me?tab=settings')
    await waitFor(
      async () => {
        const s = await cdp.evaluate(READ_SETTINGS)
        return s.hasChange || s.unavailable !== ''
      },
      { label: '设置区渲染出头像块', timeoutMs: 25000 },
    )
    await sleep(300)

    const settings = await cdp.evaluate(READ_SETTINGS)

    // C1 —— 有入口，点开是模态子窗体
    record(
      'C1 设置区有「更换头像」入口，点开是 aria-modal 子窗体',
      settings.hasChange && settings.changeDisabled === false,
      `hasChange=${settings.hasChange} disabled=${settings.changeDisabled}`,
    )
    await cdp.evaluate(`document.querySelector('[data-testid=avatar-change]').click()`)
    await waitFor(
      async () => {
        const d = await cdp.evaluate(READ_DIALOG)
        return d.dialog
      },
      { label: '裁剪子窗体出现', timeoutMs: 15000 },
    )
    const opened = await cdp.evaluate(READ_DIALOG)
    record(
      'C1b 子窗体是 role=dialog + aria-modal=true',
      opened.dialog && opened.role === 'dialog' && opened.modal === 'true',
      `role=${opened.role} modal=${opened.modal}`,
    )

    // C2 —— 没图时不能导出
    record(
      'C2 没有选图时「用这张」不可点',
      opened.confirmDisabled === true,
      `confirmDisabled=${opened.confirmDisabled}`,
    )

    // C3 —— 选一张 4000×3000 的非方图，画布出现且 meta 说原图尺寸
    const dataUrl = await cdp.evaluate(MAKE_PNG(4000, 3000))
    const pngPath = join(REPO_ROOT, '_crop_fixture.png')
    // PNG 的 base64 直接落盘（node 内置 Buffer，不需要额外依赖）
    const { writeFileSync } = await import('node:fs')
    writeFileSync(pngPath, Buffer.from(String(dataUrl).split(',')[1], 'base64'))
    // ⚠️ 用 `DOM.setFileInputFiles` 精确喂**这一个文件**，不要用
    // `cdp.uploadDirectory(selector, dir)` —— 那个会把目录里的所有文件都塞进
    // input，而这里要的恰好是一张已知尺寸的图（塞进仓库根目录 = 把整个仓当头像候选）。
    const { root } = await cdp.send('DOM.getDocument', { depth: -1 })
    const { nodeId } = await cdp.send('DOM.querySelector', {
      nodeId: root.nodeId,
      selector: '[data-testid=avatar-file-input]',
    })
    await cdp.send('DOM.setFileInputFiles', { nodeId, files: [pngPath] })
    await waitFor(
      async () => {
        const d = await cdp.evaluate(READ_DIALOG)
        return d.hasCanvas
      },
      { label: '选图后画布出现', timeoutMs: 20000 },
    )
    const withImage = await cdp.evaluate(READ_DIALOG)
    record(
      'C3 选 4000×3000 的图后画布出现，meta 写明原图尺寸',
      withImage.hasCanvas && withImage.meta.includes('4000') && withImage.meta.includes('3000'),
      `canvas=${withImage.canvasW}x${withImage.canvasH} meta=${withImage.meta}`,
    )

    // C4 —— 缩放推到两端都不出错
    await cdp.evaluate(`(() => {
      const el = document.querySelector('[data-testid=avatar-crop-zoom]')
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(el, el.max)
      el.dispatchEvent(new Event('input', { bubbles: true }))
      return 'ok'
    })()`)
    await sleep(150)
    const atMax = await cdp.evaluate(READ_DIALOG)
    await cdp.evaluate(`(() => {
      const el = document.querySelector('[data-testid=avatar-crop-zoom]')
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
      setter.call(el, el.min)
      el.dispatchEvent(new Event('input', { bubbles: true }))
      return 'ok'
    })()`)
    await sleep(150)
    const atMin = await cdp.evaluate(READ_DIALOG)
    record(
      'C4 缩放推到最大/最小都不越界、不报错',
      atMax.zoom === atMax.zoomMax &&
        atMin.zoom === atMin.zoomMin &&
        atMax.error === '' &&
        atMin.error === '' &&
        atMax.hasCanvas &&
        atMin.hasCanvas,
      `max=${atMax.zoom}/${atMax.zoomMax} min=${atMin.zoom}/${atMin.zoomMin} err=${atMax.error || atMin.error}`,
    )

    // C5 —— 导出的是正方 512、jpeg/webp、≤1 MiB
    //
    // 判据不能读 canvas 的显示尺寸（那个是 288 的预览画布），要拦 `toBlob` 的产物。
    // 做法：在页面里打一次 fetch 钩子把 blob 的类型与尺寸记下来 —— `exportBlob` 之后
    // 走的是 `uploadAvatarBlob` → `authedFetch('/api/uploads')`，钩住它就能看到真实的
    // 上传体，而不必真的把图传上去（mock 域下那条请求本来就 404）。
    await cdp.evaluate(`(() => {
      window.__cropProbe = null
      window.__uploadCalls = 0
      const orig = window.fetch
      window.fetch = async function (input, init) {
        const url = typeof input === 'string' ? input : (input && input.url) || ''
        if (url.includes('/api/uploads') && init && init.method === 'POST') {
          window.__uploadCalls = (window.__uploadCalls || 0) + 1
          if (init.body instanceof FormData) {
            const file = init.body.get('file')
            if (file) {
              const bmp = await createImageBitmap(file)
              window.__cropProbe = { type: file.type, size: file.size, width: bmp.width, height: bmp.height }
            }
          }
        }
        return orig.apply(this, arguments)
      }
      return 'ok'
    })()`)
    await cdp.evaluate(`document.querySelector('[data-testid=avatar-crop-confirm]').click()`)
    // 导出之后**先弹人机验证**（决议 #54 §3 的审核四件套之一）：上传是第二次往返，
    // 也正因为如此，"点了用这张"之后 DOM 上先是验证窗、不是请求。
    await waitFor(
      async () => (await cdp.evaluate(`Boolean(document.querySelector('[data-testid=captcha-dialog]'))`)),
      { label: '裁剪确认后弹出人机验证', timeoutMs: 20000 },
    )
    record('C5a 裁剪确认后先弹人机验证子窗体（不是直接上传）', true)
    const solved = await solveCaptcha(cdp)
    record('C5b 按题面点完图形（子窗体自行 pass）', solved, `solveCaptcha=${solved}`)
    await waitFor(
      async () => (await cdp.evaluate('Boolean(window.__cropProbe)')),
      { label: '导出并进入上传', timeoutMs: 25000 },
    )
    const probe = await cdp.evaluate('window.__cropProbe')
    record(
      'C5 导出的是正方 512×512、jpeg/webp、≤1 MiB',
      probe.width === 512 &&
        probe.height === 512 &&
        (probe.type === 'image/jpeg' || probe.type === 'image/webp') &&
        probe.size <= 1024 * 1024,
      `${probe.width}x${probe.height} ${probe.type} ${probe.size}B`,
    )

    // C6 —— 取消**不发请求**、不留半个状态
    //
    // 判据要能区分"没发"与"发了但失败"：所以数一次 `/api/uploads` 的调用次数，
    // 然后走一遍"打开裁剪窗 → 选图 → Esc 取消"，号码必须**不变**。
    //
    // 取消之后还必须能再打开（没有卡在 busy）：那正是"取消"这条路最容易漏掉的一步，
    // 漏了它症状是"取消一次之后按钮就再没反应了"。
    const uploadCallsBefore = await cdp.evaluate('window.__uploadCalls || 0')
    await cdp.evaluate(`document.querySelector('[data-testid=avatar-change]').click()`)
    await waitFor(
      async () => (await cdp.evaluate(READ_DIALOG)).dialog,
      { label: '再次打开裁剪子窗体', timeoutMs: 15000 },
    )
    // 选一张（不选图时"取消"其实什么都没做，测不出状态残留）
    const smallPath = join(REPO_ROOT, '_crop_cancel_fixture.png')
    {
      const { writeFileSync: wfs } = await import('node:fs')
      const small = await cdp.evaluate(MAKE_PNG(1200, 800))
      wfs(smallPath, Buffer.from(String(small).split(',')[1], 'base64'))
    }
    {
      const { root: r2 } = await cdp.send('DOM.getDocument', { depth: -1 })
      const { nodeId: n2 } = await cdp.send('DOM.querySelector', {
        nodeId: r2.nodeId,
        selector: '[data-testid=avatar-file-input]',
      })
      await cdp.send('DOM.setFileInputFiles', { nodeId: n2, files: [smallPath] })
    }
    await waitFor(
      async () => (await cdp.evaluate(READ_DIALOG)).hasCanvas,
      { label: '取消用例的图已载入', timeoutMs: 20000 },
    )
    // Esc 关窗（与登录页的人机验证窗同一套键位约定）
    await cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
    await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
    await sleep(400)
    const uploadCallsAfter = await cdp.evaluate('window.__uploadCalls || 0')
    const closedByEsc = !(await cdp.evaluate(READ_DIALOG)).dialog
    const reopened = await cdp.evaluate(READ_SETTINGS)
    record(
      'C6 取消（Esc）不发任何请求，且之后仍能再次打开裁剪器',
      closedByEsc && uploadCallsAfter === uploadCallsBefore && reopened.hasChange && reopened.changeDisabled === false,
      `closed=${closedByEsc} calls=${uploadCallsBefore}→${uploadCallsAfter} disabled=${reopened.changeDisabled}`,
    )
    // 清掉取消用例的临时夹具
    try {
      const { unlinkSync } = await import('node:fs')
      unlinkSync(smallPath)
    } catch {
      // 忽略
    }

    // C7 —— 源码级：导出尺寸与上限只有一处定义
    const cropSrc = readSource('utils/avatarCrop.ts')
    const dialogSrc = readSource('components/features/profile/AvatarCropDialog.vue')
    const defs = [...cropSrc.matchAll(/AVATAR_EXPORT_SIZE\s*=\s*512/g)].length
    const capDefs = [...cropSrc.matchAll(/AVATAR_MAX_BYTES\s*=\s*1024\s*\*\s*1024/g)].length
    record(
      'C7 源码级：512 与 1 MiB 只在 utils/avatarCrop.ts 定义一次',
      defs === 1 && capDefs === 1 && dialogSrc.includes('AVATAR_EXPORT_SIZE') && dialogSrc.includes('AVATAR_MAX_BYTES'),
      `exportSizeDefs=${defs} maxBytesDefs=${capDefs}`,
    )

    const allVue = walkSource(join(SRC), []).filter((f) => f.endsWith('.vue'))
    const exporters = allVue.filter((f) => readSource(f.slice(SRC.length + 1).split(sep).join('/')).includes('toBlob('))
    record(
      'C7b 源码级：canvas 导出只有 AvatarCropDialog 一处',
      exporters.length === 1 && exporters[0].endsWith('AvatarCropDialog.vue'),
      `exporters=${exporters.map((f) => f.slice(SRC.length + 1).replace(/\\/g, '/')).join(',')}`,
    )

    // 清掉临时夹具
    try {
      const { unlinkSync } = await import('node:fs')
      unlinkSync(pngPath)
    } catch {
      // 忽略
    }
  } finally {
    // ⚠️ 两处都要关：`startChrome` 返回的对象**没有** `kill`，写 `chrome.kill?.()` 是
    // 静默 no-op，脚本会跑完全部判据后挂住不退（verify-avatar-overlay 踩过）。
    if (cdp) cdp.close()
    chrome.close()
    site.close()
  }
}

// ---------------------------------------------------------------------------
// S 组：服务端（需要 --server）
// ---------------------------------------------------------------------------

function freshUsername(prefix) {
  return prefix + Math.random().toString(36).slice(2, 8)
}

/** 走完整两段式注册建号（需要实例开 PR_OTP_DEV_ECHO=1）。 */
async function registerAccount(prefix, password) {
  const username = freshUsername(prefix)
  const reg = await fetch(SERVER + '/api/auth/register/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ username, password }),
  })
  if (!reg.ok) skipEnv('注册失败（HTTP ' + reg.status + '）：' + (await reg.text()))
  const regStart = await reg.json()
  const email = username + '@example.test'
  const sent = await fetch(SERVER + '/api/auth/register/email', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, email }),
  })
  if (!sent.ok) skipEnv('发验证码失败（HTTP ' + sent.status + '）：需要 PR_OTP_DEV_ECHO=1 的实例')
  const sentBody = await sent.json().catch(() => ({}))
  const code = sentBody.devCode || sentBody.dev_code || ''
  if (!code) skipEnv('响应里没有 devCode —— 实例没开 PR_OTP_DEV_ECHO=1，拿不到验证码')
  const verified = await fetch(SERVER + '/api/auth/register/verify', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ ticket: regStart.ticket, code }),
  })
  if (!verified.ok) skipEnv('建号失败（HTTP ' + verified.status + '）')
  const account = await verified.json()
  if (!account.user || !account.user.id) skipEnv('注册响应里没有 user.id')
  return { username, password, email, user: account.user, accessToken: account.accessToken }
}

/** 带凭据发请求，返回 `{status, body}`（解析失败给 `{raw}`）。 */
async function api(token, method, path, payload) {
  const headers = {}
  if (token) headers.Authorization = 'Bearer ' + token
  const init = { method, headers }
  if (payload !== undefined) {
    headers['Content-Type'] = 'application/json'
    headers['Idempotency-Key'] = crypto.randomUUID()
    init.body = JSON.stringify(payload)
  }
  const r = await fetch(SERVER + path, init)
  const text = await r.text()
  let body = {}
  try {
    body = text ? JSON.parse(text) : {}
  } catch {
    body = { raw: text }
  }
  return { status: r.status, body }
}

/**
 * 造一张真实可解码的 JPEG（走页面里的 canvas，保证服务端的 MIME 嗅探认它）。
 *
 * 不在 node 里拼 JPEG 字节：那要么引入图像库（本仓零图像依赖），要么手写
 * 一段不可维护的 base64。页面里画一张再导出来最省事，且与真实用户路径一致。
 */
async function makeJpegBytes(cdp) {
  const dataUrl = await cdp.evaluate(`(async () => {
    const c = document.createElement('canvas')
    c.width = 512
    c.height = 512
    const g = c.getContext('2d')
    const grad = g.createLinearGradient(0, 0, 512, 512)
    grad.addColorStop(0, '#7a4cff')
    grad.addColorStop(1, '#ff4c9a')
    g.fillStyle = grad
    g.fillRect(0, 0, 512, 512)
    return c.toDataURL('image/jpeg', 0.9)
  })()`)
  return Buffer.from(String(dataUrl).split(',')[1], 'base64')
}

/** 直接 multipart 上传一张 JPEG（字段名必须是 `file`）。 */
async function uploadAvatar(token, bytes) {
  const form = new FormData()
  form.append('file', new Blob([bytes], { type: 'image/jpeg' }), 'avatar.jpg')
  const r = await fetch(SERVER + '/api/uploads', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + token },
    body: form,
  })
  const text = await r.text()
  let body = {}
  try {
    body = text ? JSON.parse(text) : {}
  } catch {
    body = { raw: text }
  }
  return { status: r.status, body }
}

async function runServerGroup(cdp) {
  // S0 —— 两个账号：A 本人、B 旁观者
  const password = 'Avatar#2026pass'
  const a = await registerAccount('avcrop', password)
  const b = await registerAccount('avwatch', password)
  record('S0 两个账号就绪', Boolean(a.user.id && b.user.id), `A=#${a.user.id} B=#${b.user.id}`)

  // S1 —— 上传链路
  const jpeg = await makeJpegBytes(cdp)
  const up = await uploadAvatar(a.accessToken, jpeg)
  const url = typeof up.body.url === 'string' ? up.body.url : ''
  record(
    'S1 POST /api/uploads 回 201 + {url}',
    up.status === 201 && url !== '',
    `status=${up.status} url=${url}`,
  )
  if (status_is_503(up.status)) {
    skipOne('S2 外链注入被拒', '本实例未启用对象存储（上传 503）')
    return
  }

  // S2 —— 外链注入被拒
  const foreign = await api(a.accessToken, 'PATCH', '/api/me', {
    avatarUrl: 'https://example.com/evil.jpg',
  })
  record(
    'S2 外链地址被拒（400 AVATAR_URL_INVALID）',
    foreign.status === 400 && (foreign.body.code === 'AVATAR_URL_INVALID' || String(foreign.body.error || '').includes('不合法')),
    `status=${foreign.status} code=${foreign.body.code || ''}`,
  )

  // 设上真头像（后面 S3/S4 都要一个非空的基线）
  const setFirst = await api(a.accessToken, 'PATCH', '/api/me', { avatarUrl: url })
  record('S1b PATCH /api/me 设上刚上传的地址', setFirst.status === 200 && Boolean(setFirst.body.user && setFirst.body.user.avatarUrl), `status=${setFirst.status}`)

  // S3 —— 三态：只发 displayName 时头像原封不动
  const before = await api(a.accessToken, 'GET', '/api/auth/me')
  const nameOnly = await api(a.accessToken, 'PATCH', '/api/me', { displayName: '裁剪测试' })
  const keptUrl = nameOnly.body.user && nameOnly.body.user.avatarUrl
  record(
    'S3 只改昵称时头像不被清掉（键缺席 ≠ 清除）',
    nameOnly.status === 200 && keptUrl === url && Boolean(before.body.user && before.body.user.avatarUrl),
    `status=${nameOnly.status} kept=${Boolean(keptUrl)}`,
  )

  // S4 —— 幂等重复不扣额度：同 URL 连发 3 次都应 200
  let repeatOk = true
  for (let i = 0; i < 3; i += 1) {
    const again = await api(a.accessToken, 'PATCH', '/api/me', { avatarUrl: url })
    if (again.status !== 200) repeatOk = false
  }
  record('S4a 同一个地址重复设置不扣额度（3 次都 200）', repeatOk)

  // S4b —— 每日额度：**本次会话累计**成功 DAILY_LIMIT 次之后，下一次变更 429
  //
  // 计数起点是 1 而不是 0：上面 S1b 已经成功改过一次、真真切切花掉了一个额度。
  // 按"本段成功几次"算会把这笔前置消耗算漏（第一版就是这么红的：ok=4/5）。
  // 判据用"累计成功次数"，因为它才是服务端真正在数的那件事。
  let okCount = 1 // S1b 那次
  let tooMany = { status: 0, body: {} }
  let lastStatus = 200
  for (let guard = 0; guard < DAILY_LIMIT + 2; guard += 1) {
    const next = await uploadAvatar(a.accessToken, jpeg)
    const nextUrl = typeof next.body.url === 'string' ? next.body.url : ''
    const r = await api(a.accessToken, 'PATCH', '/api/me', { avatarUrl: nextUrl })
    lastStatus = r.status
    if (r.status === 200) {
      okCount += 1
      continue
    }
    tooMany = r
    break
  }
  record(
    `S4b 每日额度：累计 ${DAILY_LIMIT} 次全过、第 ${DAILY_LIMIT + 1} 次 429`,
    okCount === DAILY_LIMIT && tooMany.status === 429 && tooMany.body.code === 'RATE_LIMITED',
    `ok=${okCount}/${DAILY_LIMIT} over=${tooMany.status} code=${tooMany.body.code || ''} lastOk=${lastStatus}`,
  )

  // S5 —— 清除不扣额度：额度用满之后清除仍应 200
  const clear1 = await api(a.accessToken, 'PATCH', '/api/me', { avatarUrl: null })
  const clear2 = await api(a.accessToken, 'PATCH', '/api/me', { avatarUrl: null })
  record(
    'S5 额度用满后清除头像仍然 200（清除不扣额度）',
    clear1.status === 200 && clear2.status === 200,
    `first=${clear1.status} second=${clear2.status}`,
  )

  // 为 S6 准备：当前 A 没有头像 —— 先设一张（额度已满，所以换 B 来当 S6 的主体）
  const bUpload = await uploadAvatar(b.accessToken, jpeg)
  const bUrl = typeof bUpload.body.url === 'string' ? bUpload.body.url : ''
  const bSet = await api(b.accessToken, 'PATCH', '/api/me', { avatarUrl: bUrl })
  record('S6a B 设上头像（管理端重置的前置）', bSet.status === 200 && Boolean(bSet.body.user && bSet.body.user.avatarUrl), `status=${bSet.status}`)

  // B 视角能看到自己的头像；A 视角（旁观者）也应看到
  const seen = await api(a.accessToken, 'GET', '/api/users/' + b.user.id)
  record(
    'S6b 旁人视角的公开档案带着这个地址',
    seen.status === 200 && seen.body.avatarUrl === bUrl,
    `status=${seen.status} url=${seen.body.avatarUrl || ''}`,
  )

  // S6c —— 管理端重置（需要管理员凭据）
  const adminToken = await ensureAdminToken(password)
  if (!adminToken) {
    skipOne('S6c 管理端重置头像', '拿不到管理员凭据（--admin <文件> 或实例里没有可提权的账号）')
    skipOne('S6d 重置后旁人立刻看到字母头', '同上')
    skipOne('S7 重置幂等', '同上')
  } else {
    const reset = await api(adminToken, 'PATCH', '/api/admin/users/' + b.user.id, {
      resetAvatar: true,
    })
    record('S6c PATCH {resetAvatar:true} 回 200', reset.status === 200, `status=${reset.status}`)
    const afterReset = await api(a.accessToken, 'GET', '/api/users/' + b.user.id)
    record(
      'S6d 重置后旁人立刻看不到那个地址',
      afterReset.status === 200 && !afterReset.body.avatarUrl,
      `url=${afterReset.body.avatarUrl || '(空)'}`,
    )
    const resetAgain = await api(adminToken, 'PATCH', '/api/admin/users/' + b.user.id, {
      resetAvatar: true,
    })
    record('S7 对没有头像的人再重置一次仍 200（幂等）', resetAgain.status === 200, `status=${resetAgain.status}`)
  }

  // S8 —— 未配存储的实例（可选第二个实例）
  if (NO_STORAGE_SERVER === '') {
    skipOne('S8 未配存储时 /api/uploads 404 且带值 PATCH 503', '没给 --server-no-storage')
  } else {
    const noStoreProbe = await fetch(NO_STORAGE_SERVER + '/api/uploads', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + a.accessToken },
      body: new FormData(),
    })
    const noStorePatch = await fetch(NO_STORAGE_SERVER + '/api/me', {
      method: 'PATCH',
      headers: { Authorization: 'Bearer ' + a.accessToken, 'Content-Type': 'application/json' },
      body: JSON.stringify({ avatarUrl: 'https://cdn.example.test/pr-files/2026/11/' + 'a'.repeat(32) + '.jpg' }),
    })
    record(
      'S8 未配存储：/api/uploads 404、带值 PATCH 503',
      noStoreProbe.status === 404 && noStorePatch.status === 503,
      `uploads=${noStoreProbe.status} patch=${noStorePatch.status}`,
    )
  }
}

function status_is_503(status) {
  return status === 503
}

/**
 * 拿管理员凭据：优先 `--admin <文件>`（一个 JSON，含 accessToken），
 * 否则尝试让本次注册的 A 提权（**只有本机测试库才有这条路**，生产不该有）。
 *
 * 刻意不在脚本里直连数据库：那会让「验收」依赖 psql 路径，而 CI 上没有它。
 * 所以缺凭据时如实 SKIP，把判断留给跑脚本的人。
 */
async function ensureAdminToken() {
  const file = argOf(process.argv, 'admin', '')
  if (!file || !existsSync(file)) return ''
  try {
    const parsed = JSON.parse(readFileSync(file, 'utf8'))
    if (typeof parsed.accessToken === 'string' && parsed.accessToken !== '') return parsed.accessToken
    if (typeof parsed.token === 'string' && parsed.token !== '') return parsed.token
  } catch {
    return ''
  }
  return ''
}

// ---------------------------------------------------------------------------

async function main() {
  console.log('--- C 组：裁剪器（mock 域）---')
  await runCropGroup()

  if (SERVER === '') {
    console.log('--- S 组：服务端（未给 --server，整组跳过）---')
    skipOne('S0–S8 服务端行为', '没给 --server')
  } else {
    console.log('--- S 组：服务端（' + SERVER + '）---')
    const chromePath = findChrome()
    if (!chromePath) skipEnv('找不到 Chrome（S 组要用页面里的 canvas 造一张可解码的 JPEG）')
    const chrome = await startChrome(chromePath, BASE_PORT + 1, 'avatar-crop-jpeg')
    let cdp
    try {
      cdp = await openTarget(chrome.port, 'avatar-crop-jpeg')
      await cdp.send('Page.enable')
      await cdp.send('Runtime.enable')
      await cdp.navigate('about:blank')
      // canvas 在 about:blank 里也能用（不需要同源），只是不能读跨源像素 —— 我们不读。
      await runServerGroup(cdp)
    } finally {
      if (cdp) cdp.close()
      chrome.close()
    }
  }

  console.log(
    '\n结果：PASS ' + passed + ' / FAIL ' + failed + (skipped ? ' / SKIP ' + skipped : ''),
  )
  process.exit(failed > 0 ? 1 : 0)
}

main().catch((err) => {
  console.error('[FATAL] ' + (err && err.stack ? err.stack : err))
  process.exit(1)
})
