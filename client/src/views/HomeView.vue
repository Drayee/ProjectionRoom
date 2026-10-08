<script setup lang="ts">
/**
 * 首页：**一个路由两种形态**。
 *
 *   - 未登录：直接就是登录/注册界面（`AuthPanel` 用标签页在两者之间切换，URL 不变），
 *     同一屏右边是「直接进入房间」（免登录，观众路径）—— 落地页不再是"先看一屏宣传文案"；
 *   - 已登录：直接是核心功能页（建房 / 我的房间 / 加入房间）。
 *
 * 为什么不做成两个路由 + 守卫：授权 100% 在服务端（规格 §11），守位只能控"看到什么"，
 * 而观众进房本来就不需要登录，一个"未登录一律跳登录页"的守位会顺手把那扇门关上。
 * 这里只按**当前登录态**换渲染内容，`/login`、`/register` 仍然保留（回跳与深链依赖它们）。
 *
 * 长说明（为什么建房要登录、怎么准备片源、房间码/密码是什么、卡顿怎么办、诊断指标、
 * 隐私与安全、常见问题）全部搬到 `/help`，本页每个入口只留一句话 + 指向它的图标链接。
 */
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import AuthPanel from '../components/AuthPanel.vue'
import BrandIcon from '../components/BrandIcon.vue'
import CreateRoomCard from '../components/CreateRoomCard.vue'
import JoinRoomCard from '../components/JoinRoomCard.vue'
import MyRoomsCard from '../components/MyRoomsCard.vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()

/** 开源仓库地址（作者 drayee）。 */
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

const NAME_KEY = 'pr:name'

/**
 * 本机昵称。优先级：本机上次用过的 > **账号昵称**（登录了就不用再想办法起名字）> 随机。
 * 从登录页回跳到首页时组件会重新创建，所以用账号昵称兜底这件事在"登录后回到发起处"时也成立。
 *
 * 建房与进房共用这一份（通过 `v-model:name`），不再各存一份。
 */
const displayName = ref(
  localStorage.getItem(NAME_KEY) ?? auth.profile?.displayName ?? `观众${Math.floor(Math.random() * 900 + 100)}`,
)
</script>

<template>
  <div class="home">
    <AppHeader variant="page" />

    <header class="home-head">
      <img class="logo" src="/icons/夜晚.svg" alt="" width="34" height="34" aria-hidden="true" />
      <div class="head-text">
        <h1>月喵</h1>
        <p class="muted">一起看 · 作者 drayee · 开源</p>
      </div>
    </header>

    <nav class="home-tools" aria-label="片源与帮助">
      <RouterLink class="tool" :to="{ name: 'help', hash: '#source' }">
        <BrandIcon name="video" decorative :size="15" />
        片源准备
      </RouterLink>
      <RouterLink class="tool" :to="{ name: 'help' }">
        <BrandIcon name="tips" decorative :size="15" />
        帮助
      </RouterLink>
      <a
        class="tool icon-only"
        :href="REPO_URL"
        target="_blank"
        rel="noopener noreferrer"
        aria-label="开源仓库（作者 drayee）"
        title="开源仓库（作者 drayee）"
        data-testid="home-github"
      >
        <BrandIcon name="github" decorative :size="17" />
      </a>
      <span class="muted tiny">服务器只做信令与房间状态，不传输视频字节。</span>
    </nav>

    <!-- 未登录：登录/注册 + 同一屏的「直接进入房间」。 -->
    <div class="grid landing" v-if="!auth.isLoggedIn">
      <AuthPanel />
      <JoinRoomCard
        v-model:name="displayName"
        heading="直接进入房间"
        direct
        data-testid="home-direct-join"
      />
    </div>

    <!-- 已登录：核心功能页（不再出现登录表单）。 -->
    <template v-else>
      <div class="grid">
        <CreateRoomCard v-model:name="displayName" />
        <JoinRoomCard v-model:name="displayName" :show-name="false" heading="加入房间" />
      </div>
      <MyRoomsCard />
    </template>
  </div>
</template>

<style scoped>
.home {
  max-width: 980px;
  margin: 0 auto;
  padding: 36px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.home-head {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.logo {
  flex: none;
}

.head-text {
  min-width: 0;
}

.home-head h1 {
  margin: 0 0 2px;
  font-size: 26px;
}

.home-head p {
  margin: 0;
  font-size: 12px;
}

.home-tools {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.tool {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 12px;
  color: var(--text-dim);
  text-decoration: none;
}

.tool:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}

.tool.icon-only {
  padding: 5px;
  line-height: 0;
}

.tiny {
  font-size: 11px;
}

.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  align-items: start;
}

/* 落地页在窄屏上先看到登录/注册，再看到「直接进入房间」。 */
.landing {
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
}

@media (max-width: 760px) {
  .grid,
  .landing {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
