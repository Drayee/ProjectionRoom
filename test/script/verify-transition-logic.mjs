#!/usr/bin/env node
/**
 * `verify-transition-logic.mjs` —— **路由过渡决策器的单测**（票 #76）。
 *
 * # 为什么是 node 脚本而不是 vitest
 *
 * 本仓库前端**刻意没有**单测框架（`client/package.json` 里只有 vue-tsc + vite），
 * 为了一个纯函数引入一整套 vitest 依赖不划算。同一条既有先例：
 * `test/script/verify-schedule.mjs` 直接 import `client/src/utils/serveSchedule.ts`
 * （Node 24 的原生类型擦除），零新依赖、零新配置。
 *
 * ⚠ 决策器**必须**保持纯（不 import vue、不碰 DOM、不读 router）：
 * 一旦它摸到 `window.matchMedia` 或 router 实例，就退化成只能靠真机脚本验的东西，
 * 而真机脚本验不了「20 项边角」这种组合爆炸的判据。
 * 本文件与 `client/src/utils/routeTransition.ts` 的分层就是这个意思。
 *
 * # 判据来源
 *
 * 票 #76 的验收清单 + 票 #45 的决议评论 + 原型分支
 * `prototype/natural-route-transition` 的 **20 项边角表**（决策器纯模块的一手来源）。
 * 下面把那张表里与决策器有关的行逐条钉住。
 *
 * 用法：node test/script/verify-transition-logic.mjs
 */
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  TRANSITION_SHIFT_PX,
  TRANSITION_DURATION_MS,
  planQueryOnly,
  planTransition,
  routeDepth,
  shouldMorphSharedElement,
} from '../../client/src/utils/routeTransition.ts'

/** 仓库根目录（读 tokens.css 用）。 */
const REPO_ROOT = resolve(fileURLToPath(new URL('../..', import.meta.url)))

let failed = 0
let passed = 0

function check(name, actual, expected) {
  const a = JSON.stringify(actual)
  const e = JSON.stringify(expected)
  if (a === e) {
    passed += 1
    console.log(`  ok   ${name} → ${a}`)
    return
  }
  failed += 1
  console.log(`  FAIL ${name}\n       期望 ${e}\n       实际 ${a}`)
}

/** 路由 meta 的最小形状（只取决策器用到的字段）。 */
function meta(depth, exempt = false) {
  return { depth, exempt }
}

console.log('== 参数（票 #45 决议）==')
check('位移量是 12px', TRANSITION_SHIFT_PX, 12)
check('时长是 240ms', TRANSITION_DURATION_MS, 240)

console.log('\n== 方向：按 meta.depth 比较，不用历史 ==')
{
  const d = planTransition(meta(1), meta(2), {})
  check('前进（列表→详情）kind', d.kind, 'forward')
  check('前进的位移量', d.shiftPx, 12)
  check('前进的时长', d.durationMs, 240)
}
{
  const d = planTransition(meta(2), meta(1), {})
  check('后退（详情→列表）kind', d.kind, 'back')
  check('后退的位移量', d.shiftPx, 12)
  check('后退的时长', d.durationMs, 240)
}
{
  const d = planTransition(meta(1), meta(1), {})
  check('同级（分区→分区）kind', d.kind, 'lateral')
  check('同级**无位移**', d.shiftPx, 0)
  check('同级仍有淡入时长', d.durationMs, 240)
}
{
  const d = planTransition(meta(0), meta(2), {})
  check('落地页→详情（0→2）按 depth 差判前进', d.kind, 'forward')
}
{
  const d = planTransition(meta(2), meta(0), {})
  check('详情→落地页（2→0）判后退', d.kind, 'back')
}

console.log('\n== 不触发的三种情形 ==')
{
  const d = planTransition(null, meta(0), {})
  check('首屏进入（没有上一页）kind', d.kind, 'none')
  check('首屏进入无位移', d.shiftPx, 0)
  check('首屏进入不占用时间', d.durationMs, 0)
}
{
  const d = planTransition(meta(1), meta(1), { sameRoute: true })
  check('同路由（仅 query/params 变化）kind', d.kind, 'none')
  check('同路由不占用时间', d.durationMs, 0)
}
{
  const d = planQueryOnly()
  check('同路由仅 query 变化 kind', d.kind, 'none')
  check('同路由仅 query 变化不占用时间', d.durationMs, 0)
  check('同路由仅 query 变化无位移', d.shiftPx, 0)
}

console.log('\n== 放映室整页双向豁免 ==')
{
  const enter = planTransition(meta(1), meta(2, true), {})
  check('进放映室 kind', enter.kind, 'exempt')
  check('进放映室不占用时间', enter.durationMs, 0)
  check('进放映室无位移', enter.shiftPx, 0)
}
{
  const leave = planTransition(meta(2, true), meta(1), {})
  check('出放映室 kind', leave.kind, 'exempt')
  check('出放映室不占用时间', leave.durationMs, 0)
}
{
  // 豁免优先于方向判定：即使 depth 差是「前进」，房页仍是硬替换。
  const d = planTransition(meta(0), meta(2, true), {})
  check('豁免优先于方向（不是 forward）', d.kind, 'exempt')
}
{
  // 出房时哪怕方向本该是 backward，豁免仍然生效。
  const d = planTransition(meta(2, true), meta(0), {})
  check('出房豁免优先于后退', d.kind, 'exempt')
}

console.log('\n== reduced-motion：JS 侧判死 ==')
{
  const d = planTransition(meta(1), meta(2), { reducedMotion: true })
  check('reduced-motion kind', d.kind, 'instant')
  check('reduced-motion 不占用时间', d.durationMs, 0)
  check('reduced-motion 无位移', d.shiftPx, 0)
}
{
  // 判死优先于一切：哪怕这次换页是进放映室，结果也该是 instant（两者都不动）。
  const d = planTransition(meta(1), meta(2, true), { reducedMotion: true })
  check('reduced-motion 优先于豁免', d.kind, 'instant')
}

console.log('\n== 共享元素：只在前进方向触发（#78 的规则）==')
{
  check('前进触发共享元素', shouldMorphSharedElement(planTransition(meta(1), meta(2), {})), true)
  check('后退不触发共享元素', shouldMorphSharedElement(planTransition(meta(2), meta(1), {})), false)
  check('同级不触发共享元素', shouldMorphSharedElement(planTransition(meta(1), meta(1), {})), false)
  check('豁免不触发共享元素', shouldMorphSharedElement(planTransition(meta(1), meta(2, true), {})), false)
}

console.log('\n== 路由层级读法 ==')
check('depth 0 读出 0', routeDepth({ depth: 0 }), 0)
check('depth 2 读出 2', routeDepth({ depth: 2 }), 2)
check('缺省是 1（中性起点，不是落地页）', routeDepth({}), 1)
check('undefined meta 也缺省成 1', routeDepth(undefined), 1)

console.log('\n== 决策理由非空（"为什么没动"必须能解释）==')
{
  const cases = [
    planTransition(meta(1), meta(2), {}),
    planTransition(meta(2), meta(1), {}),
    planTransition(meta(1), meta(1), {}),
    planTransition(null, meta(0), {}),
    planTransition(meta(1), meta(2, true), {}),
    planTransition(meta(1), meta(2), { reducedMotion: true }),
    planQueryOnly(),
  ]
  check('每条决策都有理由', cases.every((d) => typeof d.reason === 'string' && d.reason.length > 0), true)
}

console.log('\n== 唯一真源：CSS 时长 token 与 TS 常量一致 ==')
{
  // 决策器的 TRANSITION_DURATION_MS 与 tokens.css 的 --dur-comp 是**同一条约定**。
  // 两处各写一个 240，改了一处忘了另一处 → 界面动效时长与决策器声明的对不上，
  // 而这种漂移在真机上很难一眼看出（"看起来还是有动画"）。这里把它钉住。
  const css = readFileSync(join(REPO_ROOT, 'client', 'src', 'styles', 'tokens.css'), 'utf8')
  const m = css.match(/--dur-comp:\s*([\d.]+)ms/)
  const tokenMs = m ? Number(m[1]) : null
  check('--dur-comp 的值与 TRANSITION_DURATION_MS 一致', tokenMs, TRANSITION_DURATION_MS)
}

console.log(`\n通过 ${passed} / 失败 ${failed}`)
console.log(failed === 0 ? '判定：PASS' : '判定：FAIL')
process.exit(failed === 0 ? 0 : 1)
