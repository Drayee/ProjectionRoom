/**
 * 地址掩码（安全批次 F-6）：诊断报告要能贴进 issue / 群里，就必须先去掉可定位到人的部分。
 *
 * 为什么按"前缀保留"而不是整体抹掉：报告的价值在于分辨
 *   · 这条链路是走 IPv4 还是 IPv6、
 *   · 是同网段还是跨网、
 *   · 是私网还是公网 ——
 * 这些只需要地址前缀。完整地址（对端可能是陌生人的家宽公网 IP）不该跟着报告出去。
 *
 * 口径（与报告口径一致）：
 *   - IPv4 保留**前两段**：`192.168.1.5` → `192.168.x.x`（`10/8`、`172.16/12`、`192.168/16`
 *     这些私网段靠前两段就能判别，"是私网/是公网"的信号不丢）；
 *   - IPv6 保留**前两段**：`240e:398:1:2::5` → `240e:398:xxxx::`
 *     （全局单播前缀 `2000::/3` 与链路本地 `fe80::/10`、ULA `fc00::/7` 都靠它就能认出）；
 *   - 既不是 v4 也不是 v6（含 Chrome 的 mDNS 混淆名 `xxxx.local`）：不猜形状，整体打码。
 *
 * 纯函数、无副作用：可以被单测直接喂样本断言。
 */

const IPV4_RE = /^\d{1,3}(?:\.\d{1,3}){3}$/
/** 打码后的占位片段（IPv6 用）。 */
const V6_MASK = 'xxxx'
/** 认不出来的地址形态：整体打码。 */
export const ADDRESS_MASK_FALLBACK = '（已打码）'

/** 去掉 `[...]`（IPv6 字面量的方括号，可能带 `:port`）与 `%zone`（`fe80::1%en0` 的 scope id）。 */
function stripDecorations(raw: string): string {
  let text = raw.trim()
  if (text.startsWith('[')) {
    const end = text.indexOf(']')
    if (end > 0) {
      text = text.slice(1, end)
    }
  }
  const zone = text.indexOf('%')
  if (zone >= 0) {
    text = text.slice(0, zone)
  }
  return text
}

/** IPv4 字面量的掩码：保留前两段。 */
function maskIpv4(address: string): string {
  const parts = address.split('.')
  if (parts.length !== 4) {
    return ADDRESS_MASK_FALLBACK
  }
  return `${parts[0]}.${parts[1]}.x.x`
}

/** IPv6 字面量的掩码：保留前两段 hextet（`::` 之后的部分整体打掉）。 */
function maskIpv6(address: string): string {
  const head = address.split('::')[0] ?? ''
  const groups = head.split(':').filter((group) => group !== '')
  if (groups.length === 0) {
    // 以 `::` 开头（`::1` / `::ffff:1.2.3.4`）：没有可保留的前缀。
    return `${V6_MASK}::`
  }
  if (groups.length === 1) {
    return `${groups[0]}:${V6_MASK}::`
  }
  return `${groups[0]}:${groups[1]}:${V6_MASK}::`
}

/**
 * 掩码一个 IP 文本。空串原样返回（调用方自己有"（地址不可见）"这类文案）。
 * 非 IP 文本（mDNS 混淆名、主机名）一律整体打码 —— 它们同样能定位到具体设备。
 */
export function maskAddress(raw: string | null | undefined): string {
  const text = stripDecorations(typeof raw === 'string' ? raw : '')
  if (text === '') {
    return ''
  }
  if (IPV4_RE.test(text)) {
    return maskIpv4(text)
  }
  if (text.includes(':')) {
    return maskIpv6(text)
  }
  return ADDRESS_MASK_FALLBACK
}
