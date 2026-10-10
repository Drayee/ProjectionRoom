// 邮件链接里的一次性 token 的**唯一**读取点。
//
// 链接形态（服务端冻结的契约）：
//   <域名>/reset#token=<一次性串>
//   <域名>/verify#token=<一次性串>
//
// 为什么 token 放在 **fragment** 里：fragment 不会跟着 HTTP 请求发给服务器，
// 因此不会进 nginx 的访问日志（那是部署上唯一一处会记录完整 URL 的地方）。
//
// 这也解释了本文件为什么必须存在：`router.ts` 用的是 `createWebHistory()`，
// **vue-router 不消费 fragment**（浏览器的 hash 路由才消费）。所以路由匹配到的只是
// `/reset`，`#token=…` 得由页面自己在挂载时解析。
//
// 三条纪律（与需求里的安全要求一一对应）：
//   1. **只从 fragment 读**，不读查询串（查询串会进访问日志与 Referer）；
//   2. **取到后立刻把 hash 抹掉**（`history.replaceState`）—— 否则用户刷新、截图、
//      或把地址栏复制给别人，等于把一次性凭据一起送出去；
//   3. 抹掉之后 token 只活在**内存**里：写进 `localStorage`/`sessionStorage` 等于
//      让它在浏览器里原地过期（还可能是共享终端），进 `console` 等于进崩溃上报/录屏。
//      提交时它只出现在**请求体**里，绝不拼进任何 URL。

/** fragment 里承载一次性 token 的键名（`#token=…`）。 */
export const FRAGMENT_TOKEN_KEY = 'token'

/**
 * 只声明用到的那几项窗口能力，而不是直接用全局 `window`：
 * 这样这个模块在单测里可以喂假的实现进来，不必真的起一个页面。
 */
export interface HashLocation {
  hash: string
  pathname: string
  search: string
}

/** 把 `#token=abc&x=1` 这串 fragment 正文解析成 token（空串 = 里面没有可用 token）。 */
export function parseFragmentToken(fragment: string): string {
  const hash = fragment.startsWith('#') ? fragment.slice(1) : fragment
  if (hash === '') return ''
  for (const part of hash.split('&')) {
    const eq = part.indexOf('=')
    if (eq < 0) continue
    if (part.slice(0, eq) !== FRAGMENT_TOKEN_KEY) continue
    const value = part.slice(eq + 1)
    if (value === '') return ''
    try {
      return decodeURIComponent(value)
    } catch {
      // 解不开就退回原样：宁可让服务端判"token 非法"、把文案给它，也不在这里把
      // 链接内容吞掉（吞掉之后用户只会看到一个"链接不完整"，查不出原因）。
      return value
    }
  }
  return ''
}

/**
 * 从 `performance.getEntriesByType('navigation')[0].name` 读 token。
 *
 * **本批没有调用点**，保留它只为留一条"应用自身已经把地址栏 fragment 归一化掉"时的兜底来源：
 * 真机上应用初始化早于 `load`，`onMounted` 里读 `location.hash` 有可能已经是空串。
 * 当前两个页面直接从 `location.hash` / `hashchange.newURL` 读（真机 Chrome + CDP 实测都拿得到），
 * 所以这条兜底暂时用不上 —— 一旦出现"链接明明带 token、页面却说链接不完整"的现场，
 * 第一件事就是把它接上去试。
 */
export function tokenFromNavigationEntry(): string {
  let name = ''
  try {
    name = performance.getEntriesByType('navigation')[0]?.name ?? ''
  } catch {
    name = ''
  }
  const at = name.indexOf('#')
  return at < 0 ? '' : parseFragmentToken(name.slice(at + 1))
}

/**
 * 从 `hashchange` 事件的 `newURL` 里解析 token。
 *
 * **为什么不能只读 `location.hash`**（实测踩到）：SPA 内的导航可能先触发 `hashchange`、
 * 而 `location.hash` 在同一轮里被别的代码（vue-router 的 URL 归一化 / 本页的清理）
 * 动过 —— 只读 `location.hash` 会读到空串，页面停在"链接不完整"上，看起来像链接坏了。
 * 事件里的 `newURL` 是**事件产生那一刻**的完整地址，这才是权威输入（已带有 API 常量，
 * 不在 TS 的 DOM 库覆盖范围之外，故显式声明一个最小结构）。
 */
export function parseHashChangeToken(event: Event): string {
  const newURL = (event as Event & { newURL?: unknown }).newURL
  if (typeof newURL !== 'string' || !newURL.startsWith('http')) return ''
  const at = newURL.indexOf('#')
  return at < 0 ? '' : parseFragmentToken(newURL.slice(at + 1))
}

/**
 * 把地址栏的 fragment 抹掉，保留 pathname 与查询串（`/reset#token=…` → `/reset`）。
 *
 * 用**原生** `history.replaceState` 而不是 vue-router 的 `router.replace`：后者会重新跑
 * 一次路由匹配（组件可能被重建，刚取到的 token 就白取了），而这里要的恰恰是
 * "只把地址栏上那一段抹掉，别的什么都不做"。
 *
 * 第三个参数用当前 URL 而不是相对路径：相对路径会丢掉 vue-router 存在 `history.state` 里的位置信息
 * （下次前进/后退可能对不上）。所以这里把 `history.state` 原样写回。
 */
export function stripFragment(location: HashLocation, history: History): void {
  if (location.hash === '') return
  const url = `${location.pathname}${location.search}`
  try {
    history.replaceState(history.state ?? null, '', url)
  } catch {
    // history 被限制（极少见）时不做补救：token 仍然只在内存里、只进请求体，
    // 只是地址栏上还留着那一段。宁可留下这段，也不要因为清理失败让整个流程崩掉。
  }
}

/**
 * 从 `location.hash` 解析 token，并**立即**把 hash 抹掉 —— 两个页面挂载时用的就是它。
 *
 * 返回空串表示"链接里没有 token"（用户直接打开了 `/reset`、或被截断的链接）——
 * 这是**正常输入**，不是异常：调用方据此渲染"链接不完整"并给回登录/忘记密码的入口。
 *
 * **为什么以 `location.hash` 为首选**：它最直接，也不会被"当前文档最初那次导航的 URL"
 * 误导 —— 曾经只读导航条目，结果 SPA 内导航进 `/reset`（同文档导航）时读到的是不含
 * fragment 的旧 URL，token 被判定为"没有"，页面停在"链接不完整"上。
 *
 * 解析规则（严格贴近实际链接形态，不做 `URLSearchParams` 那套扩展语义）：
 *   `#token=abc`              → `abc`
 *   `#token=abc&other=1`      → `abc`
 *   `#token=`                 → `''`（空值当作没有）
 *   `#a=1&token=abc`          → `abc`
 *   `#abc`                    → `''`（没有 `=`）
 *   `#token=a%2Bb`            → `a+b`（百分号编码会解码一次）
 */
export function takeFragmentToken(location: HashLocation, history: History): string {
  const value = parseFragmentToken(location.hash)
  // hash 一律抹掉（无论解析结果如何）：分享/截图/刷新都不该把这一段带出去。
  stripFragment(location, history)
  return value
}
