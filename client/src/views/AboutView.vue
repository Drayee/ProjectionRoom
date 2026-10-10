<script setup lang="ts">
/**
 * 作者介绍页（`/about`）。
 *
 * **最小占位**：本轮只把"入口"和"这一页存在"落地 —— 落地页右上角第二个图标指向这里。
 * 真实内容（作者自述、联系方式、其他作品、项目缘起）留待补充，页面上明确标注
 * 「待补充」，不编造事实。
 */
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'

const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

/** 待补充清单：写清"这一页缺什么"，比留一句"敬请期待"有用。 */
const TODO = [
  '作者自述（一两段，想做什么、为什么做这个项目）',
  '联系方式与 Issues 之外的其他作品入口',
  '项目的缘起与设计取舍（可与帮助页的"为什么"合并指向）',
  '头像/头图（本地素材，放 client/public/ 下，不引外链）',
]
</script>

<template>
  <div class="page" data-testid="about-page">
    <AppHeader variant="page" />

    <header class="head">
      <h1>
        <BrandIcon name="star" decorative :size="20" />
        关于作者
      </h1>
      <p class="muted">月喵（ProjectionRoom）· 作者 drayee · 开源</p>
    </header>

    <section class="card">
      <h2>这一页</h2>
      <p data-testid="about-pending">
        内容<strong>待补充</strong>：本轮只落地了入口与路由（<code class="mono">/about</code>），
        正文还没写。下面这份清单就是缺的部分，写完后把这一节换成真正的介绍即可。
      </p>

      <h2>待补充</h2>
      <ul data-testid="about-todo">
        <li v-for="item in TODO" :key="item">{{ item }}</li>
      </ul>

      <h2>现在就能看的东西</h2>
      <ul>
        <li>
          源码与 Issues：
          <a :href="REPO_URL" target="_blank" rel="noopener noreferrer">{{ REPO_URL }}</a>
        </li>
        <li>
          项目怎么用、房间码是什么、卡顿怎么办：
          <RouterLink :to="{ name: 'help' }">帮助与说明</RouterLink>。
        </li>
        <li>
          免登录看片：
          <RouterLink :to="{ name: 'public-rooms' }">公开房间</RouterLink>。
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.page {
  max-width: 760px;
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
  overflow-wrap: anywhere;
}

@media (max-width: 460px) {
  .page {
    padding: 20px 12px 32px;
  }
}
</style>
