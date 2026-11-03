<script setup lang="ts">
/**
 * 建房卡片（主播入口，首页形态）。
 *
 * 旧版是 `client/src/components/CreateRoomCard.vue`（216 行），在 4454c63「基线——
 * 删除旧展示层」时被一并删除，此后**从未重建**：于是整个前端只剩"主播断线后自动重建"
 * 一条 `POST /api/rooms` 的路径，用户没有任何主动建房的入口。本组件把它接回首页。
 *
 * # 归 features/room/
 * 它发 REST 请求、读两个 store，按票 #44 §0 的分层不能放 `ui/`（那是纯展示件）。
 * 逻辑在 `useCreateRoom`，本组件只渲染 —— 与 `JoinRoomCard` / `useJoinRoom` 同一形状，
 * 因为它们是同一条契约的两端（一个建房、一个进房）。
 *
 * # 昵称为什么可编辑（而不是只读地取账号名）
 * 建房用的昵称是**进房时显示的名字**（随 join 凭据走），与账号档案上的昵称是两件事：
 * 主播完全可能想用另一个名字开播，而改账号昵称要走设置页（另一个动作、另一个后果）。
 * 所以这里保留旧实现的可编辑语义，只是把它**预填**成当前账号昵称 —— 用户什么都不做
 * 就是"用我的名字开播"，想改就地改。
 *
 * # 视觉对齐 JoinRoomCard
 * 两者会并排出现在首页，`input` 走全局 `base.css` 的样式，按钮与错误条必须
 * 用同一套语汇（`.primary` 全宽 / `.ghost` 次选 / `.alert` 危险描边）。
 * 刻意**不**用 `ui/Button`：那一套是"顶栏与列表里的动作按钮"，而这里是
 * 落地卡片内的表单主行动，重写一套会让两块卡片的按钮高度差 2px。
 *
 * # 未登录也渲染
 * 卡片不隐藏，而是显示一行门槛说明（点按钮时 `useCreateRoom` 会就地拦截并带去
 * 登录页，带 `?redirect=`）。隐藏它的话，游客根本不知道"这里能开房"。
 */
import { ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from '../../ui/BrandIcon.vue'
import { useCreateRoom } from './useCreateRoom'

/** 本机昵称的存储键（与 `useJoinRoom` 共用同一个值）。 */
const NAME_KEY = 'pr:name'

/**
 * `name` 用 getter 而不是普通字符串：首页的昵称来自 `auth.displayName`（响应式）。
 * 传值会让组件拿到挂载那一刻的快照 —— 用户在别的标签页登录/改了昵称，这里还是空的。
 * 这与 `JoinRoomCard` 的 `props.name` 完全同形。
 */
const props = defineProps<{ name: () => string }>()

/**
 * 输入框里的昵称。
 *
 * 预填顺序：本地存过的 `pr:name` → 账号昵称。前者优先是刻意的：用户在**这台机器**
 * 上一次用的名字比账号档案更贴近他此刻的意图（他可能一直用这个别名开播）。
 */
function seedName(): string {
  try {
    const saved = localStorage.getItem(NAME_KEY)
    if (saved && saved.trim() !== '') return saved.trim()
  } catch {
    /* 隐私模式：退回账号昵称 */
  }
  return props.name().trim()
}

const displayName = ref(seedName())

/**
 * 登录态到了但输入框还是空的 → 补上账号昵称。
 *
 * 这条是给"游客先看到卡片、点了按钮去登录、回跳后回到这里"那条路的：
 * 回跳时组件是新建的，`seedName()` 会在挂载时就拿到账号昵称，所以正常情况用不上；
 * 但它也覆盖"应用启动时 `bootstrap()` 还在跑"的时序（`auth.displayName` 后到）。
 * **只在空的时候补** —— 用户已经打过字就不能覆盖他。
 */
watch(
  () => props.name(),
  (next) => {
    if (displayName.value.trim() === '' && next.trim() !== '') {
      displayName.value = next.trim()
    }
  },
)

const c = useCreateRoom(() => displayName.value)

/**
 * 把建房动作暴露给首页（FAB 的「新建房间」）。
 *
 * 为什么是"暴露同一个函数"而不是在 FAB 里再写一遍：建房这条路径上有未登录拦截
 * （`ensureLoggedIn` 带 `?redirect=`）、401 刷新重试、昵称空值、房间凭据落
 * sessionStorage、主播复位令牌随行 —— 抄一份出来必然分叉。FAB 只负责
 * "把卡片带到眼前 + 触发它"。
 */
defineExpose({ create: c.create })
</script>

<template>
  <section class="card" aria-labelledby="create-room-head">
    <h2 id="create-room-head">
      <BrandIcon name="video" decorative :size="16" />
      作为主播创建房间
    </h2>

    <!-- 未登录时这张卡片不会被渲染（它在已登录外壳里），所以这里没有登录提示条。
         `useCreateRoom` 里的 `ensureLoggedIn()` 仍在：那是防"登录态在这一页渲染之后
         失效"的（token 过期、另一标签页登出），不是给游客看的。 -->

    <label for="create-name">你的昵称</label>
    <input
      id="create-name"
      v-model="displayName"
      maxlength="24"
      placeholder="主播昵称"
      autocomplete="nickname"
      data-testid="home-create-name"
    />

    <label for="create-password">房间密码（可留空）</label>
    <input
      id="create-password"
      v-model="c.password.value"
      type="password"
      placeholder="留空表示谁都能进"
      autocomplete="new-password"
      data-testid="home-create-password"
    />

    <p v-if="c.error.value" class="alert" role="alert" data-testid="home-create-error">{{ c.error.value }}</p>

    <div class="row">
      <button
        class="primary"
        type="button"
        :disabled="c.busy.value"
        :aria-busy="c.busy.value"
        data-testid="home-create-room"
        @click="c.create()"
      >
        {{ c.busy.value ? '创建中…' : '创建房间' }}
      </button>
    </div>

    <p class="note">
      创建后拿到房间码，分享给观众就能看片。片源怎么准备（本机切片器 / 服务端切片 / 下载切片器）见
      <RouterLink :to="{ name: 'help', hash: '#source' }" class="hint-link">
        <BrandIcon name="video" decorative :size="13" />
        片源准备
      </RouterLink>
      。
    </p>
  </section>
</template>

<style scoped>
/* 卡片：与 JoinRoomCard 并排，所以只管自己的内边距与描边，宽高交给首页的栅格。 */
.card {
  display: flex;
  flex-direction: column;
  padding: 20px;
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: var(--radius);
}

h2 {
  display: flex;
  align-items: center;
  gap: 7px;
  margin: 0 0 14px;
  font-size: 16px;
  font-weight: 600;
}

.row {
  display: flex;
  gap: 10px;
  margin-top: 12px;
}

.primary {
  flex: 1;
  padding: 11px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  transition: background var(--dur-micro) var(--ease), transform var(--dur-micro) var(--ease);
}

.primary:hover:not(:disabled) {
  background: var(--accent-hover);
}

.primary:active:not(:disabled) {
  transform: scale(0.98);
}

.primary:disabled {
  opacity: 0.5;
}

.hint {
  margin: 0 0 14px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--dim);
}

.hint-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--accent);
}

.alert {
  margin: 10px 0 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}

.note {
  margin: 12px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--dim);
}
</style>
