<script setup lang="ts">
/**
 * 顶部导航壳：品牌、帮助/GitHub 入口与账号区的**唯一**渲染点
 *（首页、房间页、登录/注册页共用一份，避免三处各写一套"未登录/恢复中/已登录"的判定而慢慢漂移）。
 *
 * 四种形态（一个用法一种，不做"什么都能配"的万能组件）：
 *   - page：普通功能页顶栏（公开房间列表等）；
 *   - shell：**首页已登录态的标题栏** —— 月喵**居中放大**，分区 **icon-first** 分列两侧
 *     （房间室/番剧论坛 在左，帖子/消息/管理界面 在右，房间室的公开/好友/官方收进 hover 下拉），
 *     右上角账号区升级为「头像 + 昵称」；
 *   - inline：房间页头，塞进已有的 `.room-head .right` 那一行里 ——
 *     **不新增一行**，因为房间页是 `height:100vh` 的固定布局，多一行就是从播放器身上抠高度；
 *   - bare：登录/注册页，只放品牌与帮助/GitHub（不放账号区：这一屏本身就是登录入口）。
 *
 * 窄屏（375px）：整个顶栏 `flex-wrap` + 品牌文字在窄屏收起为品牌标（可访问名不丢，
 * 由 `aria-label` 承担），避免出现横向滚动条。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useAuthStore } from '../stores/auth'

const props = withDefaults(
  defineProps<{ variant?: 'page' | 'inline' | 'bare' | 'shell' }>(),
  { variant: 'page' },
)

/** 开源仓库地址（作者 drayee）。 */
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const busy = ref(false)
const isBare = computed(() => props.variant === 'bare')
const isShell = computed(() => props.variant === 'shell')

/** 登录/注册链接带上**当前路径**：登录成功后回到发起处（判据②，不留死路）。 */
const toAuth = computed(() => ({ redirect: route.fullPath }))

async function doLogout() {
  if (busy.value) return
  busy.value = true
  try {
    // notice 由 store 负责（服务端没收到登出请求时会说清"服务端会话可能仍然有效"）。
    await auth.logout()
    // 判据⑤：登出后回首页，并且此时内存与 sessionStorage 里的 access token 都已清空。
    await router.push('/')
  } finally {
    busy.value = false
  }
}

// ---------- 房间室下拉（分区 icon-first 后，「公开/好友/官方」收进 hover/focus 下拉） ----------
/** 下拉是否展开。鼠标 hover 或键盘 focus 进入都会展开；`aria-expanded` 与视觉同源。 */
const roomsOpen = ref(false)

function openRooms(): void {
  roomsOpen.value = true
}

function closeRooms(): void {
  roomsOpen.value = false
}

/** focus 在「按钮 ↔ 菜单项」之间移动时不收起；只有真移出容器才收。 */
function onRoomsFocusOut(event: FocusEvent): void {
  const container = event.currentTarget as HTMLElement
  const next = event.relatedTarget as Node | null
  if (next && container.contains(next)) return
  closeRooms()
}

// ---------- 头像兜底 ----------
//
// 本轮**没有任何头像图**（服务端档案里没有这个字段），所以"头像"就是兜底方案本身：
// 昵称首字 + 主题色圆底。为什么不做成 `<img>` 再靠 onerror 兜底：那会在每次进首页时
// 稳定打一个注定 404 的请求，且首帧会闪一下空白圆。
//
// 配色由用户名哈希决定（同一个人永远同一个颜色，不同人大概率不同色），
// 全部取自既有主题色，不新增色板。
const AVATAR_COLORS = ['var(--accent)', 'var(--accent-2)', '#8b7bf0', '#3ddc97'] as const

const avatarChar = computed(() => {
  const name = auth.displayName.trim()
  if (name === '') return '月'
  // 用扩展运算符取**第一个码点**：直接取 [0] 会把 emoji/星号平面字符切成半个代理对。
  return [...name][0] ?? '月'
})

const avatarColor = computed(() => {
  const seed = auth.profile?.username || auth.displayName
  let hash = 0
  for (const ch of seed) hash = (hash * 31 + ch.codePointAt(0)!) % 9973
  return AVATAR_COLORS[hash % AVATAR_COLORS.length]
})

/** 头像可访问名：带上昵称，读屏才知道这个圆是谁。 */
const avatarLabel = computed(() => `我的主页（${auth.displayName}）`)
</script>

<template>
  <header class="app-bar" :class="props.variant">
    <div class="top-row">
      <div class="brand-group">
        <!-- 品牌标（非 shell）：放左侧，与快捷入口同行。 -->
        <RouterLink
          v-if="!isShell"
          class="brand"
          to="/"
          :aria-label="'月喵（返回首页）'"
          data-testid="auth-brand"
        >
          <img class="logo" src="/icons/夜晚.svg" alt="" width="22" height="22" aria-hidden="true" />
          <span class="brand-text">月喵</span>
        </RouterLink>

        <nav v-if="!isShell" class="quick" aria-label="站点入口">
          <!-- 公开房间列表（二期 T5）：**对所有访客可见**（含未登录）。
               它是"发现房间"的入口，与"按房间码进房"并列，而不是登录后的特权。 -->
          <RouterLink
            class="icon-btn"
            :to="{ name: 'public-rooms' }"
            aria-label="公开房间"
            v-tip="'公开房间'"
            data-testid="nav-public-rooms"
          >
            <BrandIcon name="video" decorative :size="17" />
          </RouterLink>
          <RouterLink
            class="icon-btn"
            :to="{ name: 'help' }"
            aria-label="帮助与说明"
            v-tip="'帮助与说明'"
            data-testid="nav-help"
          >
            <BrandIcon name="tips" decorative :size="17" />
          </RouterLink>
          <!-- 作者介绍页：内容最小（"待补充"），但入口先立住。 -->
          <RouterLink
            class="icon-btn"
            :to="{ name: 'about' }"
            aria-label="关于作者"
            v-tip="'关于作者'"
            data-testid="nav-about"
          >
            <BrandIcon name="star" decorative :size="17" />
          </RouterLink>
          <!-- 管理端（二期 T7）：入口只对管理员显示 —— 但**显示不是授权**。
               服务端才是授权判定的唯一来源（RequireAdmin），非 admin 打开 /admin
               会拿到 403 并看到「无权限」提示。这里读的是本地缓存的 role：它可能过期，
               因此宁可晚显示一步，也绝不据此判断"能不能操作"（一期不变量 I3）。 -->
          <RouterLink
            v-if="!isBare && !isShell && auth.profile?.role === 'admin'"
            class="icon-btn"
            :to="{ name: 'admin' }"
            aria-label="管理端"
            v-tip="'管理端'"
            data-testid="nav-admin"
          >
            <BrandIcon name="settings" decorative :size="17" />
          </RouterLink>
          <a
            class="icon-btn"
            :href="REPO_URL"
            target="_blank"
            rel="noopener noreferrer"
            aria-label="开源仓库（作者 drayee）"
            v-tip="'开源仓库（作者 drayee）'"
            data-testid="nav-github"
          >
            <BrandIcon name="github" decorative :size="17" />
          </a>
        </nav>

        <!-- 左分区（shell）：房间室（下拉）、番剧论坛 —— icon-first。 -->
        <nav v-if="isShell" class="zones" aria-label="站点分区（左）" data-testid="shell-zones-left">
          <div
            class="zone-dropdown"
            data-testid="zone-rooms"
            @mouseenter="openRooms"
            @mouseleave="closeRooms"
            @focusin="openRooms"
            @focusout="onRoomsFocusOut"
          >
            <button
              type="button"
              class="zone-pill"
              aria-label="房间室"
              v-tip="'房间室'"
              :aria-expanded="roomsOpen"
              aria-haspopup="true"
            >
              <BrandIcon name="video" decorative :size="18" />
            </button>
            <div class="zone-menu" :class="{ open: roomsOpen }" role="menu" aria-label="房间室">
              <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'public-rooms' }" data-testid="zone-rooms-public">
                公开
              </RouterLink>
              <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'friends' }" data-testid="zone-rooms-friends">
                好友
              </RouterLink>
              <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'official' }" data-testid="zone-rooms-official">
                官方
              </RouterLink>
            </div>
          </div>

          <RouterLink class="zone-pill" :to="{ name: 'forum' }" aria-label="番剧论坛" v-tip="'番剧论坛'" data-testid="zone-forum">
            <BrandIcon name="chat" decorative :size="18" />
          </RouterLink>
        </nav>
      </div>

      <!-- 品牌（shell）：居中 + 放大 1.5 倍。 -->
      <RouterLink
        v-if="isShell"
        class="brand brand-center"
        to="/"
        :aria-label="'月喵（返回首页）'"
        data-testid="auth-brand"
      >
        <img class="logo" src="/icons/夜晚.svg" alt="" width="28" height="28" aria-hidden="true" />
        <span class="brand-text">月喵</span>
      </RouterLink>

      <div class="app-nav" :class="props.variant" data-testid="auth-nav">
        <!-- 右分区（shell）：帖子、消息、管理界面 —— icon-first。 -->
        <nav v-if="isShell" class="zones" aria-label="站点分区（右）" data-testid="shell-zones-right">
          <RouterLink class="zone-pill" :to="{ name: 'posts' }" aria-label="帖子" v-tip="'帖子'" data-testid="zone-posts">
            <BrandIcon name="tips" decorative :size="18" />
          </RouterLink>
          <RouterLink class="zone-pill" :to="{ name: 'messages' }" aria-label="消息" v-tip="'消息'" data-testid="zone-messages">
            <BrandIcon name="share" decorative :size="18" />
          </RouterLink>
          <RouterLink
            v-if="auth.profile?.role === 'admin'"
            class="zone-pill admin"
            :to="{ name: 'admin' }"
            aria-label="管理界面"
            v-tip="'管理界面'"
            data-testid="nav-admin"
          >
            <BrandIcon name="settings" decorative :size="18" />
          </RouterLink>
        </nav>

        <div v-if="!isBare" class="row">
          <!-- 恢复登录中：只在"没有 token 但有档案缓存"的首屏出现（等一次 refresh），
               避免先把已登录的用户显示成"未登录"再变回来。 -->
          <span v-if="auth.restoring" class="muted small" data-testid="auth-restoring">恢复登录中…</span>

          <template v-else-if="auth.isLoggedIn">
            <!-- 头像：昵称首字 + 主题色圆底（本轮没有头像图，这就是最终形态）。 -->
            <RouterLink
              class="avatar"
              :to="{ name: 'user', params: { id: String(auth.profile?.id ?? 0) } }"
              :aria-label="avatarLabel"
              v-tip="avatarLabel"
              data-testid="nav-avatar"
            >
              <span class="avatar-char" :style="{ background: avatarColor }" aria-hidden="true">
                {{ avatarChar }}
              </span>
            </RouterLink>
            <!-- 昵称一律文本插值：服务端已做字符白名单与去零宽/Bidi（规格 §7.3），前端不再碰 DOM。
                 shell 标题栏只留头像（icon-first，与 HTML 原型一致），昵称仅在其它形态显示。 -->
            <span v-if="!isShell" class="who" :title="auth.displayName" data-testid="auth-user">{{ auth.displayName }}</span>
            <button
              class="chip icon-btn"
              :disabled="busy"
              :aria-busy="busy"
              :aria-label="busy ? '正在退出登录…' : '退出登录（清除本机会话）'"
              v-tip="busy ? '正在退出登录…' : '退出登录（清除本机会话）'"
              data-testid="auth-logout"
              @click="doLogout"
            >
              <BrandIcon :name="busy ? 'refresh' : 'logout'" decorative :size="16" :class="{ spinning: busy }" />
            </button>
          </template>

          <template v-else>
            <RouterLink class="link" :to="{ name: 'login', query: toAuth }" data-testid="nav-login">
              <BrandIcon name="login" decorative :size="15" />
              登录
            </RouterLink>
            <RouterLink class="link" :to="{ name: 'register', query: toAuth }" data-testid="nav-register">
              <BrandIcon name="users-group" decorative :size="15" />
              注册
            </RouterLink>
          </template>
        </div>
      </div>
    </div>

    <p v-if="auth.notice" class="notice" data-testid="auth-notice">{{ auth.notice }}</p>
  </header>
</template>

<style scoped>
.app-bar {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.top-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px 16px;
  flex-wrap: wrap;
}

.app-bar.inline .top-row {
  align-items: center;
  gap: 8px;
}

.brand-group {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex-wrap: wrap;
}

.brand {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-weight: 600;
  font-size: 15px;
  text-decoration: none;
  white-space: nowrap;
}

.logo {
  flex: none;
}

.quick {
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 图标按钮的公共外形：点击区靠 padding 撑出来（图标本身只有 16~17px）。 */
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 5px;
  color: var(--text-dim);
  background: transparent;
  border: 1px solid transparent;
  border-radius: 999px;
  text-decoration: none;
  line-height: 0;
}

.icon-btn:hover {
  color: var(--text);
  border-color: var(--border);
  text-decoration: none;
}

.app-nav {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
  min-width: 0;
}

.app-nav.inline {
  align-items: center;
  flex-direction: row;
}

/* 房间页的 inline 形态本来就塞在 `.room-head .right` 那一行里（房间页是定高布局），
   那里已经另有昵称与房间码，品牌只需要留下标记：文字收起，可访问名由链接的 aria-label 承担。 */
.app-bar.inline .brand-text {
  display: none;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.who {
  max-width: 18ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 头像（兜底形态）：昵称首字 + 主题色圆底。 */
.avatar {
  display: inline-flex;
  border-radius: 50%;
  text-decoration: none;
  flex: none;
}

.avatar-char {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  color: #04141b;
  font-size: 14px;
  font-weight: 600;
  line-height: 1;
}

.avatar:hover .avatar-char {
  box-shadow: 0 0 0 2px var(--border);
}

/* ----- shell：标题栏月喵居中放大 + 分区 icon-first 分列两侧 ----- */
.app-bar.shell .top-row {
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 12px;
}

.app-bar.shell .brand-group {
  justify-content: flex-start;
}

/* 月喵放大 1.5 倍（15px → 24px），居中（与 HTML 原型一致）。 */
.app-bar.shell .brand.brand-center {
  justify-self: center;
  font-size: 24px;
}

.app-bar.shell .brand.brand-center .logo {
  width: 30px;
  height: 30px;
}

.app-bar.shell .app-nav {
  flex-direction: row;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

/* 外壳标题栏入场：登录成功后从上方滑入（与 HomeView 的整屏淡入叠加）。 */
.app-bar.shell {
  animation: shell-bar-in 0.4s cubic-bezier(0.22, 0.61, 0.36, 1) both;
}

@keyframes shell-bar-in {
  from {
    opacity: 0;
    transform: translateY(-14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* ----- 分区（icon-first）：只有图标 + aria-label + v-tip ----- */
.zones {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.zone-pill {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  padding: 0;
  border-radius: 50%;
  border: 1px solid transparent;
  color: var(--text-dim);
  background: transparent;
  text-decoration: none;
  line-height: 0;
  transition:
    color 0.18s ease,
    border-color 0.18s ease,
    background 0.18s ease,
    transform 0.18s ease;
}

.zone-pill:hover {
  color: var(--text);
  border-color: var(--border);
  transform: translateY(-1px);
  text-decoration: none;
}

.zone-pill.admin {
  border-color: var(--accent);
  color: var(--accent);
}

/* 房间室下拉：公开/好友/官方 收进 hover/focus 弹出菜单。 */
.zone-dropdown {
  position: relative;
  display: inline-flex;
}

.zone-menu {
  position: absolute;
  top: calc(100% + 8px);
  left: 50%;
  transform: translateX(-50%) translateY(-4px);
  min-width: 124px;
  padding: 6px;
  border-radius: 12px;
  background: var(--panel-2);
  border: 1px solid var(--border);
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.4);
  display: flex;
  flex-direction: column;
  gap: 2px;
  z-index: 30;
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transition:
    opacity 0.16s ease,
    transform 0.16s ease,
    visibility 0.16s ease;
}

.zone-menu.open {
  opacity: 1;
  visibility: visible;
  pointer-events: auto;
  transform: translateX(-50%) translateY(0);
}

.zone-menu-link {
  display: block;
  padding: 6px 10px;
  border-radius: 8px;
  color: var(--text);
  text-decoration: none;
  font-size: 13px;
  white-space: nowrap;
  transition: background 0.14s ease;
}

.zone-menu-link:hover {
  background: var(--panel);
  text-decoration: none;
}

button.chip {
  padding: 5px;
}

.link {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: var(--accent);
  text-decoration: none;
}

.small {
  font-size: 12px;
}

.notice {
  margin: 0;
  max-width: 420px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--danger);
}

.spinning {
  animation: brand-spin 0.9s linear infinite;
}

@keyframes brand-spin {
  to {
    transform: rotate(360deg);
  }
}

/* 降级：系统要求减少动效 → 入场动画与过渡全部瞬时。 */
@media (prefers-reduced-motion: reduce) {
  .app-bar.shell {
    animation: none;
  }

  .zone-pill,
  .zone-menu,
  .zone-menu-link {
    transition: none;
  }
}

/* 375px 档：品牌文字收起为品牌标（链接另有 aria-label），保证顶栏不横向溢出。 */
@media (max-width: 460px) {
  .brand-text {
    display: none;
  }

  .app-bar .app-nav {
    align-items: flex-start;
  }
}
</style>
