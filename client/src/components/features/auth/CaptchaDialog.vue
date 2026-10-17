<script setup lang="ts">
/**
 * 人机验证**子窗体**（页内模态覆盖层，不是浏览器弹窗）。
 *
 * 为什么是页内子窗体而不是 `window.open`（用户明确要求，且这是对的）：
 *   · 弹窗会被拦截器挡掉，被挡住时用户只能看到"请允许弹窗"——一次白跑；
 *   · 页内子窗体不打断注册表单的状态（邮箱、已填内容都还在原处）；
 *   · 键盘/读屏的可达性由我们自己控制（focus 移入、Esc 关闭、aria-modal），
 *     而浏览器弹窗的行为各浏览器不一致。
 *
 * 可达性三条（不是可选项）：
 *   1. `role="dialog"` + `aria-modal="true"` + `aria-labelledby`：读屏知道这是一层子窗体；
 *   2. 打开时把焦点移到第一颗可点格子上，关闭时还给触发它的元素；
 *   3. **Esc 关闭**、点遮罩关闭、右上角关闭按钮——三条退出路径都通（不能把人困住）。
 */
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import CaptchaChallenge from './CaptchaChallenge.vue'

const emit = defineEmits<{ (e: 'pass'): void; (e: 'cancel'): void }>()

const dialogRef = ref<HTMLElement | null>(null)
/** 打开前的焦点元素（关闭时还给它）。 */
let previousFocus: HTMLElement | null = null

function onPass(): void {
  emit('pass')
}

function cancel(): void {
  emit('cancel')
}

/** 键盘：Esc 关闭；Tab 在子窗体内循环（焦点不跑到背后的表单上）。 */
function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    e.preventDefault()
    cancel()
    return
  }
  if (e.key !== 'Tab') return

  const root = dialogRef.value
  if (!root) return
  const focusables = root.querySelectorAll<HTMLElement>(
    'button:not(:disabled), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
  )
  if (focusables.length === 0) return
  const first = focusables[0]
  const last = focusables[focusables.length - 1]
  const active = document.activeElement as HTMLElement | null

  if (e.shiftKey && (active === first || !root.contains(active))) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && active === last) {
    e.preventDefault()
    first.focus()
  }
}

onMounted(async () => {
  previousFocus = document.activeElement as HTMLElement | null
  document.addEventListener('keydown', onKeydown)
  await nextTick()
  // 焦点移入子窗体：否则键盘用户仍停在背后的表单上。
  const root = dialogRef.value
  root?.querySelector<HTMLElement>('.grid .cell:not(:disabled)')?.focus()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
  // 焦点还给触发者（可达性的基本要求：关闭后不该把人丢回文档开头）。
  previousFocus?.focus?.()
})
</script>

<template>
  <!-- 遮罩：点击关闭（"我放弃验证"的第三条出口） -->
  <div class="overlay" @click.self="cancel">
    <div
      ref="dialogRef"
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="captcha-title"
      data-testid="captcha-dialog"
    >
      <header class="head">
        <h2 id="captcha-title" class="title">人机验证</h2>
        <button class="close" type="button" aria-label="关闭验证" data-testid="captcha-cancel" @click="cancel">
          ✕
        </button>
      </header>

      <p class="intro">为了防止脚本批量注册，先完成这一步，通过后会自动发送验证码。</p>

      <CaptchaChallenge @pass="onPass" />

      <div class="foot">
        <button class="ghost" type="button" data-testid="captcha-cancel-foot" @click="cancel">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: rgba(6, 4, 10, 0.72);
  /* 轻微模糊：把背后的表单"压下去"，视觉上明确这是另一层 */
  backdrop-filter: blur(2px);
  /* 入场：遮罩淡入。用 animation 而不是 Vue Transition —— 后者移除时要等
     transitionend + 双 rAF，而后台/无头标签页会把 rAF 节流到十几秒，
     遮罩会卡在 DOM 里继续挡住页面（真机验证时踩过）。 */
  animation: overlay-in var(--dur-comp) var(--ease);
}

@keyframes overlay-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

.dialog {
  width: min(420px, 100%);
  max-height: calc(100vh - 40px);
  overflow-y: auto;
  border: 1px solid var(--line-strong);
  background: var(--bg-mid);
  padding: 16px 18px 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  /* 子窗体不是"缩小的页面"：它有自己的边框与直角语言 */
  box-shadow: 0 18px 48px rgba(0, 0, 0, 0.6);
  /* 入场：上浮 + 轻微放大（只动 transform / opacity） */
  animation: dialog-in var(--dur-comp) var(--ease);
}

@keyframes dialog-in {
  from {
    transform: translateY(14px) scale(0.96);
    opacity: 0;
  }
  to {
    transform: none;
    opacity: 1;
  }
}

.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: 18px;
}

.close {
  width: 28px;
  height: 28px;
  flex: none;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}

.close:hover {
  color: var(--text);
  border-color: var(--accent);
}

.intro {
  margin: 0;
  font-size: 12px;
  color: var(--dim);
  line-height: 1.6;
}

.foot {
  display: flex;
  justify-content: flex-end;
}

.ghost {
  padding: 7px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}

.ghost:hover {
  color: var(--text);
  border-color: var(--accent);
}

/* 窄屏：子窗体贴底铺满，避免在 375px 上出现双层滚动 */
@media (max-width: 520px) {
  .overlay {
    padding: 0;
    align-items: flex-end;
  }

  .dialog {
    width: 100%;
    max-height: 92vh;
    border-left: 0;
    border-right: 0;
    border-bottom: 0;
  }
}
</style>
