/**
 * 路由页面模块的**唯一**加载入口（票 #79）。
 *
 * 为什么不是把 `() => import(...)` 直接写在路由表里：预热（`utils/routePrefetch.ts`）
 * 需要同一份加载函数，而「加载函数写两遍」= 两份必然漂移 —— 一份改了路径、另一份没改，
 * 症状是"悬停预热的是 A 页、点进去加载的是 B 页"，而且只在网络慢时偶发。
 * 所以：路由表与预热都从这里取，**这份表是唯一真源**。
 *
 * 每一条都是**字面量**的动态 import：打包器据此切 chunk。
 * 写成 `import(path)`（变量路径）会让打包器无法静态分析，整包回到一个 chunk 里
 * —— 那正是懒加载要消灭的状态。
 *
 * 页面清单（20 条路由 / 20 个组件；/login 与 /register 共用 LandingView，
 * 打包器会自动把它抽成共享 chunk）。
 *
 * **落地页（/）不在这一份里**：它由 router.ts 静态 import。理由见那里的注释 ——
 * 首屏必须是"打开就有"，不能等一次 chunk 往返。
 */
export const PAGE_MODULES = {
  room: () => import('../pages/RoomPage.vue'),
  login: () => import('../pages/LoginPage.vue'),
  register: () => import('../pages/RegisterPage.vue'),
  verify: () => import('../pages/VerifyEmailPage.vue'),
  help: () => import('../pages/HelpPage.vue'),
  'public-rooms': () => import('../pages/PublicRoomsPage.vue'),
  admin: () => import('../pages/admin/AdminUsersPage.vue'),
  'admin-rooms': () => import('../pages/admin/AdminRoomsPage.vue'),
  'admin-ops': () => import('../pages/admin/AdminOpsPage.vue'),
  about: () => import('../pages/ComingSoonPage.vue'),
  user: () => import('../pages/UserPage.vue'),
  me: () => import('../pages/MePage.vue'),
  friends: () => import('../pages/FriendsPage.vue'),
  official: () => import('../pages/OfficialPage.vue'),
  forum: () => import('../pages/ForumPage.vue'),
  'anime-board': () => import('../pages/AnimeBoardPage.vue'),
  posts: () => import('../pages/PostsPage.vue'),
  'post-editor': () => import('../pages/PostEditorPage.vue'),
  'post-detail': () => import('../pages/PostDetailPage.vue'),
  messages: () => import('../pages/MessagesPage.vue'),
} as const

/** 路由名 → 页面模块加载函数的键名。 */
export type PageModuleName = keyof typeof PAGE_MODULES
