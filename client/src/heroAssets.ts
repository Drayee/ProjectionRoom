/**
 * 落地页标题背后的「动图槽位」（现在**没有素材**，所以列表是空的）。
 *
 * 替换方式（三步，不需要改任何组件逻辑）：
 *   1. 把动图/循环视频放进 `client/public/hero/`（该目录下的文件按原样发布到 `/hero/…`）；
 *   2. 在下面的 `HERO_LOOPS` 里加一条（`src` 是**文件名**，不是路径）；
 *   3. 尺寸/柔化/不透明度由 CSS 变量控制（见 styles.css 的 `--hero-motion-*`），
 *      需要单独调这个槽位时在 `HeroMotion.vue` 的 `.hero-motion` 上覆盖即可。
 *
 * 为什么用"显式清单"而不是自动扫描 public/：
 *   - 自动扫描要么在构建期加插件，要么在运行期发一次探测请求 —— 后者在没有素材时
 *     会稳定产生一个 404（`/hero/…`），而"没有素材"恰恰是本轮的默认状态；
 *   - 清单为空 ⇒ 一个 `<img>/<video>` 都不渲染 ⇒ **不存在破图**（槽位由纯 CSS 渐变兜底）；
 *   - 清单里写了但文件被删（或放错目录）时，`HeroMotion` 会在加载失败时把这一条
 *     从显示列表里摘掉并回落到渐变，同样不会出现破图（见 onError）。
 *
 * ⚠️ 只放**本地**素材：本项目不引外链资源（图标一样，动图也一样）。
 */

export interface HeroLoop {
  /** 文件名，位于 `client/public/hero/` 下（不含前导斜杠，可带子目录）。 */
  src: string
  /** image = gif/webp/avif/png/svg 等；video = mp4/webm 循环视频（自动静音、循环、内联播放）。 */
  kind: 'image' | 'video'
  /** 无障碍描述。留空 = 纯装饰（容器 `aria-hidden`），读屏不播报。 */
  alt?: string
}

/** 标题背后的动图列表。空 = 只有 CSS 渐变兜底（当前状态）。 */
export const HERO_LOOPS: HeroLoop[] = []

/** 把一个条目转成可用的 URL（`/hero/<文件名>`）。文件名里的空白与引号在清单阶段就被挡掉。 */
export function heroLoopUrl(loop: HeroLoop): string {
  return `/hero/${loop.src.trim().replace(/^\/+/, '')}`
}
