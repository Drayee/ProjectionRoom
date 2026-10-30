#!/usr/bin/env node
/**
 * 构建后清理 dist/assets 里未被引用的旧产物。
 *
 * 为什么需要：client/vite.config.ts 里 `emptyOutDir: false` 是刻意的（保护交叉编译进
 * dist/downloads 的切片器二进制，它们不由 Vite 产出）。代价是每次构建都会在 assets/
 * 下堆积历史 hash 文件 —— 这不只是体积问题：旧产物里可能带着只在当次构建存在的代码
 * （例如验收用的调试钩子 window.__pr），而它们**仍然能被直接 HTTP 访问**
 * （`/assets/index-<旧hash>.js`），等于把"已从入口下线"的代码继续暴露在线上。
 * 这个会话已经踩到两次（一次是审计点名的旧包，一次是钩子构建的残留），所以在构建
 * 流程里堵死，而不是靠每次手工清理。
 *
 * ## 票 #79 之后：引用从"只认 index.html"变成"认整张 chunk 图"
 *
 * 路由懒加载（`() => import(...)`）之后，**chunk 不再只被 index.html 引用**：
 *   · index.html 引用入口 chunk 与入口 CSS；
 *   · 入口 chunk 的 `__vite__mapDeps` 里列出全部动态 chunk（含各页 CSS）；
 *   · 动态 chunk 之间还有互相引用（例如 RoomPage 引用入口 chunk 里的公共代码）。
 *
 * 只认 index.html 的旧实现会把**每一个懒加载 chunk 都当成"陈旧产物"删掉** ——
 * 症状是线上首页正常、点任何分区就 404（chunk 404 → 页面加载失败 → 空白页）。
 * 那正是"脚本自己把功能删了"：脚本看起来干净，构建产物却是残的。
 *
 * 所以现在按**引用图**判定：从 index.html 出发，把"被入口引用"与"被任意存活 chunk 引用"
 * 都算作存活，只删真正的孤儿（既不被 index.html 引用、也不被任何存活 chunk 引用）。
 *
 * 引用有两种形式，都要看：
 *   · `assets/<file>` —— Vite 的 `__vite__mapDeps` 表（动态 import 的预加载清单）；
 *   · `./<file>` —— chunk 之间的静态 import。
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

/**
 * 抽出一段文本里引用到的 assets 文件名。
 *
 * 两种形式都要认：
 *   - `assets/<name>`：Vite 的 `__vite__mapDeps` 表；
 *   - `./<name>`：chunk 之间的静态 import。
 *
 * 只认 `assets/` 会漏掉 chunk 之间的引用；只认 `./` 会漏掉动态 import 清单 ——
 * 两者都要，因为它们指向的是**不同**的文件集合。
 */
function refsIn(text) {
  const found = new Set()
  for (const m of text.matchAll(/assets\/([A-Za-z0-9_.-]+\.(?:js|css))/g)) found.add(m[1])
  for (const m of text.matchAll(/\.\/([A-Za-z0-9_.-]+\.(?:js|css))/g)) found.add(m[1])
  return found
}

// index.html 里所有 assets/<文件名> 形式的引用（含 crossorigin 的 script/link）
const referenced = new Set()
for (const m of html.matchAll(/assets\/([A-Za-z0-9_.-]+)/g)) referenced.add(m[1])

const assetsFiles = readdirSync(assetsDir).filter((name) => statSync(join(assetsDir, name)).isFile())

// 引用图闭包：从 index.html 引用的入口 chunk 出发，把它们引用到的 chunk 也算存活。
// 迭代到不动点 —— chunk 之间可能互相引用（A 引 B，B 引 A）。
const alive = new Set(referenced)
let changed = true
while (changed) {
  changed = false
  for (const name of [...alive]) {
    const p = join(assetsDir, name)
    if (!existsSync(p)) continue
    const text = readFileSync(p, 'utf8')
    for (const dep of refsIn(text)) {
      if (!alive.has(dep)) {
        alive.add(dep)
        changed = true
      }
    }
  }
}

let removed = 0
let bytes = 0
const removedNames = []
for (const name of assetsFiles) {
  if (alive.has(name)) continue
  const p = join(assetsDir, name)
  bytes += statSync(p).size
  unlinkSync(p)
  removed += 1
  removedNames.push(name)
}
console.log(
  `[prune-dist] 清理陈旧产物 ${removed} 个（${(bytes / 1024 / 1024).toFixed(2)} MB）；` +
    `保留存活产物 ${alive.size} 个（含懒加载 chunk）`,
)
if (removedNames.length > 0) {
  console.log(`[prune-dist] 已删：${removedNames.slice(0, 12).join(', ')}${removedNames.length > 12 ? ' …' : ''}`)
}
