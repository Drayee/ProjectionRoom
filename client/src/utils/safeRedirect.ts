/**
 * 登录回跳路径的白名单（判据② 的落地件，与 utils/safeLink.ts 同一套纪律）。
 *
 * 威胁：`/login?redirect=…` 的值来自 URL，是**不可信输入**；登录成功后我们会
 * `router.replace(它)`。原样照抄就等于开了一个开放跳转：攻击者发一个
 * `https://本站在线域名/login?redirect=https://钓鱼站/` 的链接，用户看到的是自己的站点、
 * 输入的是自己的密码，最后被送到外站 —— 而且此时页面刚拿到 access token。
 *
 * 判据（三条都必须满足）：
 *   1. 必须是**站内绝对路径**（以单个 `/` 开头）：`https://evil`、`javascript:…`、`data:…`
 *      这类带 scheme 的值一律出局；
 *   2. 拒绝协议相对地址（`//evil.com`）—— 浏览器把它当外站；
 *      同时拒绝反斜杠（`/\evil.com`、`/a\b`）：部分浏览器的 URL 解析会把 `\` 规范化成 `/`，
 *      于是 `/\evil.com` 也变成协议相对地址，绕过第 1 条；
 *   3. 拒绝控制字符（换行/制表等），它们能改写后续拼接出来的 URL。
 *
 * 不通过时**静默**退回 fallback：这里没有"让用户确认一下"的余地，
 * 目标是"要么留在站内，要么回到首页"。
 */
export function safeRedirectPath(raw: unknown, fallback = '/'): string {
  if (typeof raw !== 'string') {
    return fallback
  }
  const text = raw.trim()
  if (text === '' || !text.startsWith('/')) {
    return fallback
  }
  if (text.startsWith('//') || text.includes('\\')) {
    return fallback
  }
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(text)) {
    return fallback
  }
  // 回跳到登录/注册页本身 = 登录成功后停在原地，看起来像"点了没反应"。
  const path = text.split(/[?#]/)[0]
  if (path === '/login' || path === '/register') {
    return fallback
  }
  return text
}
