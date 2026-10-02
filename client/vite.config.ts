import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Go 服务端地址。前后端分离开发（SPEC §1.3）：Vite 负责页面，Go 负责 /api 与 /ws。
const server = 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    // 显式绑 IPv4：Vite 默认的 localhost 在 Node 17+ 上只监听 ::1，
    // 会出现"浏览器能开、127.0.0.1 连不上"的困惑。
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': { target: server, changeOrigin: true },
      // WebSocket 代理：前端始终用相对路径 /ws，避免开发/部署两套地址
      '/ws': { target: server, ws: true, changeOrigin: true },
    },
  },
})

