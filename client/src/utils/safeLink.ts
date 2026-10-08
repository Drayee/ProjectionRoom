/**
 * 同源链接白名单（安全批次 F-1 / F-2）。
 *
 * 威胁：`SliceToolPanel` 的下载链接来自 `/api/downloads/segmenter` 的 `item.url`，
 * `SegmentUpload` 的产物链接来自切片作业的 `part.url` —— 两处都是**服务端 JSON**。
 * 原实现直接把它塞进 `<a :href>` / `link.href`：只要服务端（或明文 http 下改写响应的
 * 中间人）给出 `javascript:...`，用户点一下就变成"在本页 origin 里执行脚本"——
 * 页面里持有房间凭证与 DataChannel，代价极高。
 *
 * 判据（三条都必须满足）：
 *   1. 协议 ∈ { http:, https: }：挡掉 javascript: / data: / blob: / file: 这类"点一下就执行"；
 *   2. 与页面**同源**（与 `location.origin` 完全一致）：挡掉把用户引到任意站点的开放跳转；
 *   3. 不带 userinfo（`http://user:pass@host/`）：它的 origin 与目标站点一致，会骗过同源判断，
 *      同时把凭证写进地址栏。
 *
 * 不通过时返回 `url: null` **并给出中文原因**：调用方必须把它渲染成纯文本 + 提示，
 * 不能静默丢弃 —— 否则"下载按钮不见了"会被误判成"服务端没构建切片工具"。
 */

export interface SafeLinkResult {
  /** 通过白名单的绝对地址；不通过时为 null。 */
  url: string | null
  /** 人类可读的拦截原因（通过时为空串）。 */
  reason: string
}

/** 允许的协议白名单：只有它们能作为跳转/下载目标。 */
export const SAFE_LINK_SCHEMES: readonly string[] = ['http:', 'https:']

/**
 * 把一个**外部来源**的链接解析成可安全使用的绝对地址。
 *
 * @param raw    服务端给的原始值（类型未知，函数自己收窄）
 * @param origin 页面来源，通常是 `window.location.origin`
 */
export function resolveSafeLink(raw: unknown, origin: string): SafeLinkResult {
  if (typeof raw !== 'string') {
    return { url: null, reason: '链接不是字符串' }
  }
  const text = raw.trim()
  if (text === '') {
    return { url: null, reason: '链接为空' }
  }

  let parsed: URL
  try {
    parsed = new URL(text, origin)
  } catch {
    return { url: null, reason: '链接不是合法 URL' }
  }

  if (!SAFE_LINK_SCHEMES.includes(parsed.protocol)) {
    return { url: null, reason: `协议 ${parsed.protocol || '（空）'} 不在白名单（只允许 http/https）` }
  }
  if (parsed.username !== '' || parsed.password !== '') {
    // http://user:pass@同源主机/ 的 origin 与站点一致：不做这一步会漏过同源检查。
    return { url: null, reason: '链接带 userinfo（user:pass@host），拒绝使用' }
  }

  let base: URL
  try {
    base = new URL(origin)
  } catch {
    return { url: null, reason: `页面来源不是合法 URL（${origin || '空'}），无法判定同源` }
  }
  if (parsed.origin !== base.origin) {
    return { url: null, reason: `与页面不同源（${parsed.origin}）` }
  }

  return { url: parsed.toString(), reason: '' }
}

/**
 * 统一的"已拦截"可读提示。两处渲染都必须带上它，
 * 这样用户看到的是"这里本该有个按钮，但它被安全策略挡下了"，而不是空白。
 */
export function blockedLinkHint(reason: string): string {
  return `已拦截不可点击链接：${reason || '未通过同源/协议白名单'}（请核对服务端下发的地址）`
}
