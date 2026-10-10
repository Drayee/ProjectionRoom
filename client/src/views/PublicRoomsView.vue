<script setup lang="ts">
/**
 * 公开房间列表（`/rooms`）。
 *
 * 三条产品决定，写在最前面：
 *   1. **免登录**。这一页与"直接进入房间"是同一条路径：点卡片就是 `router.push('/room/<code>')`，
 *      没有任何账号拦截、也没有全局路由守卫（路由表刻意不设守卫，见 router.ts 的说明）。
 *      进房凭据由 `RoomView` 自己按 `pr:join:<码>` 决定，没凭据时它会回首页重新走流程 ——
 *      那正是我们要的行为（这一页只负责把人送到房间门口）。
 *   2. **列表里没有的东西就是没有**。服务端给的是字段白名单（无密码、无成员明细、
 *      无房主邮箱/角色/状态），所以这里显示的每一项都能被服务端的那份白名单解释；
 *      卡片里也不做"点进去猜密码"这类补充，密码只以一格锁标记存在。
 *   3. **一律文本插值**。标题与昵称都是用户可控文本，任何 `v-html`/`innerHTML` 都是注入面；
 *      这一页只有 `{{ }}`。
 *
 * 四态：
 *   加载（首屏骨架）/ 空（还没有公开的房间）/ 失败（服务端文案 + 重试）/ 加载更多。
 */

import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import { PUBLIC_ROOMS_EMPTY_TEXT } from '../api/publicRooms'
import { usePublicRoomsStore } from '../stores/publicRooms'

const store = usePublicRoomsStore()

onMounted(() => {
  void store.load()
})

/**
 * 进房目标。
 *
 * 房间码统一大写：服务端的房间码是大写字母+数字，而 sessionStorage 的键
 * （`pr:join:<码>`）也是大写 —— 两处不一致的话，`MyRoomsCard` 记下的凭据会读不到。
 */
function roomPath(roomId: string): string {
  return `/room/${roomId.toUpperCase()}`
}
</script>

<template>
  <div class="rooms">
    <AppHeader variant="page" />

    <header class="rooms-head">
      <div class="head-text">
        <h1>
          <BrandIcon name="users-group" label="公开房间" :size="20" />
          公开房间
        </h1>
        <p class="muted">
          这些房间由房主主动公开。<strong>进房不需要账号</strong>，有密码的房间需要房间密码。
        </p>
      </div>
      <button
        type="button"
        class="icon-btn refresh"
        :disabled="store.loading"
        :aria-busy="store.loading"
        aria-label="刷新公开房间列表"
        v-tip="'刷新公开房间列表'"
        data-testid="public-rooms-refresh"
        @click="store.load()"
      >
        <BrandIcon name="refresh" decorative :size="16" :class="{ spinning: store.loading }" />
      </button>
    </header>

    <!-- 失败态：原样显示服务端文案 + 一个重试入口。 -->
    <div class="error-bar" v-if="store.error" data-testid="public-rooms-error">
      <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
      <span class="msg">{{ store.error }}</span>
      <button
        type="button"
        class="icon-btn retry"
        :disabled="store.loading"
        aria-label="重试"
        v-tip="'重试'"
        data-testid="public-rooms-retry"
        @click="store.load()"
      >
        <BrandIcon name="refresh" decorative :size="15" />
      </button>
    </div>

    <!-- 加载态：骨架而不是空白。骨架的数量固定 3 条 —— 它的作用是"这一屏马上会有内容"，
         不需要与服务端的分页大小一致。 -->
    <ul v-if="store.loading && store.items.length === 0" class="room-list" aria-busy="true" data-testid="public-rooms-loading">
      <li v-for="n in 3" :key="n" class="room-card skeleton" aria-hidden="true">
        <span class="line w60"></span>
        <span class="line w40"></span>
      </li>
    </ul>

    <!-- 空态：只在"没有错误、确实取到了 0 条"时出现（三态互斥，不会同时看到空与错）。 -->
    <p class="empty card" v-else-if="store.empty" data-testid="public-rooms-empty">
      {{ PUBLIC_ROOMS_EMPTY_TEXT }}
      <span class="muted">：房主把房间公开后就会出现在这里。</span>
    </p>

    <ul class="room-list" v-else-if="store.items.length > 0" data-testid="public-rooms-list">
      <li v-for="room in store.items" :key="room.roomId" class="room-card">
        <!-- 整张卡片是一个链接：键盘可达、可右击新标签打开，也不需要自己实现点击区域。 -->
        <RouterLink
          class="room-link"
          :to="roomPath(room.roomId)"
          :title="`进入房间 ${room.roomId.toUpperCase()}`"
          :data-testid="`public-room-${room.roomId.toUpperCase()}`"
        >
          <span class="title">{{ room.title || '未命名房间' }}</span>
          <span class="meta">
            <span class="code mono">{{ room.roomId.toUpperCase() }}</span>
            <span class="badge" v-if="room.hostOffline" data-testid="public-room-host-offline">
              主播离线中
            </span>
            <span class="muted" v-if="room.ownerName">房主 {{ room.ownerName }}</span>
            <span class="muted">
              <BrandIcon name="users-group" label="在座人数" :size="13" />
              {{ room.memberCount }} 人在座
            </span>
            <BrandIcon
              v-if="room.hasPassword"
              name="lock"
              label="有密码"
              :size="13"
              class="lock"
            />
          </span>
        </RouterLink>
      </li>
    </ul>

    <div class="pager" v-if="store.items.length > 0">
      <span class="muted small">已显示 {{ store.items.length }} / {{ store.total }} 个房间</span>
      <button
        type="button"
        :disabled="store.loadingMore || !store.hasMore"
        :aria-busy="store.loadingMore"
        data-testid="public-rooms-more"
        @click="store.loadMore()"
      >
        <BrandIcon :name="store.loadingMore ? 'refresh' : 'download'" decorative :size="14" :class="{ spinning: store.loadingMore }" />
        {{ store.loadingMore ? '加载中…' : store.hasMore ? '加载更多' : '没有更多了' }}
      </button>
    </div>

    <p class="muted small foot">
      列表只包含<strong>显式公开且当前存在</strong>的房间；房间码与密码怎么用见
      <RouterLink :to="{ name: 'help', hash: '#room-code' }" class="hint-link">
        <BrandIcon name="tips" decorative :size="13" />
        帮助
      </RouterLink>
      。
    </p>
  </div>
</template>

<style scoped>
.rooms {
  max-width: 980px;
  margin: 0 auto;
  padding: 36px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.rooms-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.head-text {
  min-width: 0;
}

.rooms-head h1 {
  margin: 0 0 4px;
  font-size: 22px;
  display: flex;
  align-items: center;
  gap: 7px;
}

.rooms-head p {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
}

button.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 5px;
  background: transparent;
  border-color: transparent;
  color: var(--text-dim);
  line-height: 0;
}

button.icon-btn:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--border);
}

button.icon-btn.retry {
  color: inherit;
}

.room-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.room-card {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
}

/* 链接铺满整张卡片：点击区 = 卡片，且不需要额外的 tabindex。 */
.room-link {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 14px;
  text-decoration: none;
  color: var(--text);
  border-radius: 10px;
}

.room-link:hover {
  border-color: var(--accent);
  text-decoration: none;
}

.title {
  font-size: 15px;
  font-weight: 600;
  /* 长标题必须能断行：标题是房主自己起的，60 个汉字的标题在 375px 上一行放不下。 */
  overflow-wrap: anywhere;
}

.meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  min-width: 0;
}

.code {
  font-size: 13px;
  letter-spacing: 2px;
}

.lock {
  color: var(--text-dim);
}

.empty {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.pager button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.small {
  font-size: 12px;
}

.foot {
  margin: 0;
  line-height: 1.6;
}

.hint-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

/* 骨架：一块浅色占位，不显示任何文字（读屏由容器的 aria-busy 承担）。 */
.skeleton {
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.skeleton .line {
  height: 10px;
  border-radius: 6px;
  background: var(--panel-2);
}

.skeleton .w60 {
  width: 60%;
}

.skeleton .w40 {
  width: 40%;
}

.spinning {
  animation: rooms-spin 0.9s linear infinite;
}

@keyframes rooms-spin {
  to {
    transform: rotate(360deg);
  }
}

/* 375px：卡片内的元信息已经 flex-wrap，这里只需收紧外边距，
   避免长房间码 + 昵称 + 人数挤出一行横向滚动。 */
@media (max-width: 460px) {
  .rooms {
    padding: 20px 12px 32px;
  }

  .rooms-head h1 {
    font-size: 19px;
  }

  .meta {
    gap: 8px;
  }
}
</style>
