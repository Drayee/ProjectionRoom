/**
 * ICE 候选地址族分类：**"IPv6 直连到底有没有生效"的唯一判据来源**。
 *
 * 为什么需要它：本机实测过"只有 fe80:: 链路本地、拿不到公网 IPv6"的网卡状态。
 * 那种情况下 IPv6 是**不可跨网**的 —— 把它当成"IPv6 可用"只会在信令里塞一对
 * 注定连不上的候选，对端把时间浪费在它上面，最后仍然退回 IPv4。
 * 所以这里的口径必须严格：
 *
 *   - 全局单播 IPv6（2000::/3，含 2001:da8:... / 240e:...）= 可跨网直连；
 *   - 链路本地 fe80::/10 与 ULA fc00::/7 = **不可跨网**（前者只在同一条链路上有效，
 *     后者只在同一私有域内有效），不当作 IPv6 可用，也不发到信令里；
 *   - 非 IP 文本（Chrome 默认开启 mDNS 混淆，host 候选的 address 形如 `xxxx.local`，
 *     getStats 里甚至是空串）= unknown。**不能**按"看起来不是 IPv6"丢掉：
 *     同局域网直连正是靠这些候选，丢掉就等于把局域网场景打死。
 *
 * 注意 mDNS 混淆带来的一个真实限制（实测，见 test/script/verify-ice.mjs 输出）：
 * 在 Chrome 默认参数下，**host 候选的地址看不见**，能看见的真实地址来自 srflx
 * （STUN 回显）。所以链路本地候选往往表现为 unknown 而无法被过滤掉 ——
 * 这不是实现缺陷，是浏览器刻意隐藏本地 IP 的结果；能判定的一定要判定，
 * 判不了的要保留而不是误杀。
 */

/** 地址族。除四要素外多给的 ula/loopback/unknown 只是为了诊断时能分清"没有"和"看不出来"。 */
export type CandidateFamily = 'v4' | 'v6-global' | 'v6-link-local' | 'v6-ula' | 'v6-loopback' | 'unknown'

/** 各地址族的候选计数（诊断抽屉与验收脚本都读它）。 */
export interface FamilyCounts {
  v4: number
  v6Global: number
  v6LinkLocal: number
  v6Ula: number
  unknown: number
}

export function emptyFamilyCounts(): FamilyCounts {
  return { v4: 0, v6Global: 0, v6LinkLocal: 0, v6Ula: 0, unknown: 0 }
}

/** 把一个候选地址归入计数字段。loopback 与 unknown 一起并入 unknown（它们都不是"可跨网"证据）。 */
export function countFamily(counts: FamilyCounts, family: CandidateFamily): void {
  switch (family) {
    case 'v4':
      counts.v4 += 1
      return
    case 'v6-global':
      counts.v6Global += 1
      return
    case 'v6-link-local':
      counts.v6LinkLocal += 1
      return
    case 'v6-ula':
      counts.v6Ula += 1
      return
    default:
      counts.unknown += 1
  }
}

/**
 * 候选是否值得发到信令里。
 *
 * 只有"明确判定为不可跨网"的才丢：IPv6 链路本地 / ULA / 回环 / 组播。
 * 判定不了的（mDNS 混淆名、空地址）一律保留 —— 宁可多发一条，也不要把
 * 局域网直连的唯一路径砍掉。
 */
export function isCrossNetworkUsable(family: CandidateFamily): boolean {
  return family !== 'v6-link-local' && family !== 'v6-ula' && family !== 'v6-loopback'
}

const IPV4_RE = /^\d{1,3}(?:\.\d{1,3}){3}$/

/**
 * 分类一个候选地址字符串。输入可能来自 `RTCIceCandidate.address` 或
 * `getStats()` 里 `local-candidate` / `remote-candidate` 的 `address` 字段，
 * 二者都可能是 `.local` 主机名或空串。
 */
export function classifyAddress(raw: string | null | undefined): CandidateFamily {
  if (!raw) {
    return 'unknown'
  }
  let text = raw.trim().toLowerCase()
  if (text.startsWith('[') && text.endsWith(']')) {
    text = text.slice(1, -1)
  }
  // 带 zone id 的链路本地地址（fe80::1%9 / fe80::1%wlan0）：按地址本身分类。
  const zone = text.indexOf('%')
  if (zone >= 0) {
    text = text.slice(0, zone)
  }
  if (text === '') {
    return 'unknown'
  }
  if (IPV4_RE.test(text)) {
    return 'v4'
  }
  if (!text.includes(':')) {
    // 主机名（mDNS 混淆名 .local、或未解析的名字）：不知道族，保留。
    return 'unknown'
  }

  // 只取首个 hextet 就够判前缀：'::1' 的第一段是空串（等价于 0），
  // 'fe80::1' 是 'fe80'，完整写法 '2001:db8:0:0:0:0:0:1' 是 '2001'。
  const first = text.split(':')[0]
  if (first === '') {
    // 以 :: 开头：::1 是回环，:: 是未指定，其余（::ffff:x.x.x.x）都不参与 ICE。
    return text === '::1' ? 'v6-loopback' : 'unknown'
  }
  if (!/^[0-9a-f]{1,4}$/.test(first)) {
    return 'unknown'
  }
  const value = Number.parseInt(first, 16)

  if ((value & 0xffc0) === 0xfe80) {
    return 'v6-link-local'
  }
  if ((value & 0xfe00) === 0xfc00) {
    return 'v6-ula'
  }
  if ((value & 0xff00) === 0xff00) {
    // 组播（ff00::/8）：ICE 不会用，判定为不可用。
    return 'v6-ula'
  }
  if (value >= 0x2000 && value <= 0x3fff) {
    return 'v6-global'
  }
  return 'unknown'
}

/** 诊断与日志用的短标签。 */
export function familyLabel(family: CandidateFamily): string {
  switch (family) {
    case 'v4':
      return 'IPv4'
    case 'v6-global':
      return 'IPv6 全局'
    case 'v6-link-local':
      return 'IPv6 链路本地'
    case 'v6-ula':
      return 'IPv6 ULA/组播'
    case 'v6-loopback':
      return 'IPv6 回环'
    default:
      return '未知'
  }
}
