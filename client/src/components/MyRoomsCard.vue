<script setup lang="ts">
/**
 * 我的房间（本机最近进过的房间）。
 *
 * 数据来源只有一处：`utils/joinSession` 写进 sessionStorage 的进房凭据（`pr:join:<房间码>`）。
 * 刻意**不新增接口**：服务端只有"建房"与"按房间码查询单间"，没有"列出我建过的房间"，
 * 前端凭本地凭据列出来才是诚实的 —— 代价是它跟着**本标签页**走，关掉标签页就会清空，
 * 所以卡片标题与说明都写明了这一点，不让用户误以为这是账号级的房间列表。
 */
import { onMounted, ref } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'

interface MyRoom {
  code: string
  role: 'host' | 'viewer'
}

const router = useRouter()
const rooms = ref<MyRoom[]>([])

const PREFIX = 'pr:join:'

function load() {
  const list: MyRoom[] = []
  try {
    for (let i = 0; i < sessionStorage.length; i += 1) {
      const key = sessionStorage.key(i)
      if (!key || !key.startsWith(PREFIX)) continue
      const code = key.slice(PREFIX.length).toUpperCase()
      if (!code) continue
      let role: 'host' | 'viewer' = 'viewer'
      try {
        const raw = sessionStorage.getItem(key)
        const parsed = raw ? (JSON.parse(raw) as { role?: string }) : null
        if (parsed?.role === 'host') role = 'host'
      } catch {
        // 脏数据按观众处理：角色只影响这里显示哪一个图标，不影响进房（凭据仍在）。
      }
      list.push({ code, role })
    }
  } catch {
    // 隐私模式下 sessionStorage 可能直接抛错：当作"没有记录"，不挡页面。
  }
  // 主播房间排前面（刚建的房间通常就是要回去的那个），同角色按房间码稳定排序。
  rooms.value = list.sort((a, b) => (a.role === b.role ? a.code.localeCompare(b.code) : a.role === 'host' ? -1 : 1))
}

onMounted(load)

/** 进房凭据已经在 sessionStorage 里，`RoomView` 会自己读；这里只负责跳转。 */
function enter(room: MyRoom) {
  void router.push(`/room/${room.code}`)
}
</script>

<template>
  <section class="card my-rooms">
    <h2>
      <BrandIcon name="star" decorative :size="16" />
      我的房间
      <span class="muted small">本标签页最近进过的房间（关掉标签页即清空）</span>
    </h2>

    <ul v-if="rooms.length > 0">
      <li v-for="room in rooms" :key="room.code">
        <BrandIcon
          v-if="room.role === 'host'"
          name="star"
          label="你创建的房间（主播）"
          :size="14"
          class="host-star"
        />
        <span class="code mono">{{ room.code }}</span>
        <span class="badge" :class="{ host: room.role === 'host' }">
          {{ room.role === 'host' ? '主播' : '观众' }}
        </span>
        <button type="button" class="ghost" @click="enter(room)">进入</button>
      </li>
    </ul>

    <p class="muted note" v-else>
      本标签页还没有进过任何房间。建一个房，或者用房间码直接进入。
      <RouterLink :to="{ name: 'help', hash: '#room-code' }" class="hint-link">
        <BrandIcon name="tips" decorative :size="13" />
        房间码是什么
      </RouterLink>
    </p>
  </section>
</template>

<style scoped>
h2 {
  margin: 0 0 12px;
  font-size: 16px;
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
}

.small {
  font-size: 12px;
  font-weight: 400;
}

ul {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

li {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.host-star {
  color: var(--accent-2);
}

.code {
  font-size: 15px;
  letter-spacing: 2px;
}

.note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
}

.hint-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--accent);
}
</style>
