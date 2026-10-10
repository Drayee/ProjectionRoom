<script setup lang="ts">
/**
 * 建设中页面（占位）。
 *
 * 用于"路由已注册、入口已在标题栏、但后端与数据模型还没实现"的分区：
 * 房间室-好友/官方、番剧论坛、帖子、消息。内容由路由的 `props` 传进来
 * （见 `router.ts`），因此这里只有一份渲染逻辑，不会因为六个页面各写一遍而漂移。
 *
 * 三条自我约束（避免"占位页悄悄变成假功能"）：
 *   1. **零请求**：这一页不发任何 API 调用 —— 没有后端接口可调，发出去只会得到 404；
 *      页面明说这一点，比"转圈然后失败"诚实。
 *   2. **不放假控件**：没有禁用按钮、没有假装能用的搜索框；只写"预期形态 + 依赖什么"。
 *   3. **不遮蔽既有路径**：免登录进房（房间室-公开 / 直接进入放映室）与本页并列存在，
 *      占位不等于把那扇门关上。
 */
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'

const props = withDefaults(
  defineProps<{
    /** 页面标题。 */
    title: string
    /** 图标名（`public/icons/<name>.svg`，不新增图标文件）。 */
    icon?: string
    /** 一句话说明这一页要解决什么。 */
    summary?: string
    /** 预期形态（上线后长什么样）。 */
    bullets?: string[]
    /** 依赖什么（数据模型 / 接口 / 通道）。 */
    depends?: string[]
  }>(),
  { icon: 'tips', summary: '', bullets: () => [], depends: () => [] },
)
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />

    <header class="head">
      <h1 data-testid="coming-soon-title">
        <BrandIcon :name="props.icon" decorative :size="20" />
        {{ props.title }}
      </h1>
      <p class="muted" v-if="props.summary">{{ props.summary }}</p>
      <p class="badge building">建设中</p>
    </header>

    <section class="card" data-testid="coming-soon">
      <h2>这一页现在是什么</h2>
      <p>
        路由 <code class="mono">{{ $route.path }}</code> 已注册、标题栏入口已就位，页面本身
        <strong>是占位</strong>：后端接口与数据模型随后另行实现，这里先不发任何请求、也不摆
        假控件（没有假装能用的搜索框或禁用的按钮）。
      </p>

      <template v-if="props.bullets.length > 0">
        <h2>上线后的预期形态</h2>
        <ul>
          <li v-for="item in props.bullets" :key="item">{{ item }}</li>
        </ul>
      </template>

      <template v-if="props.depends.length > 0">
        <h2>依赖</h2>
        <ul>
          <li v-for="item in props.depends" :key="item">{{ item }}</li>
        </ul>
      </template>

      <h2>现在能做的</h2>
      <ul>
        <li>
          看片：<RouterLink :to="{ name: 'public-rooms' }">公开房间</RouterLink>
          （<strong>免登录</strong>），或用房间码直接进房。
        </li>
        <li>
          建房与账号：<RouterLink :to="{ name: 'home' }">回首页</RouterLink>
          （建房需要登录，看片不需要）。
        </li>
        <li>
          规则与排障：<RouterLink :to="{ name: 'help' }">帮助</RouterLink>。
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.page {
  max-width: 980px;
  margin: 0 auto;
  padding: 36px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.head h1 {
  display: flex;
  align-items: center;
  gap: 7px;
  margin: 0 0 6px;
  font-size: 22px;
}

.head p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
}

.badge.building {
  display: inline-block;
  margin-top: 10px;
  border-color: var(--accent);
  color: var(--accent);
}

.card h2 {
  margin: 0 0 10px;
  font-size: 15px;
}

.card h2:not(:first-child) {
  margin-top: 18px;
}

.card p {
  margin: 0;
  font-size: 13px;
  line-height: 1.8;
}

.card ul {
  margin: 0;
  padding-left: 20px;
  font-size: 13px;
  line-height: 1.9;
}

@media (max-width: 460px) {
  .page {
    padding: 20px 12px 32px;
  }
}
</style>
