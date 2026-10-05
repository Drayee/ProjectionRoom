/**
 * 复制文本到剪贴板。
 *
 * 优先用异步 Clipboard API；它在非安全上下文（http 且非 localhost）或权限被拒时会失败，
 * 这时退回临时 textarea + execCommand —— http 内网访问、旧浏览器都还能用。
 *
 * 返回是否成功，调用方负责给用户反馈（这里不弹任何全局提示，也不抛错）。
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 继续走下面的兜底
  }

  try {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', 'true')
    area.style.position = 'fixed'
    area.style.top = '0'
    area.style.left = '0'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const done = document.execCommand('copy')
    area.remove()
    return done
  } catch {
    return false
  }
}
