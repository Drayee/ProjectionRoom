/**
 * 幂等键（写操作的重放保护）。
 *
 * 为什么需要它：注册、发验证码、改密码这三类操作**重试是常态** ——
 * 用户双击、网络超时后前端自动重发、反代重放。没有幂等键时一次重试可能：
 *
 *   · 建出两个账号（`register/verify` 被重放）；
 *   · 发出两封验证码，而**第一封立刻作废**（发新码会作废旧码）——
 *     用户拿着第一封信里的码永远验证不过，这是最坏的形态；
 *   · 白烧一次 bcrypt 与一次邮件配额。
 *
 * 关键约定：**同一个"用户意图"必须复用同一个键**。
 * 因此键的生命周期由调用方管：它在一份表单被提交时生成，重试时**原样复用**，
 * 直到这次操作成功、或被用户明确改成另一次提交（例如改了用户名再提交）。
 *
 * 生成方式用 `crypto.randomUUID()`（浏览器原生，128 bit 随机）。
 * 不用 `Math.random()`：键只需要唯一性，但用随机源写这类东西是廉价的正确性。
 */

/**
 * 生成一个新的幂等键。
 *
 * 退化路径：极老的浏览器没有 `crypto.randomUUID` 时用 `getRandomValues` 拼一个
 * 同形状的 UUIDv4。再退化（连 `crypto` 都没有）则返回空串 = 不带键 ——
 * 服务端把"没有键"当作普通请求，功能不受影响，只是少了重放保护。
 */
export function newIdempotencyKey(): string {
  const c = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined
  if (c && typeof c.randomUUID === 'function') {
    return c.randomUUID()
  }
  if (c && typeof c.getRandomValues === 'function') {
    const bytes = new Uint8Array(16)
    c.getRandomValues(bytes)
    // 按 RFC 4122 打上 version/variant 位（形状对得上 UUID 即可）。
    bytes[6] = (bytes[6] & 0x0f) | 0x40
    bytes[8] = (bytes[8] & 0x3f) | 0x80
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
  }
  return ''
}

/**
 * 一份"绑定到当前输入"的幂等键持有者。
 *
 * 用法：
 *
 *   const idem = createIdempotencySlot()
 *   await api.registerStart(input, idem.for(input))   // 首次：生成键
 *   await api.registerStart(input, idem.for(input))   // 重试（同一份输入）：复用同一个键
 *   idem.reset()                                      // 用户改了输入 / 操作成功：下次换新键
 *
 * `for(input)` 会对比输入的 JSON 指纹：**输入变了就换键**。
 * 这条规则很重要 —— 服务端对"同键不同体"返回 409（那是客户端 bug），
 * 而"用户改了用户名再提交"是完全正常的行为，必须给它一个新键。
 */
export interface IdempotencySlot {
  /** 取当前输入对应的键（输入未变则复用，变了则换新）。 */
  for(input: unknown): string
  /** 强制换新键（操作成功、或用户明确要"再来一次"时调）。 */
  reset(): void
}

export function createIdempotencySlot(): IdempotencySlot {
  let key = ''
  let fingerprint = ''

  return {
    for(input: unknown): string {
      let next = ''
      try {
        next = JSON.stringify(input) ?? ''
      } catch {
        // 循环引用等：退化成"每次都换键"（放弃重放保护，但不影响功能）。
        next = String(Math.random())
      }
      if (key === '' || next !== fingerprint) {
        key = newIdempotencyKey()
        fingerprint = next
      }
      return key
    },
    reset(): void {
      key = ''
      fingerprint = ''
    },
  }
}
