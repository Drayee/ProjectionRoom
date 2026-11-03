<script setup lang="ts">
/**
 * 头像裁剪子窗体（票 #75 / 决议 #54 §1、§2）。
 *
 * ## 这里做的三件事
 *
 * 1. **选图**：`<input type="file">` 只收 JPEG/PNG/GIF/WebP（与 `utils/avatarCrop.ts`
 *    的 `AVATAR_ACCEPT_ATTR` 同源，也与服务端 `storage.allowedImageTypes` 同口径 ——
 *    SVG 明确不给选，它可内嵌 `<script>`，同源托管就是存储型 XSS）。
 * 2. **裁方形**：正方形取景框 + 拖动 + 缩放滑杆。全部几何计算在
 *    `utils/avatarCrop.ts`（纯函数、有 Node 单测），本组件只负责「事件 → 数字 → 重画」。
 * 3. **导出 512×512**：`canvas.drawImage` 按 `cropRect` 取源像素正方形，缩到
 *    `AVATAR_EXPORT_SIZE`；JPEG/WebP 按 `QUALITY_STEPS` 从高质量往下试，取第一个
 *    ≤ 1 MiB 的结果。
 *
 * ## 服务端不重编码，所以这里必须一次到位
 *
 * 决议 #54 §1：服务端只验不重编码（go.mod 里零图像依赖）。这意味着**尺寸、体积、
 * 格式全在这里定**：512 是导出边长（§2）、1 MiB 是体积上限（§2）、格式是
 * `AVATAR_ACCEPT_ATTR` 里的四种。上传成功之后服务端只会做类型与体积的复核，
 * 不会替你缩小 —— 所以这里输出的就是最终落进桶里的字节。
 *
 * ## GIF 会变成静态图
 *
 * canvas 只画当前帧（`<img>` 解码完那一帧，通常就是第一帧）。动图头像在决议 #54
 * 里没被考虑，实现上按「取第一帧」处理，并把这件事写进界面上 —— 否则用户会以为
 * 「我的动图怎么不动了」是缺陷。
 *
 * ## 可达性（与 `features/auth/CaptchaDialog.vue` 同一套纪律）
 *
 * `role="dialog"` + `aria-modal` + `aria-labelledby`；打开时焦点移入、关闭时还给
 * 触发者；**Esc 取消、点遮罩取消、右上角 ✕ 取消**三条出口都通；Tab 在窗内循环。
 * 拖拽是鼠标/触摸专属的增强，键盘用户可以用滑杆 + 「居中」按钮达到同样效果 ——
 * 位移不靠键盘也能归零，这是"键盘可达"的底线。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import Button from '../../ui/Button.vue'
import {
  AVATAR_ACCEPT_ATTR,
  AVATAR_EXPORT_SIZE,
  AVATAR_MAX_BYTES,
  MAX_ZOOM,
  MIN_ZOOM,
  QUALITY_STEPS,
  clampOffset,
  clampZoom,
  cropRect,
  formatBytes,
  isAcceptedAvatarType,
  pickAcceptableBlob,
} from '../../../utils/avatarCrop'

const emit = defineEmits<{ (e: 'confirm', blob: Blob): void; (e: 'cancel'): void }>()

/** 取景框边长（CSS px）。320 在窄屏上放得下（窗体内边距 + 320 < 375）。 */
const VIEWPORT = 288

const dialogRef = ref<HTMLElement | null>(null)
const canvasRef = ref<HTMLCanvasElement | null>(null)
const fileInputRef = ref<HTMLInputElement | null>(null)

/** 打开前的焦点元素（关闭时还给它）。 */
let previousFocus: HTMLElement | null = null
/** 解码后的原图。刻意**不**放进 reactive：DOM 节点进响应式系统只会多一层代理开销。 */
let image: HTMLImageElement | null = null
/** 当前 object URL（换图/销毁时要 revoke，否则整张原图留在内存里）。 */
let objectUrl = ''

const source = reactive({ width: 0, height: 0 })
const viewport = reactive({ size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 })

const ready = ref(false)
const busy = ref(false)
const error = ref('')

/** 拖拽状态：指针起点 + 起点处的位移。 */
let dragStart: { x: number; y: number; offsetX: number; offsetY: number } | null = null

const hasImage = computed(() => ready.value && source.width > 0 && source.height > 0)

/** 取好的裁剪信息（给界面上那行"将裁出 3000×3000 源像素"用）。 */
const rect = computed(() => (hasImage.value ? cropRect(source, viewport) : null))

const zoomPercent = computed(() => Math.round(viewport.zoom * 100))

function cancel(): void {
  emit('cancel')
}

/** 把 canvas 按当前视口状态重画一遍。 */
function redraw(): void {
  const canvas = canvasRef.value
  if (!canvas || !image || !hasImage.value) return
  const dpr = Math.min(window.devicePixelRatio || 1, 3)
  const pixels = Math.round(VIEWPORT * dpr)
  if (canvas.width !== pixels || canvas.height !== pixels) {
    canvas.width = pixels
    canvas.height = pixels
  }
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, VIEWPORT, VIEWPORT)

  const { width, height } = drawnShape()
  const dx = (VIEWPORT - width) / 2 + viewport.offsetX
  const dy = (VIEWPORT - height) / 2 + viewport.offsetY
  ctx.drawImage(image, dx, dy, width, height)
}

/**
 * 图片当前的**显示**尺寸。
 *
 * 走 `cropRect` 的同一套输入（`coverScale × zoom`）而不是自己再算一遍：
 * 预览画错与导出裁错必须同时错或同时对，两处各算一遍迟早会漂。
 */
function drawnShape(): { width: number; height: number } {
  const scale = (VIEWPORT / Math.min(source.width, source.height)) * clampZoom(viewport.zoom)
  return { width: source.width * scale, height: source.height * scale }
}

watch(viewport, redraw)

function resetViewport(): void {
  viewport.zoom = 1
  viewport.offsetX = 0
  viewport.offsetY = 0
}

function releaseImage(): void {
  if (objectUrl) {
    URL.revokeObjectURL(objectUrl)
    objectUrl = ''
  }
  image = null
  ready.value = false
  source.width = 0
  source.height = 0
}

function pickFile(): void {
  error.value = ''
  fileInputRef.value?.click()
}

async function onFileChange(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  // 同一个文件再选一次也要触发 change：否则"选错了想重选"看起来像没反应。
  input.value = ''
  if (!file) return
  if (!isAcceptedAvatarType(file.type)) {
    error.value = '只支持 JPEG、PNG、GIF、WebP 四种格式。'
    return
  }
  error.value = ''
  busy.value = true
  releaseImage()
  const url = URL.createObjectURL(file)
  objectUrl = url
  try {
    const img = await loadImage(url)
    image = img
    source.width = img.naturalWidth
    source.height = img.naturalHeight
    resetViewport()
    ready.value = true
    await nextTick()
    redraw()
  } catch {
    error.value = '这张图片打不开，换一张试试。'
    releaseImage()
  } finally {
    busy.value = false
  }
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('image decode failed'))
    img.src = url
  })
}

function applyOffset(nextX: number, nextY: number): void {
  const clamped = clampOffset(source, { ...viewport, offsetX: nextX, offsetY: nextY })
  viewport.offsetX = clamped.offsetX
  viewport.offsetY = clamped.offsetY
}

function onPointerDown(event: PointerEvent): void {
  if (!hasImage.value) return
  // 只有主键/触摸：右键拖拽在这里没有意义，而且会顺手弹出上下文菜单。
  if (event.button !== 0) return
  dragStart = { x: event.clientX, y: event.clientY, offsetX: viewport.offsetX, offsetY: viewport.offsetY }
  ;(event.currentTarget as HTMLElement | null)?.setPointerCapture?.(event.pointerId)
  event.preventDefault()
}

function onPointerMove(event: PointerEvent): void {
  if (!dragStart) return
  applyOffset(dragStart.offsetX + (event.clientX - dragStart.x), dragStart.offsetY + (event.clientY - dragStart.y))
}

function onPointerUp(event: PointerEvent): void {
  if (!dragStart) return
  dragStart = null
  ;(event.currentTarget as HTMLElement | null)?.releasePointerCapture?.(event.pointerId)
}

function onZoomInput(event: Event): void {
  const input = event.target as HTMLInputElement
  const next = clampZoom(Number(input.value))
  if (next === viewport.zoom) return
  viewport.zoom = next
  // 放大之后原来的位移可能已经合法，也可能越界 —— 统一夹一次，让边界永远成立。
  applyOffset(viewport.offsetX, viewport.offsetY)
}

/** 键盘增强：方向键微调位移（`Shift` 大步）。拖拽之外的第二种精确控制。 */
function onCanvasKeydown(event: KeyboardEvent): void {
  if (!hasImage.value) return
  const step = event.shiftKey ? 20 : 5
  let dx = 0
  let dy = 0
  if (event.key === 'ArrowLeft') dx = -step
  else if (event.key === 'ArrowRight') dx = step
  else if (event.key === 'ArrowUp') dy = -step
  else if (event.key === 'ArrowDown') dy = step
  else return
  event.preventDefault()
  applyOffset(viewport.offsetX + dx, viewport.offsetY + dy)
}

/** 导出用的 MIME：优先 WebP（同质量体积更小），不支持时退回 JPEG。 */
function exportType(): string {
  const probe = document.createElement('canvas')
  return probe.toDataURL('image/webp').startsWith('data:image/webp') ? 'image/webp' : 'image/jpeg'
}

function toBlob(canvas: HTMLCanvasElement, type: string, quality: number): Promise<Blob | null> {
  return new Promise((resolve) => canvas.toBlob((blob) => resolve(blob), type, quality))
}

async function exportBlob(): Promise<Blob | null> {
  if (!image || !hasImage.value) return null
  const type = exportType()
  const canvas = document.createElement('canvas')
  canvas.width = AVATAR_EXPORT_SIZE
  canvas.height = AVATAR_EXPORT_SIZE
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  if (type === 'image/jpeg') {
    // JPEG 没有 alpha：不铺底色的话，PNG 的透明区域会变成**黑色**。
    // 白色是中性的（头像底色是字母头的颜色，透明处露白比露黑更不刺眼）。
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, AVATAR_EXPORT_SIZE, AVATAR_EXPORT_SIZE)
  }
  const box = cropRect(source, viewport)
  ctx.drawImage(
    image,
    box.x,
    box.y,
    box.size,
    box.size,
    0,
    0,
    AVATAR_EXPORT_SIZE,
    AVATAR_EXPORT_SIZE,
  )

  const candidates: Blob[] = []
  for (const quality of QUALITY_STEPS) {
    const blob = await toBlob(canvas, type, quality)
    if (!blob) continue
    candidates.push(blob)
    if (blob.size <= AVATAR_MAX_BYTES) break
  }
  return pickAcceptableBlob(candidates, AVATAR_MAX_BYTES)
}

async function confirm(): Promise<void> {
  if (!hasImage.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const blob = await exportBlob()
    if (!blob) {
      error.value = '图片处理失败，换一张试试。'
      return
    }
    if (blob.size > AVATAR_MAX_BYTES) {
      // 走到这里说明六档质量全试过还是超标：只能如实说，别假装成功。
      error.value = `这张图压到最低质量仍有 ${formatBytes(blob.size)}，超过 1 MiB 上限，换一张简单点的图。`
      return
    }
    emit('confirm', blob)
  } finally {
    busy.value = false
  }
}

/** 键盘：Esc 取消；Tab 在子窗体内循环（焦点不跑到背后的设置页上）。 */
function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    cancel()
    return
  }
  if (event.key !== 'Tab') return

  const root = dialogRef.value
  if (!root) return
  const focusables = root.querySelectorAll<HTMLElement>(
    'button:not(:disabled), [href], input:not(:disabled), select, textarea, [tabindex]:not([tabindex="-1"])',
  )
  if (focusables.length === 0) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  const active = document.activeElement as HTMLElement | null

  if (event.shiftKey && (active === first || !root.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(async () => {
  previousFocus = document.activeElement as HTMLElement | null
  document.addEventListener('keydown', onKeydown)
  await nextTick()
  // 焦点先落在「选择图片」上：这是本窗体的第一步，也是唯一能立刻做的事。
  dialogRef.value?.querySelector<HTMLElement>('[data-testid="avatar-pick"]')?.focus()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  releaseImage()
  previousFocus?.focus?.()
})
</script>

<template>
  <!-- 遮罩点击取消：与 CaptchaDialog 同一口径的第三条出口 -->
  <div class="overlay" @click.self="cancel">
    <div
      ref="dialogRef"
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="avatar-crop-title"
      data-testid="avatar-crop-dialog"
    >
      <header class="head">
        <h2 id="avatar-crop-title" class="title">裁剪头像</h2>
        <button class="close" type="button" aria-label="关闭裁剪" data-testid="avatar-crop-cancel" @click="cancel">
          ✕
        </button>
      </header>

      <p class="intro">
        选一张图，拖动和缩放决定方框里留下哪一块。导出的永远是
        {{ AVATAR_EXPORT_SIZE }}×{{ AVATAR_EXPORT_SIZE }} 的方块，服务端不会再改尺寸。
      </p>

      <input
        ref="fileInputRef"
        class="file"
        type="file"
        :accept="AVATAR_ACCEPT_ATTR"
        data-testid="avatar-file-input"
        @change="onFileChange"
      />

      <div v-if="!hasImage" class="empty">
        <Button type="button" variant="ghost" data-testid="avatar-pick" @click="pickFile">选择图片</Button>
        <p class="hint">支持 JPEG / PNG / GIF / WebP；GIF 会保留第一帧（头像不会动）。</p>
      </div>

      <template v-else>
        <div class="stage">
          <canvas
            ref="canvasRef"
            class="stage-canvas"
            tabindex="0"
            role="application"
            aria-label="拖动图片调整取景位置，也可以用方向键微调"
            data-testid="avatar-crop-canvas"
            @pointerdown="onPointerDown"
            @pointermove="onPointerMove"
            @pointerup="onPointerUp"
            @pointercancel="onPointerUp"
            @keydown="onCanvasKeydown"
          ></canvas>
        </div>

        <div class="controls">
          <label class="zoom">
            <span class="zoom-label">缩放 {{ zoomPercent }}%</span>
            <input
              class="range"
              type="range"
              :min="MIN_ZOOM"
              :max="MAX_ZOOM"
              step="0.01"
              :value="viewport.zoom"
              data-testid="avatar-crop-zoom"
              @input="onZoomInput"
            />
          </label>
          <button class="link" type="button" data-testid="avatar-crop-center" @click="resetViewport">居中</button>
          <button class="link" type="button" data-testid="avatar-crop-repick" @click="pickFile">换一张</button>
        </div>

        <p class="meta" data-testid="avatar-crop-meta">
          原图 {{ source.width }}×{{ source.height }}，将裁剪
          {{ rect ? Math.round(rect.size) : 0 }}×{{ rect ? Math.round(rect.size) : 0 }} 源像素 →
          {{ AVATAR_EXPORT_SIZE }}×{{ AVATAR_EXPORT_SIZE }}。
        </p>
      </template>

      <p v-if="error" class="error" role="alert" data-testid="avatar-crop-error">{{ error }}</p>

      <div class="foot">
        <Button
          type="button"
          variant="primary"
          data-testid="avatar-crop-confirm"
          :disabled="!hasImage"
          :loading="busy"
          @click="confirm"
        >
          {{ busy ? '处理中…' : '用这张' }}
        </Button>
        <Button type="button" variant="ghost" data-testid="avatar-crop-cancel-foot" @click="cancel">取消</Button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: rgba(6, 4, 10, 0.72);
  backdrop-filter: blur(2px);
  animation: overlay-in var(--dur-comp) var(--ease);
}

@keyframes overlay-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

.dialog {
  width: min(420px, 100%);
  max-height: calc(100vh - 40px);
  overflow-y: auto;
  border: 1px solid var(--line-strong);
  background: var(--bg-mid);
  padding: 16px 18px 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-shadow: 0 18px 48px rgba(0, 0, 0, 0.6);
  animation: dialog-in var(--dur-comp) var(--ease);
}

@keyframes dialog-in {
  from {
    transform: translateY(14px) scale(0.96);
    opacity: 0;
  }
  to {
    transform: none;
    opacity: 1;
  }
}

.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: 18px;
}

.close {
  width: 28px;
  height: 28px;
  flex: none;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}

.close:hover {
  color: var(--text);
  border-color: var(--accent);
}

.intro {
  margin: 0;
  font-size: 12px;
  color: var(--dim);
  line-height: 1.6;
}

.file {
  display: none;
}

.empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
}

.hint {
  margin: 0;
  font-size: 12px;
  color: var(--dim);
  line-height: 1.6;
}

.stage {
  display: flex;
  justify-content: center;
}

.stage-canvas {
  /* 尺寸由 JS 按 devicePixelRatio 设到 width/height 属性上，CSS 只钉显示大小 */
  width: 288px;
  height: 288px;
  max-width: 100%;
  display: block;
  background: var(--bg-low, rgba(0, 0, 0, 0.35));
  border: 1px solid var(--line-strong);
  /* 圆形预览会让人误判边缘：这里给方框，与导出形状一致 */
  touch-action: none;
  cursor: grab;
}

.stage-canvas:active {
  cursor: grabbing;
}

.stage-canvas:focus-visible {
  outline: 2px solid var(--ring, var(--accent));
  outline-offset: 2px;
}

.controls {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.zoom {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 160px;
}

.zoom-label {
  font-size: 12px;
  color: var(--dim);
  white-space: nowrap;
}

.range {
  flex: 1;
  min-width: 80px;
  accent-color: var(--accent);
}

.link {
  border: 0;
  background: none;
  padding: 2px 0;
  color: var(--dim);
  font-size: 12px;
  text-decoration: underline;
  cursor: pointer;
}

.link:hover {
  color: var(--text);
}

.meta {
  margin: 0;
  font-size: 12px;
  color: var(--dim);
  line-height: 1.5;
}

.error {
  margin: 0;
  font-size: 12px;
  color: var(--danger, #e46a6a);
  line-height: 1.6;
}

.foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

/* 窄屏：子窗体贴底铺满，避免在 375px 上出现双层滚动 */
@media (max-width: 520px) {
  .overlay {
    padding: 0;
    align-items: flex-end;
  }

  .dialog {
    width: 100%;
    max-height: 92vh;
    border-left: 0;
    border-right: 0;
    border-bottom: 0;
  }

  .stage-canvas {
    width: 100%;
    max-width: 288px;
  }
}

/* 降级：取消入场位移，只保留透明度变化（票 #43 §8） */
@media (prefers-reduced-motion: reduce) {
  .overlay,
  .dialog {
    animation: none;
  }
}
</style>
