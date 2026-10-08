/**
 * ICE 服务器条目的白名单清洗（安全批次 F-12）。
 *
 * 威胁：`/api/ice` 是明文 http 下的普通同源请求。改写它的响应就能让浏览器把
 * STUN 探针打到攻击者指定的**任意主机**上 —— 对外的效果是"从受害者浏览器发出
 * 一次带内网源地址的出站连接"，既泄露客户端公网 IP，也能把页面当内网探针用
 * （`stun:192.168.1.1:3478` 探一个段里的存活主机）。原实现只过滤"没有 urls 的条目"，
 * 等于把服务端下发的内容原样喂给 `RTCPeerConnection`。
 *
 * 白名单口径（三层，只有第一层不可放宽）：
 *   1. 协议 ∈ { stun:, stuns:, turn:, turns: }，且主机名非空 —— 挡掉 `javascript:`、
 *      `data:`、`file:` 之类的畸形值（它们会被浏览器直接判为非法配置，但先挡掉更干净）；
 *   2. 默认**允许任意公网主机名**（合法 STUN/TURN 都是域名，例如 stun.l.google.com）；
 *   3. IP 字面量落在私网/回环/链路本地/未指定/组播/保留段时默认**拒绝**。
 *
 * 第 3 层的取舍（本批次自行决定，报告里写明）：本部署常见"自建局域网 coturn"，
 * 一刀切会误杀合法配置，所以给了两条放宽路径：
 *   · 页面本身就部署在私网/回环上（`location.hostname` 是私网地址、`localhost` 或 `.local`）
 *     —— 此时这个部署本来就是局域网内网，私网 STUN 是正常的；
 *   · 显式构建期开关 `VITE_ICE_ALLOW_PRIVATE=1`。
 * 公网部署下（页面在公网域名上）私网/回环 ICE 地址一律拒绝。
 *
 * 纯函数、不碰 DOM：可以被单测直接喂样本断言（`allowPrivateHosts` 由调用方决定）。
 */

/** 允许的 ICE 协议前缀。 */
export const ICE_ALLOWED_SCHEMES: readonly string[] = ['stun:', 'stuns:', 'turn:', 'turns:']

export type IceUrlFilterCode = 'empty' | 'unparsable' | 'scheme' | 'no-host' | 'blocked-host'

/** 一条被过滤掉的 url（保留原文与原因，供诊断展示）。 */
export interface IceFilteredEntry {
  url: string
  code: IceUrlFilterCode
  detail: string
}

export interface IceSanitizeResult {
  /** 通过白名单的条目（urls 只保留存活的部分；其余字段原样保留）。 */
  servers: RTCIceServer[]
  /** 被丢掉的 url / 空条目。 */
  filtered: IceFilteredEntry[]
}

export interface IceSanitizeOptions {
  /** 是否允许 ICE 主机写成私网/回环地址（默认 false，见文件头第 3 层说明）。 */
  allowPrivateHosts?: boolean
}

const IPV4_RE = /^\d{1,3}(?:\.\d{1,3}){3}$/

/**
 * 拆出 ICE url 的协议与主机名。
 *
 * 为什么不用 `new URL()`：`stun:` / `turn:` 都不是 URL 规范的"特殊 scheme"，
 * `new URL('stun:stun.l.google.com:19302').host` 是**空串**（整段落在 pathname 里），
 * `turn:user:pass@host:3478` 更是完全解析不出 authority。所以这里手工拆。
 */
export function parseIceUrl(raw: unknown): { scheme: string; host: string } | null {
  if (typeof raw !== 'string') {
    return null
  }
  const text = raw.trim()
  const colon = text.indexOf(':')
  if (colon <= 0) {
    return null
  }
  const scheme = `${text.slice(0, colon).toLowerCase()}:`

  let rest = text.slice(colon + 1)
  // `?transport=udp` 之类的查询串不参与主机判定。
  const question = rest.indexOf('?')
  if (question >= 0) {
    rest = rest.slice(0, question)
  }
  rest = rest.trim()
  if (rest === '') {
    return { scheme, host: '' }
  }

  let host: string
  if (rest.startsWith('[')) {
    const end = rest.indexOf(']')
    if (end < 0) {
      return { scheme, host: '' }
    }
    host = rest.slice(1, end)
  } else {
    // TURN 的 `user:pass@host` 凭证：只取最后一个 @ 之后的主机部分。
    const at = rest.lastIndexOf('@')
    if (at >= 0) {
      rest = rest.slice(at + 1)
    }
    const colons = (rest.match(/:/g) ?? []).length
    if (colons > 1) {
      // 未加方括号的裸 IPv6 字面量：整段都是主机，没有端口部分。
      host = rest
    } else {
      const portAt = rest.lastIndexOf(':')
      host = portAt >= 0 ? rest.slice(0, portAt) : rest
    }
  }

  return { scheme, host: host.trim() }
}

/** IPv4 字面量是否落在"私网 / 回环 / 链路本地 / 未指定 / 组播 / 保留"里。 */
function isPrivateIpv4(address: string): boolean {
  const parts = address.split('.').map((part) => Number.parseInt(part, 10))
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) {
    // 形态非法：当成不可信，按"私网"处理（拒绝）。
    return true
  }
  const [a, b] = parts
  if (a === 0) return true // 0.0.0.0/8：本网络
  if (a === 10) return true // RFC1918
  if (a === 127) return true // 回环
  if (a === 169 && b === 254) return true // 链路本地
  if (a === 172 && b >= 16 && b <= 31) return true // RFC1918
  if (a === 192 && b === 168) return true // RFC1918
  if (a === 100 && b >= 64 && b <= 127) return true // CGNAT（含 Tailscale 的 100.64/10）
  if (a === 198 && (b === 18 || b === 19)) return true // 基准测试段
  if (a >= 224) return true // 组播 / 保留 / 广播
  return false
}

/** IPv6 字面量是否落在"回环 / 未指定 / 链路本地 / ULA / 组播"里。 */
function isPrivateIpv6(address: string): boolean {
  const text = address.toLowerCase()
  if (text === '::1' || text === '::') {
    return true
  }
  // IPv4 映射/兼容写法（::ffff:192.168.1.5）：按里面的 v4 判。
  const mapped = /^::(?:ffff:)?(\d{1,3}(?:\.\d{1,3}){3})$/.exec(text)
  if (mapped) {
    return isPrivateIpv4(mapped[1])
  }
  const first = text.split(':')[0] ?? ''
  if (first === '') {
    // 以 `::` 开头但不是 ::1 / :: 且不是映射写法：形态不可信，拒绝。
    return true
  }
  if (!/^[0-9a-f]{1,4}$/.test(first)) {
    return true
  }
  const value = Number.parseInt(first, 16)
  if ((value & 0xffc0) === 0xfe80) return true // fe80::/10 链路本地
  if ((value & 0xfe00) === 0xfc00) return true // fc00::/7 ULA
  if ((value & 0xff00) === 0xff00) return true // ff00::/8 组播
  const second = text.split(':')[1] ?? ''
  if (/^[0-9a-f]{1,4}$/.test(second)) {
    const v2 = Number.parseInt(second, 16)
    if ((value & 0xffc0) === 0xfe80) return true
    // 2001:db8::/32 文档段：不是可用的公共 STUN 主机，按不可信处理。
    if (value === 0x2001 && v2 === 0x0db8) return true
  }
  return false
}

/**
 * 主机是否属于"私网 / 回环 / 局域网名字"这一类（需要 `allowPrivateHosts` 才放行）。
 *
 * 也用于判断**页面自身**的 hostname：页面在私网上 → 这个部署本来就是局域网内网 →
 * 自动放宽私网 ICE 主机（见 useIceConfig）。
 */
export function isPrivateOrLoopbackHost(raw: unknown): boolean {
  if (typeof raw !== 'string') {
    return true
  }
  let host = raw.trim().toLowerCase()
  if (host.startsWith('[') && host.endsWith(']')) {
    host = host.slice(1, -1)
  }
  const zone = host.indexOf('%')
  if (zone >= 0) {
    host = host.slice(0, zone)
  }
  if (host === '') {
    return true
  }
  if (IPV4_RE.test(host)) {
    return isPrivateIpv4(host)
  }
  if (host.includes(':')) {
    return isPrivateIpv6(host)
  }
  // 名字：localhost / .local（mDNS）/ 单标签名字（`stun`、`cubridge` 这类内网名）都当内网。
  if (host === 'localhost' || host.endsWith('.localhost')) {
    return true
  }
  if (host.endsWith('.local')) {
    return true
  }
  return !host.includes('.')
}

/** 单条 url 的判定。通过时返回 null，否则返回被过滤的原因。 */
export function checkIceUrl(raw: unknown, opts: IceSanitizeOptions = {}): IceFilteredEntry | null {
  if (typeof raw !== 'string') {
    return { url: String(raw ?? ''), code: 'unparsable', detail: 'url 不是字符串' }
  }
  const text = raw.trim()
  if (text === '') {
    return { url: '', code: 'empty', detail: 'url 为空' }
  }
  const parsed = parseIceUrl(text)
  if (!parsed) {
    return { url: text, code: 'unparsable', detail: 'url 缺少 `scheme:` 前缀' }
  }
  if (!ICE_ALLOWED_SCHEMES.includes(parsed.scheme)) {
    return { url: text, code: 'scheme', detail: `协议 ${parsed.scheme} 不在白名单（stun/stuns/turn/turns）` }
  }
  if (parsed.host === '') {
    return { url: text, code: 'no-host', detail: '主机名为空' }
  }
  if (!opts.allowPrivateHosts && isPrivateOrLoopbackHost(parsed.host)) {
    return { url: text, code: 'blocked-host', detail: `主机 ${parsed.host} 是私网/回环/内网名字，已拒绝` }
  }
  return null
}

/** 取条目声明的 urls（字符串或数组），统一成数组。 */
function urlsOf(entry: RTCIceServer | undefined): { raw: unknown[]; wasString: boolean } {
  const urls = entry?.urls
  if (typeof urls === 'string') {
    return { raw: [urls], wasString: true }
  }
  if (Array.isArray(urls)) {
    return { raw: urls, wasString: false }
  }
  return { raw: [], wasString: false }
}

/**
 * 清洗 `/api/ice`（或 `POST /api/rooms`）下发的 iceServers。
 *
 * 语义与原来一致的部分：只保留"至少有一条可用 url"的条目（空条目会让
 * `RTCPeerConnection` 构造直接抛）。新增的部分：逐条 url 过白名单，并把被丢掉的
 * 内容连同原因一并返回（调用方必须计数并暴露到诊断，不能静默丢）。
 */
export function sanitizeIceServers(
  list: readonly RTCIceServer[] | undefined,
  opts: IceSanitizeOptions = {},
): IceSanitizeResult {
  const servers: RTCIceServer[] = []
  const filtered: IceFilteredEntry[] = []

  for (const entry of list ?? []) {
    if (!entry || typeof entry !== 'object') {
      filtered.push({ url: '', code: 'empty', detail: '条目不是对象' })
      continue
    }
    const { raw, wasString } = urlsOf(entry)
    if (raw.length === 0) {
      filtered.push({ url: '', code: 'empty', detail: '条目没有 urls' })
      continue
    }

    const keep: string[] = []
    for (const url of raw) {
      const rejected = checkIceUrl(url, opts)
      if (rejected) {
        filtered.push(rejected)
      } else {
        keep.push(String(url).trim())
      }
    }
    if (keep.length === 0) {
      continue
    }

    servers.push({
      ...entry,
      urls: wasString && keep.length === 1 ? keep[0] : keep,
    })
  }

  return { servers, filtered }
}
