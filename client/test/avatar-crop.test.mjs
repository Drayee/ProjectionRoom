/**
 * 头像裁剪数学的**表驱动**用例（票 #75）。
 *
 * 为什么有这个文件：票面第 ① 条要求「缩放 / 拖动 / 方形输出」，而真机脚本只能钉住
 * "拖完之后导出的是不是正方形"，钉不住"拖到边界该不该停""横图竖图的基准倍数是多少"
 * 这些**数值**判据。裁剪器的这三件事全是纯函数（见 `src/utils/avatarCrop.ts` 的说明），
 * 所以按仓内既有做法：**零新依赖**，Node 内置 test runner + 原生类型剥离。
 *
 * 期望值一律**字面量**，不从实现反推（同义反复等于没测）。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  AVATAR_EXPORT_SIZE,
  AVATAR_MAX_BYTES,
  MAX_ZOOM,
  MIN_ZOOM,
  clampOffset,
  clampZoom,
  coverScale,
  cropRect,
  drawnSize,
  formatBytes,
  isAcceptedAvatarType,
  pickAcceptableBlob,
} from '../src/utils/avatarCrop.ts'

/** 一把常用的尺子：300px 的裁剪框。 */
const VIEWPORT = 300

test('coverScale 让较短的边铺满（不是较长那条）', () => {
  // 4000×3000 的横图：短边 3000 → 300/3000 = 0.1，于是长边画出来是 400px（溢出 100px 够左右拖）
  assert.equal(coverScale({ width: 4000, height: 3000 }, VIEWPORT), 0.1)
  // 竖图：短边是宽，300/3000 = 0.1，高 4000 画出来 400px
  assert.equal(coverScale({ width: 3000, height: 4000 }, VIEWPORT), 0.1)
  // 正方形：两边都是 0.1，没有任何可拖余量
  assert.equal(coverScale({ width: 3000, height: 3000 }, VIEWPORT), 0.1)
  // 小图（比框还小）也放大：200×200 在 300 框里要 ×1.5
  assert.equal(coverScale({ width: 200, height: 200 }, VIEWPORT), 1.5)
})

test('coverScale 对退化输入不产生 NaN / Infinity', () => {
  assert.equal(coverScale({ width: 0, height: 0 }, VIEWPORT), 300)
  assert.equal(coverScale({ width: -5, height: 0 }, 0), 1)
  assert.equal(Number.isFinite(coverScale({ width: Number.NaN, height: 10 }, VIEWPORT)), true)
})

test('drawnSize 随 zoom 线性放大', () => {
  const source = { width: 4000, height: 3000 }
  assert.deepEqual(drawnSize(source, { size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 }), {
    width: 400,
    height: 300,
  })
  assert.deepEqual(drawnSize(source, { size: VIEWPORT, zoom: 2, offsetX: 0, offsetY: 0 }), {
    width: 800,
    height: 600,
  })
})

test('clampZoom 夹进 [1, 4]，非数按 1', () => {
  assert.equal(clampZoom(0.2), MIN_ZOOM)
  assert.equal(clampZoom(1), 1)
  assert.equal(clampZoom(2.5), 2.5)
  assert.equal(clampZoom(99), MAX_ZOOM)
  assert.equal(clampZoom(Number.NaN), MIN_ZOOM)
  assert.equal(clampZoom(Number.POSITIVE_INFINITY), MAX_ZOOM)
})

test('clampOffset 保证图片必须盖满裁剪框', () => {
  // 横图 zoom=1：横向余量 (400-300)/2 = 50，纵向余量 0
  const source = { width: 4000, height: 3000 }
  const base = { size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 }
  assert.deepEqual(clampOffset(source, { ...base, offsetX: 10, offsetY: 10 }), { offsetX: 10, offsetY: 0 })
  // 拖过头就停在边界，不是"跟着手指跑出去"
  assert.deepEqual(clampOffset(source, { ...base, offsetX: 500, offsetY: -500 }), { offsetX: 50, offsetY: 0 })
  assert.deepEqual(clampOffset(source, { ...base, offsetX: -500, offsetY: 0 }), { offsetX: -50, offsetY: 0 })
  // zoom=2：横向余量 (800-300)/2 = 250，纵向 (600-300)/2 = 150
  assert.deepEqual(clampOffset(source, { ...base, zoom: 2, offsetX: 999, offsetY: 999 }), {
    offsetX: 250,
    offsetY: 150,
  })
})

test('正方形图在任意 zoom 下都没有可拖余量', () => {
  const source = { width: 1000, height: 1000 }
  assert.deepEqual(clampOffset(source, { size: VIEWPORT, zoom: 1, offsetX: 40, offsetY: -40 }), {
    offsetX: 0,
    offsetY: 0,
  })
  // zoom 放大之后余量出现在两边，但依然对称
  assert.deepEqual(clampOffset(source, { size: VIEWPORT, zoom: 2, offsetX: 999, offsetY: -999 }), {
    offsetX: 150,
    offsetY: -150,
  })
})

test('cropRect 在无位移时取图片正中，且边长是正方形', () => {
  const source = { width: 4000, height: 3000 }
  const rect = cropRect(source, { size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 })
  // 总缩放 0.1 → 源边长 300/0.1 = 3000（短边的全部），正方形正好塞进 3000 高
  assert.equal(rect.size, 3000)
  assert.equal(rect.x, 500) // (4000-3000)/2
  assert.equal(rect.y, 0)
  assert.equal(rect.size, rect.size)
})

test('cropRect 的位移方向：图片右移 → 取景框左移', () => {
  const source = { width: 4000, height: 3000 }
  const base = { size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 }
  const mid = cropRect(source, { ...base, offsetX: 50 })
  // 位移用满 50 显示 px = 500 源 px，于是取景框贴到左边缘
  assert.equal(mid.x, 0)
  const other = cropRect(source, { ...base, offsetX: -50 })
  assert.equal(other.x, 1000) // 4000 - 3000
})

test('cropRect 在 zoom 放大后收缩，并且永远落在源图内', () => {
  const source = { width: 4000, height: 3000 }
  for (const zoom of [1, 1.5, 2, 3, MAX_ZOOM]) {
    for (const offsetX of [-9999, -50, 0, 50, 9999]) {
      for (const offsetY of [-9999, 0, 9999]) {
        const rect = cropRect(source, { size: VIEWPORT, zoom, offsetX, offsetY })
        assert.ok(rect.size > 0, `zoom=${zoom} size 应为正`)
        assert.ok(rect.x >= 0, `zoom=${zoom} offsetX=${offsetX} x=${rect.x} 越界`)
        assert.ok(rect.y >= 0, `zoom=${zoom} offsetY=${offsetY} y=${rect.y} 越界`)
        assert.ok(rect.x + rect.size <= source.width + 1e-9, `zoom=${zoom} 右侧越界`)
        assert.ok(rect.y + rect.size <= source.height + 1e-9, `zoom=${zoom} 下侧越界`)
      }
    }
  }
})

test('cropRect 在竖图上同样取短边', () => {
  const rect = cropRect({ width: 3000, height: 4000 }, { size: VIEWPORT, zoom: 1, offsetX: 0, offsetY: 0 })
  assert.equal(rect.size, 3000)
  assert.equal(rect.x, 0)
  assert.equal(rect.y, 500)
})

test('导出边长是 512（决议 #54 §2）', () => {
  assert.equal(AVATAR_EXPORT_SIZE, 512)
  assert.equal(AVATAR_MAX_BYTES, 1024 * 1024)
})

test('pickAcceptableBlob 取第一个够小的；全超标时给最后一个（调用方据此报错）', () => {
  assert.equal(pickAcceptableBlob([]), null)
  const small = { size: 1000, q: 0.92 }
  const big = { size: 5_000_000, q: 0.45 }
  assert.equal(pickAcceptableBlob([big, small]), small)
  assert.equal(pickAcceptableBlob([big]), big)
  // 边界：恰好等于上限算过关（`<=` 而不是 `<`）
  const edge = { size: AVATAR_MAX_BYTES, q: 0.85 }
  assert.equal(pickAcceptableBlob([edge]), edge)
  // 超一个字节就不过关，落到"都给最后一个"的分支
  const over = { size: AVATAR_MAX_BYTES + 1, q: 0.85 }
  assert.equal(pickAcceptableBlob([over]), over)
})

test('formatBytes 用人话，不四舍五入到整数', () => {
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(512), '512 B')
  assert.equal(formatBytes(1024), '1 KB')
  assert.equal(formatBytes(1536), '1.5 KB')
  assert.equal(formatBytes(1024 * 1024), '1 MB')
})

test('格式白名单与 uploads 一致：SVG 必须被拒，GIF 必须被收', () => {
  assert.equal(isAcceptedAvatarType('image/png'), true)
  assert.equal(isAcceptedAvatarType('image/jpeg'), true)
  assert.equal(isAcceptedAvatarType('image/gif'), true)
  assert.equal(isAcceptedAvatarType('image/webp'), true)
  assert.equal(isAcceptedAvatarType('IMAGE/PNG'), true)
  // 服务端 `allowedImageTypes` 显式不收 SVG（内嵌 script → 存储型 XSS）
  assert.equal(isAcceptedAvatarType('image/svg+xml'), false)
  assert.equal(isAcceptedAvatarType('image/bmp'), false)
  assert.equal(isAcceptedAvatarType('application/pdf'), false)
  assert.equal(isAcceptedAvatarType(''), false)
})
