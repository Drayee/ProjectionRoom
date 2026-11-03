<script setup lang="ts">
/**
 * 首页：落地页（hero ↔ 登录窗 morph）↔ 已登录外壳。
 *
 * 顶栏由全局外壳（App.vue）常驻，本页不再自己渲染：
 *   未登录 → 路由 meta.header='none'，全屏落地页无顶栏；
 *   已登录 → 同一 meta 由全局外壳按登录态渲染 shell 态顶栏
 *   （分区图标导航 + 头像），换页不销毁重建。
 *
 * # 已登录态的构成（本块重做）
 *
 * 旧版只有一个「等待加入房间」占位文字 —— 它把**所有主动动作都藏起来了**：
 * 建房入口早在 4454c63 就被删掉且从未重建，发帖只剩 `/posts` 页头右上角一处。
 * 现在的构成是：
 *
 *   FAB（新建房间 / 发帖 / 加番剧）  ← 全局写入口，回首页就能看见
 *   建房卡片 + 进房卡片（并排）      ← 两条主路径，一眼可选
 *
 * 「等待加入房间」那句话**删掉**了：它描述的是"我什么都没有"，而页面上现在有卡片；
 * 留着它会让用户以为下面那些是可选项、真正的入口在别处。
 *
 * # FAB 三个动作的落点
 *
 *   新建房间 → 滚动到建房卡片并触发它的 `create()`（不重复实现建房逻辑）
 *   发送帖子 → `/posts/new`
 *   添加番剧 → `/forum`（番剧目录页，那里才有"发主题"的入口）
 */
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import CreateRoomCard from '../components/features/room/CreateRoomCard.vue'
import JoinRoomCard from '../components/features/room/JoinRoomCard.vue'
import HomeFab from '../components/features/shell/HomeFab.vue'
import LandingView from '../components/features/shell/LandingView.vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const booting = computed(() => auth.restoring)

const createCard = ref<InstanceType<typeof CreateRoomCard> | null>(null)

/**
 * 进房卡片的昵称来源。
 *
 * 与建房卡片**共用同一份** `auth.displayName`（账号昵称）。传入的是**函数**而不是
 * 字符串：两张卡片都按 getter 签名收昵称，传值会让它们拿到挂载那一刻的快照 ——
 * 用户在别的标签页登录/改名后，这一页还是旧的空名字。
 */
function displayName(): string {
  return auth.displayName || '主播'
}

/**
 * FAB「新建房间」：把卡片带到眼前再触发它。
 *
 * 先 `scrollIntoView` 是必要的 —— 卡片可能已经在视口外（窄屏单列时它在下方），
 * 直接建房会让页面**在用户什么都没看到的情况下跳走**，看起来像误触。
 */
function onCreateRoom(): void {
  const el = createCard.value?.$el as HTMLElement | undefined
  el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  createCard.value?.create()
}

function onPost(): void {
  void router.push({ name: 'post-editor' })
}

function onAnime(): void {
  void router.push({ name: 'forum' })
}
</script>

<template>
  <div v-if="booting" class="boot dim">恢复登录中…</div>
  <div v-else-if="auth.isLoggedIn" class="shell" data-testid="home-shell">
    <main class="body">
      <!-- FAB 在内容之上、居中：它是"我要开始做点什么"的第一落点。 -->
      <div class="fab-row">
        <HomeFab @create="onCreateRoom" @post="onPost" @anime="onAnime" />
      </div>

      <!-- 两条主路径并排：桌面两列，窄屏单列（建房在前 —— 那是主播视角的主路径）。 -->
      <div class="pair">
        <CreateRoomCard ref="createCard" :name="displayName" />
        <JoinRoomCard :name="displayName" />
      </div>
    </main>
  </div>
  <LandingView v-else />
</template>

<style scoped>
.boot {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
}

.shell {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.body {
  flex: 1;
  width: min(880px, 100%);
  margin: 0 auto;
  padding: 28px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 22px;
}

.fab-row {
  display: flex;
  justify-content: center;
}

/* 两列栅格：`minmax(0, 1fr)` 而不是 `1fr` —— 卡片里有 input，
   默认的 min-width: auto 会让长 placeholder 把一列撑破。 */
.pair {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  align-items: start;
}

@media (max-width: 720px) {
  .pair {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
