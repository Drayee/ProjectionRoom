import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// Go 服务端地址。前后端分离开发（SPEC §1.3）：Vite 负责页面，Go 负责 /api 与 /ws。
// PR_SERVER_URL 可覆盖：验收脚本要跑在**自己那份**测试服务端上（不同端口 + 短宽限期），
// 没有这个覆盖就只能打到 8080，测的就不是被测对象了。
const server = process.env.PR_SERVER_URL || 'http://127.0.0.1:8080'

export default defineConfig(({ mode }) => {
  // 构建期开关（安全批次 F-5）。
  //
  // 为什么必须是"构建期常量"而不是运行期判断：`window.__pr` 调试钩子会把整个
  // Pinia store（房间密码、成员表、reportMetrics / setUploadThrottle）挂到全局，
  // 生产 bundle 里**连字符串都不该出现**。用 define 注入一个字面量，Rollup 才能
  // 在打包阶段把 `if (false || false) installDebugHook()` 整段判死、连带把 debug.ts
  // 从产物里摇掉（`npm run build` 后 grep 产物必须 0 命中 `window.__pr`）。
  //
  // 默认关闭。要跑依赖这些钩子的验收脚本时，用 Vite dev（DEV 恒为 true）或显式开启：
  //   PowerShell:  $env:VITE_DEBUG_HOOKS=1; npm --prefix client run build
  const env = loadEnv(mode, process.cwd(), 'VITE_')
  const debugHooks = env.VITE_DEBUG_HOOKS === '1' || process.env.VITE_DEBUG_HOOKS === '1'

  return {
    plugins: [vue()],
    define: {
      __PR_DEBUG_HOOKS__: JSON.stringify(debugHooks),
    },
    build: {
      // 不要清空 dist：scripts/build-segmenter.ps1 交叉编译出的切片器二进制就放在
      // client/dist/downloads/ 下（与静态托管的 /downloads/* 一一对应），
      // 默认的 emptyOutDir 会在 `npm run build` 时把它们**静默删掉**——
      // 端点清单还在、URL 还在，但下载全 404，脚本只能退回兜底路径（真踩过）。
      // 代价是 assets/ 下的旧 hash 文件会留下来（无害：index.html 只引用当前这一份）。
      emptyOutDir: false,
    },
    server: {
      // 显式绑 IPv4：Vite 默认的 localhost 在 Node 17+ 上只监听 ::1，
      // 会出现"浏览器能开、127.0.0.1 连不上"的困惑。
      host: '127.0.0.1',
      port: 5173,
      proxy: {
        '/api': { target: server, changeOrigin: true },
        // WebSocket 代理：前端始终用相对路径 /ws，避免开发/部署两套地址
        '/ws': { target: server, ws: true, changeOrigin: true },
        // 切片器二进制：一键切片脚本按"页面来源 + /downloads/..."拼下载地址，
        // 部署态是同源（Go 单端口直接托管），开发态必须靠这条代理才能下到，
        // 否则脚本在 Vite 5173 下会拿到 404。
        '/downloads': { target: server, changeOrigin: true },
      },
    },
  }
})

