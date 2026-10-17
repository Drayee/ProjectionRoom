<script setup lang="ts">
/**
 * 能力降级空态：某分区后端尚未上线时，页面上唯一该出现的东西。
 *
 * 为什么需要一个共享组件而不是各页各写一句 `<p class="dim">`：
 *   - 同一件事（`CapabilityUnavailable`）在四个分区都会发生，文案与结构必须一致，
 *     否则用户会以为是四个不同的问题；
 *   - 「空态」与「错误」必须在**视觉上**分开：这里用中性描边 + 说明 + 可选的重试，
 *     不用 `--danger`，因为这不是失败，是**该功能还没上线**；
 *   - 它同时是给开发者看的：`domain` 会显示出来，配合 `api/mode.ts` 的
 *     `mockSwitchSnapshot()`，一眼能判断"我是不是把开关翻成了真实实现却没后端"。
 *
 * 归 ui/：纯展示，不发请求、不读 store。
 */
import BrandIcon from './BrandIcon.vue'

withDefaults(
  defineProps<{
    /** 分区名，如「帖子」。用于标题。 */
    title?: string
    /** 域标识（`ApiDomain`），显示在脚注里帮助排查开关配置。 */
    domain?: string
    /** 补充说明：这个分区上线后会有什么。给用户一个"以后会好用"的预期。 */
    hint?: string
    /** 是否显示重试（父级给了 `retry` 监听时才真的有用）。 */
    retryable?: boolean
  }>(),
  { title: '该分区后端尚未上线', domain: '', hint: '', retryable: false },
)

defineEmits<{ retry: [] }>()
</script>

<template>
  <section class="notice" role="status">
    <h2 class="head">
      <BrandIcon name="alert-triangle" decorative :size="16" />
      {{ title }}
    </h2>
    <p class="body">
      前端界面已就绪，服务端接口还没上线。这不是网络故障，稍后回来就好。
    </p>
    <p v-if="hint" class="body dim">{{ hint }}</p>
    <div class="foot">
      <span v-if="domain" class="mono dim">域：{{ domain }}</span>
      <button v-if="retryable" type="button" class="retry" @click="$emit('retry')">重试</button>
    </div>
  </section>
</template>

<style scoped>
.notice {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.head {
  margin: 0;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
  /* 中性描边 + 常规字色：状态不靠颜色单独表达（tokens.css 硬约束 2） */
  color: var(--text);
}

.body {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
}

.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 2px;
}

.mono {
  font-family: var(--font-mono);
  font-size: 11px;
}

.retry {
  padding: 6px 14px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 13px;
}

.retry:hover {
  background: rgba(180, 108, 255, 0.1);
}
</style>
