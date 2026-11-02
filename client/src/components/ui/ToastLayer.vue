<script setup lang="ts">
/**
 * 轻提示图层（票 #67；决议 #53 §5）。
 *
 * ## 它挂在路由出口之外
 *
 * 由 `App.vue` 渲染（与 `TooltipLayer` 并排）。原因：用户点了一条
 * "有人关注了你"，正在跳去粉丝 tab —— 提示不该在跳转的一瞬间消失。
 *
 * ## 三条无障碍约定
 *
 *  1. **`role="status"` + `aria-live="polite"`**：读屏会念，但**不打断**
 *     当前正在读的内容。用 `alert` 会打断 —— 而"有人关注了你"不是紧急事件。
 *  2. **不抢焦点**：提示出现时绝不 `focus()`。抢焦点会让键盘用户正在填的表单失焦，
 *     而这条提示本身是"顺便告诉你一声"。
 *  3. **reduced-motion 下静态出现**：位移/淡入全部取消，只留定时消失
 *     （决议 §5 原文）。「不动画」不等于「不消失」—— 后者会让它永久停在屏幕上。
 *
 * ## 为什么是可点的一条
 *
 * 点击跳 `/friends?tab=followers`（决议 §5）。整条是 `<RouterLink>` 而不是
 * 「文字 + 旁边一个按钮」：一条 4 秒就消失的提示里，让用户去瞄准一个小按钮
 * 是不现实的 —— 整条可点，命中区就是整条。
 */
import { useNoticesStore } from '../../stores/notices'

const notices = useNoticesStore()
</script>

<template>
  <!--
    容器 `aria-live="polite"` 而不是每条各带一个：多个 live region 会各念各的。
    容器本身不占位（`pointer-events: none`），只有里面的每一条可点。
  -->
  <div class="toasts" role="status" aria-live="polite" aria-atomic="false">
    <div v-for="t in notices.toasts" :key="t.id" class="toast-wrap">
      <RouterLink
        v-if="t.to"
        class="toast toast--link"
        :to="t.to"
        data-testid="follow-toast"
        @click="notices.dismiss(t.id)"
      >
        <span class="toast-text">{{ t.text }}</span>
        <span class="toast-go" aria-hidden="true">去看看</span>
      </RouterLink>
      <!-- 没有落点时不渲染链接：一个 href 为空的 <a> 既不可点又会被读屏当成链接。 -->
      <div v-else class="toast" data-testid="follow-toast">
        <span class="toast-text">{{ t.text }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/*
 * 固定在底部居中（决议 §5 的"底部"）。
 *
 * `pointer-events: none` 在外层、`auto` 在每一条：否则这个全宽的容器会挡住
 * 页面底部那一块的点击（症状是"底部按钮点不动"，而且很难联想到提示层）。
 */
.toasts {
  position: fixed;
  left: 50%;
  bottom: 24px;
  z-index: 60;
  transform: translateX(-50%);
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: center;
  pointer-events: none;
  width: max-content;
  max-width: min(92vw, 420px);
}

.toast-wrap {
  pointer-events: auto;
  width: 100%;
}

.toast {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--line-strong);
  box-shadow: var(--elev-2);
  color: var(--text);
  font-size: 13px;
  text-decoration: none;
  animation: toast-in var(--dur-comp) var(--ease);
}

.toast--link:hover {
  border-color: var(--accent);
  text-decoration: none;
}

.toast--link:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

.toast-text {
  min-width: 0;
  flex: 1;
}

.toast-go {
  flex: none;
  color: var(--accent);
  font-size: 12px;
}

@keyframes toast-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/*
 * reduced-motion：**静态出现**，但定时消失照旧（决议 §5）。
 *
 * 只取消动画，不改 `display` —— 取消掉整个提示等于"这条通知不存在"，
 * 而用户明确关掉了动效、不是关掉了通知。
 */
@media (prefers-reduced-motion: reduce) {
  .toast {
    animation: none;
  }
}
</style>
