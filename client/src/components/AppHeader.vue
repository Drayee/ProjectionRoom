<script setup lang="ts">
/**
 * 顶部导航壳。四种形态：page / shell（月喵在左、分区 icon-first 随后）/ inline / bare。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useAuthStore } from '../stores/auth'

const props = withDefaults(defineProps<{ variant?: 'page' | 'inline' | 'bare' | 'shell' }>(), { variant: 'page' })
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const busy = ref(false)
const isBare = computed(() => props.variant === 'bare')
const isShell = computed(() => props.variant === 'shell')
const toAuth = computed(() => ({ redirect: route.fullPath }))

async function doLogout() {
  if (busy.value) return
  busy.value = true
  try { await auth.logout(); await router.push('/') } finally { busy.value = false }
}

const roomsOpen = ref(false)
function openRooms() { roomsOpen.value = true }
function closeRooms() { roomsOpen.value = false }
function onRoomsFocusOut(event: FocusEvent) {
  const container = event.currentTarget as HTMLElement
  const next = event.relatedTarget as Node | null
  if (next && container.contains(next)) return
  closeRooms()
}

const AVATAR_COLORS = ['var(--accent)', 'var(--accent-2)', '#8b7bf0', '#3ddc97'] as const
const avatarChar = computed(() => {
  const name = auth.displayName.trim()
  return name === '' ? '月' : [...name][0] ?? '月'
})
const avatarColor = computed(() => {
  const seed = auth.profile?.username || auth.displayName
  let hash = 0
  for (const ch of seed) hash = (hash * 31 + ch.codePointAt(0)!) % 9973
  return AVATAR_COLORS[hash % AVATAR_COLORS.length]
})
const avatarLabel = computed(() => `我的主页（${auth.displayName}）`)
</script>

<template>
  <header class="app-bar" :class="props.variant">
    <div class="top-row">
      <!-- shell：月喵在左 + 分区 icon-first 靠左、头像/登出在右 -->
      <template v-if="isShell">
        <div class="shell-inner">
          <div class="shell-left">
            <RouterLink class="brand" to="/" :aria-label="'月喵（返回首页）'" data-testid="auth-brand">
              <img class="logo" src="/icons/夜晚.svg" alt="" width="30" height="30" aria-hidden="true" />
              <span class="brand-text">月喵</span>
            </RouterLink>
            <nav class="zones" aria-label="站点分区">
              <div class="zone-dropdown" data-testid="zone-rooms" @mouseenter="openRooms" @mouseleave="closeRooms" @focusin="openRooms" @focusout="onRoomsFocusOut">
                <button type="button" class="zone-pill" aria-label="房间室" v-tip="'房间室'" :aria-expanded="roomsOpen">
                  <BrandIcon name="video" decorative :size="20" />
                </button>
                <div class="zone-menu" :class="{ open: roomsOpen }" role="menu">
                  <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'public-rooms' }">公开</RouterLink>
                  <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'friends' }">好友</RouterLink>
                  <RouterLink role="menuitem" class="zone-menu-link" :to="{ name: 'official' }">官方</RouterLink>
                </div>
              </div>
              <RouterLink class="zone-pill" :to="{ name: 'forum' }" aria-label="番剧论坛" v-tip="'番剧论坛'"><BrandIcon name="chat" decorative :size="20" /></RouterLink>
              <RouterLink class="zone-pill" :to="{ name: 'posts' }" aria-label="帖子" v-tip="'帖子'"><BrandIcon name="tips" decorative :size="20" /></RouterLink>
              <RouterLink class="zone-pill" :to="{ name: 'messages' }" aria-label="消息" v-tip="'消息'"><BrandIcon name="share" decorative :size="20" /></RouterLink>
              <RouterLink v-if="auth.profile?.role === 'admin'" class="zone-pill admin" :to="{ name: 'admin' }" aria-label="管理界面" v-tip="'管理界面'"><BrandIcon name="settings" decorative :size="20" /></RouterLink>
            </nav>
          </div>
          <div class="shell-right">
            <span v-if="auth.restoring" class="muted small">恢复登录中…</span>
            <template v-else-if="auth.isLoggedIn">
              <RouterLink class="avatar" :to="{ name: 'user', params: { id: String(auth.profile?.id ?? 0) } }" :aria-label="avatarLabel" v-tip="avatarLabel">
                <span class="avatar-char" :style="{ background: avatarColor }">{{ avatarChar }}</span>
              </RouterLink>
              <button class="chip icon-btn" :disabled="busy" :aria-busy="busy" :aria-label="busy ? '退出中' : '退出'" v-tip="busy ? '退出中' : '退出'" @click="doLogout">
                <BrandIcon :name="busy ? 'refresh' : 'logout'" decorative :size="18" :class="{ spinning: busy }" />
              </button>
            </template>
            <template v-else>
              <RouterLink class="link" :to="{ name: 'login', query: toAuth }"><BrandIcon name="login" decorative :size="15" /> 登录</RouterLink>
              <RouterLink class="link" :to="{ name: 'register', query: toAuth }"><BrandIcon name="users-group" decorative :size="15" /> 注册</RouterLink>
            </template>
          </div>
        </div>
      </template>

      <!-- 非 shell：page / inline / bare -->
      <template v-else>
        <div class="brand-group">
          <RouterLink class="brand" to="/" :aria-label="'月喵（返回首页）'" data-testid="auth-brand">
            <img class="logo" src="/icons/夜晚.svg" alt="" width="22" height="22" aria-hidden="true" />
            <span class="brand-text">月喵</span>
          </RouterLink>
          <nav class="quick" aria-label="站点入口">
            <RouterLink class="icon-btn" :to="{ name: 'public-rooms' }" aria-label="公开房间" v-tip="'公开房间'"><BrandIcon name="video" decorative :size="17" /></RouterLink>
            <RouterLink class="icon-btn" :to="{ name: 'help' }" aria-label="帮助" v-tip="'帮助'"><BrandIcon name="tips" decorative :size="17" /></RouterLink>
            <RouterLink class="icon-btn" :to="{ name: 'about' }" aria-label="关于" v-tip="'关于'"><BrandIcon name="star" decorative :size="17" /></RouterLink>
            <RouterLink v-if="auth.profile?.role === 'admin'" class="icon-btn" :to="{ name: 'admin' }" aria-label="管理端" v-tip="'管理端'"><BrandIcon name="settings" decorative :size="17" /></RouterLink>
            <a class="icon-btn" :href="REPO_URL" target="_blank" rel="noopener" aria-label="开源仓库" v-tip="'开源仓库'"><BrandIcon name="github" decorative :size="17" /></a>
          </nav>
        </div>
        <div class="app-nav" :class="props.variant">
          <div v-if="!isBare" class="row">
            <span v-if="auth.restoring" class="muted small">恢复登录中…</span>
            <template v-else-if="auth.isLoggedIn">
              <RouterLink class="avatar" :to="{ name: 'user', params: { id: String(auth.profile?.id ?? 0) } }" :aria-label="avatarLabel" v-tip="avatarLabel">
                <span class="avatar-char" :style="{ background: avatarColor }">{{ avatarChar }}</span>
              </RouterLink>
              <span v-if="!isShell" class="who" :title="auth.displayName">{{ auth.displayName }}</span>
              <button class="chip icon-btn" :disabled="busy" :aria-busy="busy" v-tip="'退出'" @click="doLogout">
                <BrandIcon :name="busy ? 'refresh' : 'logout'" decorative :size="16" :class="{ spinning: busy }" />
              </button>
            </template>
            <template v-else>
              <RouterLink class="link" :to="{ name: 'login', query: toAuth }"><BrandIcon name="login" decorative :size="15" /> 登录</RouterLink>
              <RouterLink class="link" :to="{ name: 'register', query: toAuth }"><BrandIcon name="users-group" decorative :size="15" /> 注册</RouterLink>
            </template>
          </div>
        </div>
      </template>
    </div>
    <p v-if="auth.notice" class="notice">{{ auth.notice }}</p>
  </header>
</template>

<style scoped>
.app-bar { display: flex; flex-direction: column; gap: 10px; }
.top-row { display: flex; align-items: center; justify-content: space-between; gap: 12px 16px; flex-wrap: wrap; }
.app-bar.inline .top-row { align-items: center; gap: 8px; }

/* ---------- shell：靠左，月喵放大 + 分区随后 ---------- */
.app-bar.shell .top-row { flex-wrap: nowrap; }
.shell-inner { display: flex; align-items: center; justify-content: space-between; width: 100%; gap: 16px; }
.shell-left { display: flex; align-items: center; gap: 16px; }
.shell-right { display: flex; align-items: center; gap: 10px; }

.brand {
  display: inline-flex; align-items: center; gap: 8px;
  font-weight: 700; font-size: 22px; text-decoration: none; white-space: nowrap;
}
.brand:hover { text-decoration: none; }
.logo { flex: none; }

.zones { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.zone-pill {
  display: inline-flex; align-items: center; justify-content: center;
  width: 38px; height: 38px; padding: 0; border-radius: 50%;
  border: 1px solid transparent; color: var(--text-dim);
  background: transparent; text-decoration: none; line-height: 0;
  transition: color 0.18s ease, border-color 0.18s ease, background 0.18s ease, transform 0.18s ease;
}
.zone-pill:hover { color: var(--text); border-color: var(--border); transform: translateY(-1px); text-decoration: none; }

.zone-dropdown { position: relative; display: inline-flex; }
.zone-menu {
  position: absolute; top: calc(100% + 8px); left: 50%;
  transform: translateX(-50%) translateY(-4px);
  min-width: 124px; padding: 6px;
  border-radius: 12px; background: var(--panel-2);
  border: 1px solid var(--border); box-shadow: 0 12px 32px rgba(0, 0, 0, 0.4);
  display: flex; flex-direction: column; gap: 2px; z-index: 30;
  opacity: 0; visibility: hidden; pointer-events: none;
  transition: opacity 0.16s ease, transform 0.16s ease, visibility 0.16s ease;
}
.zone-menu.open { opacity: 1; visibility: visible; pointer-events: auto; transform: translateX(-50%) translateY(0); }
.zone-menu-link {
  display: block; padding: 6px 10px; border-radius: 8px;
  color: var(--text); text-decoration: none; font-size: 13px; white-space: nowrap;
  transition: background 0.14s ease;
}
.zone-menu-link:hover { background: var(--panel); text-decoration: none; }

.zone-pill.admin { border-color: var(--accent); color: var(--accent); }

/* ---------- 通用 ---------- */
.brand-group { display: flex; align-items: center; gap: 10px; min-width: 0; flex-wrap: wrap; }
.quick { display: flex; align-items: center; gap: 6px; }

.icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 5px; color: var(--text-dim); background: transparent;
  border: 1px solid transparent; border-radius: 999px; text-decoration: none; line-height: 0;
}
.icon-btn:hover { color: var(--text); border-color: var(--border); text-decoration: none; }

.app-nav { display: flex; flex-direction: column; align-items: flex-end; gap: 6px; min-width: 0; }
.app-nav.inline { align-items: center; flex-direction: row; }
.app-bar.inline .brand-text { display: none; }

.row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.who { max-width: 18ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.avatar { display: inline-flex; border-radius: 50%; text-decoration: none; flex: none; }
.avatar-char {
  display: inline-flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 50%;
  color: #04141b; font-size: 14px; font-weight: 700; line-height: 1;
}
.avatar:hover .avatar-char { box-shadow: 0 0 0 2px var(--border); }

button.chip { padding: 5px; }
.link { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; color: var(--accent); text-decoration: none; }
.small { font-size: 12px; }
.notice { margin: 0; max-width: 420px; font-size: 12px; line-height: 1.5; color: var(--danger); }

.spinning { animation: brand-spin 0.9s linear infinite; }
@keyframes brand-spin { to { transform: rotate(360deg); } }

/* 外壳标题栏入场动画 */
.app-bar.shell { animation: shell-bar-in 0.4s var(--ease-out) both; }
@keyframes shell-bar-in { from { opacity: 0; transform: translateY(-14px); } to { opacity: 1; transform: none; } }

@media (prefers-reduced-motion: reduce) {
  .app-bar.shell { animation: none; }
  .zone-pill, .zone-menu, .zone-menu-link { transition: none; }
}
@media (max-width: 460px) {
  .brand-text { display: none; }
  .app-bar .app-nav { align-items: flex-start; }
}
</style>
