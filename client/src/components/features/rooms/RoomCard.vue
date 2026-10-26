<script setup lang="ts">
/**
 * 房间卡片（`/rooms` 与 `/official` 共用的**唯一**一张卡片）。wayfinder #34。
 *
 * 为什么抽出来而不是在官方页再写一份：两张列表的字段、状态口径与进房入口完全一样，
 * 差别只有"这一行是不是运营位"。抄第二份的代价是 `主播离线中` 这类文案会漂移
 * （docs/ACCOUNTS.md §6 的措辞是列表契约的一部分），而这种漂移没有任何编译期检查。
 *
 * 根元素是 `<li>`：两个调用点都在 `<ul>` 里，列表语义由调用方保留。
 *
 * 状态口径（docs/ACCOUNTS.md §6）：
 *   - `hostOnline`  → 「直播中」
 *   - `hostOffline` → 「主播离线中」（宽限期内的房间仍可按码进入，不能显示成"已结束"）
 *   - 两者都不是    → **不显示状态**（不把"不知道"说成"离线"）
 *
 * 官方标识只看数据里的 `isOfficial`（#35 让服务端在普通列表里也返回它），
 * 不用"出现在官方页 = 官方"反推 —— 那样一旦列表筛选写错，界面会跟着一起错。
 */
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from '../../ui/BrandIcon.vue'
import type { PublicRoom } from '../../../api/publicRooms'

const props = defineProps<{ room: PublicRoom }>()

/** 状态文案：空串 = 服务端没给出可判定的状态（不猜成离线）。 */
const status = computed(() => {
  if (props.room.hostOnline) return '直播中'
  if (props.room.hostOffline) return '主播离线中'
  return ''
})
</script>

<template>
  <li class="card">
    <div class="head">
      <BrandIcon name="video" decorative :size="15" />
      <span class="name">{{ room.title || '未命名房间' }}</span>
      <!-- 运营标识：官方区与公开列表都显示（同一个 isOfficial 来源）。 -->
      <span v-if="room.isOfficial" class="badge">官方</span>
      <span v-if="room.hasPassword" class="lock" role="img" aria-label="有密码">
        <BrandIcon name="shield-done" decorative :size="13" />
      </span>
    </div>

    <p class="meta">
      <span v-if="room.ownerName" class="meta-item">房主 · {{ room.ownerName }}</span>
      <span class="meta-item mono">{{ room.roomId }}</span>
      <span class="meta-item">{{ room.memberCount }} 人在看</span>
      <span v-if="status" class="status" :class="status === '直播中' ? 'live' : 'offline'">{{ status }}</span>
    </p>

    <RouterLink class="enter" :to="{ name: 'room', params: { roomId: room.roomId } }">进房</RouterLink>
  </li>
</template>

<style scoped>
.card {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-areas:
    'head enter'
    'meta enter';
  align-items: center;
  gap: 6px 12px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
}

.head {
  grid-area: head;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  color: var(--dim);
}

.name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  color: var(--text);
}

.badge {
  flex: none;
  padding: 0 7px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 11px;
  line-height: 18px;
}

.lock {
  flex: none;
  display: inline-flex;
  color: var(--dim);
}

.meta {
  grid-area: meta;
  margin: 0;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 10px;
  font-size: 12px;
  color: var(--dim);
}

.mono {
  font-family: var(--font-mono);
}

/* 状态用「文字 + 前置圆点」表达，不单靠颜色（tokens.css 的硬约束 2）。 */
.status.live::before {
  content: '';
  display: inline-block;
  width: 6px;
  height: 6px;
  margin-right: 5px;
  border-radius: 50%;
  background: var(--accent);
  vertical-align: 1px;
}

.status.offline {
  color: var(--danger);
}

.enter {
  grid-area: enter;
  flex: none;
  padding: 5px 12px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 12px;
}

.enter:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}

/* 375 档：进房按钮换到第三行，避免把标题挤成一两个字。 */
@media (max-width: 560px) {
  .card {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas:
      'head'
      'meta'
      'enter';
  }

  .enter {
    justify-self: start;
  }
}
</style>
