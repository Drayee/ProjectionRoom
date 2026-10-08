/**
 * SHA-256 十六进制（安全批次 F-11 的内容校验用）。
 *
 * 为什么返回 `null` 而不是抛错：`crypto.subtle` **只在安全上下文里存在**
 * （https、`localhost`、`127.0.0.1` 算安全上下文；而"明文 http + 局域网 IP"
 * 这种本项目的常见部署**不算**）。那种环境下 `crypto.subtle` 是 undefined：
 * 调用方必须降级成"不校验"并计数，绝不能让播放因为"没法校验"而停摆。
 */

const HEX = '0123456789abcdef'

function toHex(bytes: Uint8Array): string {
  let out = ''
  for (let i = 0; i < bytes.length; i += 1) {
    const byte = bytes[i]
    out += HEX[(byte >> 4) & 0xf] + HEX[byte & 0xf]
  }
  return out
}

/**
 * 算一个字节视图的 SHA-256 十六进制摘要。
 *
 * 注意传入的必须是**分片自身的视图**（`byteOffset`/`byteLength` 精确落在分片数据上）：
 * `subtle.digest` 只吃视图覆盖的那一段，这样配合零拷贝视图不会多拷一份内存。
 *
 * @returns 64 位小写十六进制；环境不支持或算失败时为 null（调用方按"跳过校验"处理）
 */
export async function sha256Hex(bytes: Uint8Array): Promise<string | null> {
  const subtle = globalThis.crypto?.subtle
  if (!subtle) {
    return null
  }
  try {
    const digest = await subtle.digest('SHA-256', bytes as unknown as BufferSource)
    return toHex(new Uint8Array(digest))
  } catch {
    return null
  }
}
