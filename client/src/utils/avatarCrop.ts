/**
 * 头像裁剪的**纯数学**（票 #75 / 决议 #54 §1、§2）。
 *
 * 这里刻意只有函数、没有 DOM：裁剪器的全部判断（画多大、能不能拖、导出的源矩形在哪）
 * 都能在 Node 里直接算，于是 `client/test/avatar-crop.test.mjs` 不需要浏览器。
 * 会碰 canvas / File / Image 的部分留在 `AvatarCropDialog.vue` 里 —— 那才是真正的
 * "看不见就验不了"的地带，能挪出来的逻辑就不该留在那里。
 *
 * ## 坐标系统（三个，别混）
 *
 * 1. **源像素**：原图的真实像素，`0..source.width` / `0..source.height`。
 * 2. **视口像素**：裁剪框在屏幕上的边长 `viewport.size`（CSS px）。裁剪框永远是正方形。
 * 3. **显示像素**：图片画在裁剪框里的样子。`baseScale` 让较**短**的那条边正好铺满
 *    裁剪框（cover 语义，不是 contain —— contain 会在方形框里露出白边，而白边会被
 *    当成"图片的一部分"裁进去）。
 *
 * `zoom` 是相对 `baseScale` 的倍数，`1` = 刚好铺满。`offsetX/offsetY` 是**图片相对裁剪框**
 * 的位移（显示像素）：拖动手指往右，图片就跟着往右，`offsetX` 增大。
 *
 * ## 为什么偏移要夹紧
 *
 * 不夹的话用户可以把图拖出去，四角露出裁剪框背景；那一块导出后就是黑的或者透明，
 * 而用户以为自己只是"挪了一下"。夹紧的口径：**任何时刻图片都必须盖满裁剪框**，
 * 于是某个轴上没有可拖的余量时（图片该轴正好等于框），那个轴上的位移恒为 0。
 */

/** 导出边长：决议 #54 §2 定的 512。 */
export const AVATAR_EXPORT_SIZE = 512

/** 导出体积上限：决议 #54 §2 定的 1 MiB（服务端 `MaxImageBytes` 是 10 MiB，这里紧得多）。 */
export const AVATAR_MAX_BYTES = 1024 * 1024

/** 缩放区间。`1` 是"铺满"，上界 4 倍足够取到脸部细节，再大只是把像素放大成马赛克。 */
export const MIN_ZOOM = 1
export const MAX_ZOOM = 4

/** 画布尺寸的一个最小保护值：0 / 负数 / NaN 都当 1，避免后面除零。 */
const MIN_DIMENSION = 1

export interface CropSource {
  /** 原图像素宽（`naturalWidth`）。 */
  width: number
  /** 原图像素高（`naturalHeight`）。 */
  height: number
}

export interface CropViewport {
  /** 裁剪框边长（CSS px）。 */
  size: number
  /** 相对 `baseScale` 的倍数，`1..MAX_ZOOM`。 */
  zoom: number
  /** 图片相对裁剪框的位移（显示 px），已经夹紧过的值。 */
  offsetX: number
  offsetY: number
}

export interface CropOffset {
  offsetX: number
  offsetY: number
}

/** 源矩形（左上角 + 边长，源像素）。 */
export interface CropRect {
  x: number
  y: number
  size: number
}

function sanitize(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : MIN_DIMENSION
}

function clamp(value: number, low: number, high: number): number {
  // `high < low` 只在前置条件被破坏时出现：此时以 low 为准（宁可不动，也不要出 NaN）。
  const upper = high < low ? low : high
  const out = value < low ? low : value > upper ? upper : value
  // 把 `-0` 归一成 `0`：`-maxOffset` 在"该轴没有余量"时恰好是 `-0`，而它对用户
  // 没有任何意义，却会让 `deepEqual` / `JSON.stringify` 的判据出现两种答案。
  return out === 0 ? 0 : out
}

/** 把缩放夹进 `[MIN_ZOOM, MAX_ZOOM]`；NaN 按 1 处理（滑杆拿到 NaN 时不该整块失效）。 */
export function clampZoom(zoom: number): number {
  // `NaN` 是"我不知道"，落回 1（铺满）；`Infinity` 是"太大了"，交给 clamp 收到上界。
  if (Number.isNaN(zoom)) return MIN_ZOOM
  return clamp(zoom, MIN_ZOOM, MAX_ZOOM)
}

/**
 * `baseScale`：让图片较**短**的边铺满裁剪框的倍数（覆盖，不是包含）。
 *
 * 一张 4000×3000 的横图在 300px 的框里，`baseScale = 300/3000 = 0.1`，
 * 于是 4000 那条边画出来是 400px —— 溢出 100px，正好够左右拖。
 */
export function coverScale(source: CropSource, viewportSize: number): number {
  const shorter = Math.min(sanitize(source.width), sanitize(source.height))
  return sanitize(viewportSize) / shorter
}

/**
 * 图片在裁剪框里实际画出来的尺寸（显示 px）。
 *
 * 拖动夹紧与 `cropRect` 都要它，抽出来是为了两处永远一致 ——
 * 各算一遍迟早会漂，而漂的表现是"边缘能拖出一点白"这种极难复现的缺陷。
 */
export function drawnSize(source: CropSource, viewport: CropViewport): { width: number; height: number } {
  const scale = coverScale(source, viewport.size) * clampZoom(viewport.zoom)
  return { width: sanitize(source.width) * scale, height: sanitize(source.height) * scale }
}

/** 某个轴上的最大位移（显示 px）：图片比裁剪框多出来的那一半。 */
function maxOffsetFor(drawn: number, viewportSize: number): number {
  const overflow = drawn - sanitize(viewportSize)
  return overflow > 0 ? overflow / 2 : 0
}

/**
 * 把位移夹进"图片必须盖满裁剪框"的范围。
 *
 * 组件在 `pointermove` 里逐帧调用它：与其记住"这次能拖多远"，不如每次都问一句
 * "这个位置合法吗"，拖到边界自然停住，换 zoom 之后也不用重算基准点。
 */
export function clampOffset(source: CropSource, viewport: CropViewport): CropOffset {
  const drawn = drawnSize(source, viewport)
  const maxX = maxOffsetFor(drawn.width, viewport.size)
  const maxY = maxOffsetFor(drawn.height, viewport.size)
  return {
    offsetX: clamp(Number.isFinite(viewport.offsetX) ? viewport.offsetX : 0, -maxX, maxX),
    offsetY: clamp(Number.isFinite(viewport.offsetY) ? viewport.offsetY : 0, -maxY, maxY),
  }
}

/**
 * 裁剪框在**源像素**里对应的正方形。
 *
 * 这是导出的唯一输入：`canvas.drawImage(img, rect.x, rect.y, rect.size, rect.size, 0, 0, 512, 512)`。
 *
 * 推导：显示坐标里框中心在图片中心上偏移了 `offset`，换算回源像素要除以总缩放；
 * 源边长 = 框边长 / 总缩放。夹紧保证 `x + size <= width`，所以这里可以再兜一次底
 * （浮点运算在极端 zoom 下可能多出 1e-13，`drawImage` 会对越界源矩形做静默裁剪）。
 */
export function cropRect(source: CropSource, viewport: CropViewport): CropRect {
  const width = sanitize(source.width)
  const height = sanitize(source.height)
  const scale = coverScale(source, viewport.size) * clampZoom(viewport.zoom)
  const offset = clampOffset(source, viewport)
  const size = Math.min(sanitize(viewport.size) / scale, width, height)

  // 图片中心在源坐标里是 (w/2, h/2)；图片往右移 offsetX 等于"取景框往左移"。
  const centerX = width / 2 - offset.offsetX / scale
  const centerY = height / 2 - offset.offsetY / scale
  const x = clamp(centerX - size / 2, 0, width - size)
  const y = clamp(centerY - size / 2, 0, height - size)
  return { x, y, size }
}

/**
 * 导出质量阶梯（JPEG / WebP 的 `toBlob` 质量参数）。
 *
 * 从 0.92 往下试，直到体积过关。为什么不是"固定 0.9 然后失败就报错"：
 * 512×512 的高频细节图在 0.92 下通常 60–120 KB，远低于 1 MiB；但全屏噪点的图
 * 在同样质量下能到几 MB。用户没法理解"你的图太花"，而多试几次质量参数是无感的。
 */
export const QUALITY_STEPS = [0.92, 0.85, 0.75, 0.65, 0.55, 0.45] as const

/** 按体积上限挑第一个够小的结果；全都不行时返回最后一个（调用方据此报错）。 */
export function pickAcceptableBlob<T extends { size: number }>(candidates: T[], maxBytes = AVATAR_MAX_BYTES): T | null {
  if (candidates.length === 0) return null
  const ok = candidates.find((c) => c.size <= maxBytes)
  return ok ?? candidates[candidates.length - 1]
}

/** 人类可读的体积（与 `utils/count.ts` 的截断口径同族：一位小数，不四舍五入到整数）。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < 1024) return `${Math.round(bytes)} B`
  if (bytes < 1024 * 1024) return `${Math.floor(bytes / 102.4) / 10} KB`
  return `${Math.floor(bytes / (1024 * 102.4)) / 10} MB`
}

/**
 * 这个文件能不能当头像**输入**。
 *
 * 白名单与服务端 `storage.allowedImageTypes` 一致：JPEG / PNG / GIF / WebP。
 * **SVG 明确禁止**（它可内嵌 `<script>`，同源托管即存储型 XSS；服务端那条也在拦，
 * 这里是同一口径的前置拦截 —— 让用户选完再被拒，不如在选择器上就不给选）。
 *
 * GIF 保留：裁剪会取**第一帧**（canvas 不支持动画），这条写在对话框的说明里，
 * 否则用户会以为"我的动图怎么不动了"。
 */
const ACCEPTED_MIME = ['image/jpeg', 'image/png', 'image/gif', 'image/webp']

export function isAcceptedAvatarType(mime: string): boolean {
  return ACCEPTED_MIME.includes(mime.toLowerCase())
}

/** `<input accept>` 用的属性值（与上面的白名单同源，不另写一份）。 */
export const AVATAR_ACCEPT_ATTR = ACCEPTED_MIME.join(',')
