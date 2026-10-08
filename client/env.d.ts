/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>
  export default component
}

/**
 * 构建期常量：是否把 `window.__pr` 调试钩子编进产物（安全批次 F-5）。
 *
 * 由 `vite.config.ts` 的 `define` 注入字面量 `true` / `false`：
 * 生产构建默认注入 `false`，Rollup 会把 `if (import.meta.env.DEV || false)` 整段判死，
 * 连带把 `debug.ts`（含 `window.__pr` 赋值）从产物里摇掉。
 * 打开方式：`VITE_DEBUG_HOOKS=1`（见 vite.config.ts 注释）。
 */
declare const __PR_DEBUG_HOOKS__: boolean
