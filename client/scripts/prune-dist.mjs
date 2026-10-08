#!/usr/bin/env node
/**
 * 构建后清理 dist/assets 里未被 index.html 引用的旧产物。
 *
 * 为什么需要：client/vite.config.ts 里 `emptyOutDir: false` 是刻意的（保护交叉编译进
 * dist/downloads 的切片器二进制，它们不由 Vite 产出）。代价是每次构建都会在 assets/
 * 下堆积历史 hash 文件 —— 这不只是体积问题：旧产物里可能带着只在当次构建存在的代码
 * （例如验收用的调试钩子 window.__pr），而它们**仍然能被直接 HTTP 访问**
 * （`/assets/index-<旧hash>.js`），等于把"已从入口下线"的代码继续暴露在线上。
 * 本会话已经踩到两次（一次是审计点名的旧包，一次是钩子构建的残留），所以在构建
 * 流程里堵死，而不是靠每次手工清理。
 *
 * 只删 assets/ 下未被引用的文件；downloads/ 与其它非 assets 目录一律不碰。
 */
import { existsSync, readFileSync, readdirSync, statSync, unlinkSync } from 'node:fs'
import { join, resolve } from 'node:path'

const dist = resolve(process.argv[2] ?? 'dist')
const indexPath = join(dist, 'index.html')
if (!existsSync(indexPath)) {
  console.error(`[prune-dist] 找不到 ${indexPath}，跳过（不影响构建结果）`)
  process.exit(0)
}
const html = readFileSync(indexPath, 'utf8')
const assetsDir = join(dist, 'assets')
if (!existsSync(assetsDir)) {
  console.log('[prune-dist] 没有 dist/assets，跳过')
  process.exit(0)
}

// index.html 里所有 assets/<文件名> 形式的引用（含 crossorigin 的 script/link）
const referenced = new Set()
for (const m of html.matchAll(/assets\/([A-Za-z0-9_.-]+)/g)) referenced.add(m[1])

let removed = 0
let bytes = 0
for (const name of readdirSync(assetsDir)) {
  const p = join(assetsDir, name)
  if (!statSync(p).isFile()) continue
  if (referenced.has(name)) continue
  bytes += statSync(p).size
  unlinkSync(p)
  removed += 1
}
console.log(
  `[prune-dist] 清理陈旧产物 ${removed} 个（${(bytes / 1024 / 1024).toFixed(2)} MB）` +
    `；保留被引用的 ${referenced.size} 个`,
)
