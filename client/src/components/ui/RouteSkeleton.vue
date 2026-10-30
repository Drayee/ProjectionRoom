<script setup lang="ts">
/**
 * 路由切换中的骨架（票 #79）。
 *
 * 为什么需要它：页面组件是懒加载的，chunk 在路上时如果什么都不显示，
 * 用户看到的是「点了没反应」—— 本项目反复强调要消灭的症状。
 *
 * 为什么**不**新造第三套加载样式：规格要求"chunk 加载期由 AsyncState 骨架承担"
 * （票 #44 的 AsyncState 三态门面尚未落地），所以这里先做一个**形状克制**的骨架：
 * 几条灰条，视觉上与内容页的排版一致，不喧宾夺主。票 #44 落地后，
 * 本组件应改为一层 AsyncState 薄封装（而不是两套并存）。
 *
 * **只在换页时出现**（不是首屏）：首屏有 index.html 的静态壳顶着，
 * 而且首屏的骨架会让落地页的 hero 晚 200ms 才出现 —— 那比慢更糟。
 */
</script>

<template>
  <div class="skeleton" role="status" aria-live="polite">
    <span class="sr-only">正在载入页面…</span>
    <div class="bar bar-title" aria-hidden="true"></div>
    <div class="bar" aria-hidden="true"></div>
    <div class="bar bar-short" aria-hidden="true"></div>
  </div>
</template>

<style scoped>
.skeleton {
  flex: 1;
  width: min(880px, 100%);
  margin: 0 auto;
  padding: 32px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.bar {
  height: 14px;
  border: 1px solid var(--line);
  background: rgba(200, 120, 255, 0.06);
}

.bar-title {
  height: 40px;
  width: 46%;
}

.bar-short {
  width: 62%;
}
</style>
