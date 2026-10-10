<script setup lang="ts">
/**
 * 首页标题下方的圆形加号（FAB）：展开三个圆形动作钮。
 *
 *   ① 新建房间  → 交给调用方（走**既有**建房逻辑：`CreateRoomCard` 的 createRoom）
 *   ② 发送帖子  → 调用方给中性的"能力待接入"提示（后端接口与数据模型尚未实现）
 *   ③ 添加番剧  → 同上
 *
 * 设计取舍：
 *   - **加号是 CSS 画的**（两条 2px 圆角条），不是图标文件 —— `public/icons/` 是既有
 *     验收契约的一部分（`dist/icons` 必须正好 22 个），为了一个加号新增 SVG 得不偿失；
 *     CSS 画还顺带把"展开时旋转成 ×"做成一次过渡。
 *   - 三个动作钮**只有图标**（用户明确要求），因此每个都有 `aria-label` + `v-tip`：
 *     可访问名走前者的（tooltip 不能是唯一信息源）。
 *   - 展开/收起用 `aria-expanded` + 容器上的 CSS 过渡驱动（容器**常驻 DOM**、收起时 `visibility: hidden`
 *     + 子按钮 `tabindex="-1"`，动画才能收放自如），并支持 Esc 与点击外部收起 —— 键盘与鼠标
 *     两条路都能关掉它，不会出现"展开后只能点同一个按钮"的死角。
 */
import { onBeforeUnmount, ref, watch } from 'vue'
import BrandIcon from './BrandIcon.vue'

const props = withDefaults(defineProps<{ disabled?: boolean }>(), { disabled: false })
const emit = defineEmits<{ (e: 'create' | 'post' | 'anime'): void }>()

const expanded = ref(false)
const root = ref<HTMLElement | null>(null)

/** 三个动作钮：只有图标 + 可访问名 + 提示。 */
const ACTIONS = [
  { key: 'create' as const, icon: 'video', label: '新建房间' },
  { key: 'post' as const, icon: 'chat', label: '发送帖子' },
  { key: 'anime' as const, icon: 'star', label: '添加番剧' },
]

function toggle() {
  if (props.disabled) return
  expanded.value = !expanded.value
}

function run(key: 'create' | 'post' | 'anime') {
  // ① 直接去做（并把面板收起，因为接下来要看建房卡片本身）；
  // ②③ 的入口留在面板里 —— 不收起，用户马上能看到"能力待接入"的说明。
  if (key === 'create') expanded.value = false
  emit(key)
}

function onDocumentPointerDown(event: MouseEvent) {
  if (!expanded.value) return
  const el = root.value
  if (el && !el.contains(event.target as Node)) expanded.value = false
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && expanded.value) expanded.value = false
}

watch(expanded, (open) => {
  if (open) {
    document.addEventListener('pointerdown', onDocumentPointerDown, true)
    document.addEventListener('keydown', onKeydown)
  } else {
    document.removeEventListener('pointerdown', onDocumentPointerDown, true)
    document.removeEventListener('keydown', onKeydown)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="fab" :class="{ expanded }" ref="root" data-testid="home-fab">
    <button
      type="button"
      class="fab-toggle"
      :disabled="props.disabled"
      :aria-expanded="expanded"
      aria-controls="home-fab-actions"
      :aria-label="expanded ? '收起快捷操作' : '展开快捷操作（新建房间 / 发送帖子 / 添加番剧）'"
      v-tip="expanded ? '收起快捷操作' : '新建房间 / 发送帖子 / 添加番剧'"
      data-testid="home-fab-toggle"
      @click="toggle"
    >
      <!-- 加号由两条 CSS 条组成（展开时上条不动、下条转 90° 成 ×）。 -->
      <span class="plus" aria-hidden="true" />
    </button>

    <!-- v-show 而不是 v-if：展开动画需要元素一直在，且收起时用 visibility 把它移出可聚焦序列。 -->
    <div id="home-fab-actions" class="fab-actions" data-testid="home-fab-actions" :aria-hidden="!expanded">
      <button
        v-for="(action, index) in ACTIONS"
        :key="action.key"
        type="button"
        class="fab-action"
        :style="{ transitionDelay: expanded ? `${index * 45}ms` : '0ms' }"
        :tabindex="expanded ? undefined : -1"
        :aria-label="action.label"
        v-tip="action.label"
        :data-testid="`home-fab-${action.key}`"
        @click="run(action.key)"
      >
        <BrandIcon :name="action.icon" decorative :size="18" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.fab {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 10px;
}

.fab-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 46px;
  height: 46px;
  padding: 0;
  border-radius: 50%;
  background: var(--accent);
  border: 1px solid var(--accent);
  color: #04141b;
}

.fab-toggle:hover:not(:disabled) {
  border-color: var(--accent);
  filter: brightness(1.08);
}

.plus {
  position: relative;
  display: inline-block;
  width: 18px;
  height: 18px;
}

.plus::before,
.plus::after {
  content: '';
  position: absolute;
  left: 50%;
  top: 50%;
  width: 18px;
  height: 2px;
  border-radius: 2px;
  background: currentColor;
  transform: translate(-50%, -50%);
  transition: transform 240ms ease;
}

.plus::after {
  transform: translate(-50%, -50%) rotate(90deg);
}

/* 展开后：横条转 45°、竖条转 -45° → 合成一个 ×。 */
.fab.expanded .plus::before {
  transform: translate(-50%, -50%) rotate(45deg);
}

.fab.expanded .plus::after {
  transform: translate(-50%, -50%) rotate(-45deg);
}

.fab-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  transition:
    opacity 200ms ease,
    transform 200ms ease,
    visibility 200ms ease;
  opacity: 0;
  transform: translateY(-8px) scale(0.94);
  visibility: hidden;
  pointer-events: none;
}

.fab.expanded .fab-actions {
  opacity: 1;
  transform: none;
  visibility: visible;
  pointer-events: auto;
}

/* 三个圆形动作钮：只有图标（可访问名 + 提示都挂在按钮上）。 */
.fab-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  padding: 0;
  border-radius: 50%;
  background: var(--panel-2);
  border: 1px solid var(--border);
  color: var(--text);
}

.fab-action:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}

@media (prefers-reduced-motion: reduce) {
  .fab-actions,
  .plus::before,
  .plus::after {
    transition: none;
  }
}
</style>
