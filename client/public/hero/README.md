# 落地页动图槽位（`/hero/…`）

这个目录是**落地页标题背后的动图**的落点。目前**没有素材**，所以页面上只有纯 CSS 渐变兜底
（见 `src/components/HeroMotion.vue`），不会出现破图。

## 放素材（三步）

1. 把动图或循环视频拷进本目录，例如 `moon-loop.webp` / `moon-loop.mp4`。
2. 在 `src/heroAssets.ts` 的 `HERO_LOOPS` 里加一条：

   ```ts
   export const HERO_LOOPS: HeroLoop[] = [
     { src: 'moon-loop.webp', kind: 'image' },
     // 或：{ src: 'moon-loop.mp4', kind: 'video', alt: '月相循环' }
   ]
   ```

3. 重新构建（`npm --prefix client run build`）。

## 尺寸与观感

由 CSS 变量控制（默认值写在 `src/components/HeroMotion.vue`，可用处全局改 `src/styles.css` 的 `:root`）：

| 变量 | 作用 | 默认 |
| --- | --- | --- |
| `--hero-motion-w` | 槽位宽度 | `min(560px, 86vw)` |
| `--hero-motion-h` | 槽位高度 | `min(440px, 58vh)` |
| `--hero-motion-opacity` | 动图/渐变整体不透明度 | `0.55` |
| `--hero-motion-blur` | 渐变兜底层柔化半径 | `34px` |

## 约束

- **只放本地素材**：本项目不引外链资源（图标、动图都一样）。
- 清单里写了但文件不在时，`HeroMotion` 会在 `error` 时把该条摘掉并回落到渐变 —— 仍然不会破图。
- 素材是**装饰性**的：容器默认 `aria-hidden`，读屏不播报（想要描述时给 `alt`）。
- 这个目录不会影响 `dist/icons`（图标是另一套，22 个，见 `client/public/icons/`）。
