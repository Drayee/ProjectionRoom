<script setup lang="ts">
/**
 * 标题背后的动图槽位。
 *
 * 三种状态，**没有一种会出现破图**：
 *   1. `HERO_LOOPS` 为空（当前状态）→ 只渲染纯 CSS 渐变 + 噪点兜底层，连 `<img>` 都没有；
 *   2. 清单里有素材且真的存在 → 渐变兜底层留着（做底），素材叠在上面淡入；
 *   3. 清单里有素材但文件不在（放错目录/删了）→ `onerror` 把该条从显示列表摘掉，回到状态 1。
 *
 * 尺寸与替换方式：CSS 变量（见 styles.css）——
 *   `--hero-motion-w` / `--hero-motion-h`：槽位尺寸
 *   `--hero-motion-opacity`：动图整体不透明度
 *   `--hero-motion-blur`：渐变兜底的柔化半径
 * 素材放置与清单维护见 `src/heroAssets.ts` 与 `client/public/hero/README.md`。
 */
import { computed, ref } from 'vue'
import { HERO_LOOPS, heroLoopUrl } from '../heroAssets'

/** 加载失败过的条目：用 src 记账，失败即下榜（不会留下一个破图占位）。 */
const failed = ref<Set<string>>(new Set())

const loops = computed(() => HERO_LOOPS.filter((loop) => !failed.value.has(loop.src)))

function onLoadFailed(src: string): void {
  const next = new Set(failed.value)
  next.add(src)
  failed.value = next
}
</script>

<template>
  <div class="hero-motion" data-testid="hero-motion-slot">
    <!-- 兜底层：纯 CSS（径向渐变 + 点阵噪点），任何情况下都在，不依赖任何素材。 -->
    <span class="wash" aria-hidden="true" />

    <template v-for="loop in loops" :key="loop.src">
      <img
        v-if="loop.kind === 'image'"
        class="loop"
        :src="heroLoopUrl(loop)"
        :alt="loop.alt ?? ''"
        :aria-hidden="loop.alt ? undefined : 'true'"
        decoding="async"
        @error="onLoadFailed(loop.src)"
      />
      <video
        v-else
        class="loop"
        :src="heroLoopUrl(loop)"
        autoplay
        loop
        muted
        playsinline
        :aria-hidden="loop.alt ? undefined : 'true'"
        @error="onLoadFailed(loop.src)"
      />
    </template>
  </div>
</template>

<style scoped>
.hero-motion {
  position: absolute;
  /* 铺满标题块（由 HomeView 的 .hero-head 作为定位上下文）。 */
  inset: calc(-1 * var(--hero-motion-bleed, 44px));
  z-index: 0;
  pointer-events: none;
  display: grid;
  place-items: center;
  overflow: hidden;
}

/* 兜底层：三个径向渐变（主题色）+ 点阵噪点，取"月夜"的冷蓝与一点粉。
   它不是占位灰块 —— 没有素材时这就是最终观感。 */
.wash {
  position: absolute;
  width: var(--hero-motion-w, min(560px, 86vw));
  height: var(--hero-motion-h, min(440px, 58vh));
  border-radius: 50%;
  background:
    radial-gradient(46% 42% at 32% 32%, rgba(0, 161, 214, 0.55), transparent 70%),
    radial-gradient(40% 38% at 70% 64%, rgba(251, 114, 153, 0.4), transparent 72%),
    radial-gradient(58% 55% at 50% 50%, rgba(122, 140, 255, 0.26), transparent 74%);
  filter: blur(var(--hero-motion-blur, 34px));
  opacity: var(--hero-motion-opacity, 0.55);
  mask-image: radial-gradient(closest-side, #000 60%, transparent 100%);
  -webkit-mask-image: radial-gradient(closest-side, #000 60%, transparent 100%);
  animation: hero-breathe 7s ease-in-out infinite alternate;
}

/* 噪点：纯 CSS 点阵（不引外链图），给渐变加一点颗粒，避免"塑料感"。 */
.wash::after {
  content: '';
  position: absolute;
  inset: 0;
  border-radius: 50%;
  background-image: repeating-conic-gradient(
    from 0deg at 50% 50%,
    rgba(255, 255, 255, 0.07) 0deg 1deg,
    transparent 1deg 2deg
  );
  mix-blend-mode: overlay;
  opacity: 0.45;
}

.loop {
  position: relative;
  z-index: 1;
  max-width: var(--hero-motion-w, min(560px, 86vw));
  max-height: var(--hero-motion-h, min(440px, 58vh));
  object-fit: contain;
  opacity: var(--hero-motion-opacity, 0.55);
}

@keyframes hero-breathe {
  from {
    transform: scale(0.96) translateY(6px);
  }
  to {
    transform: scale(1.04) translateY(-6px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .wash {
    animation: none;
  }
}
</style>
