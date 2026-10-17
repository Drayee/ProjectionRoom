<script setup lang="ts">
/**
 * 帮助页（/help）。硬契约（票 #16）：7 个锚点 id 全保留 —— #why-login #source
 * #room-code #playback #diagnostics #privacy #faq；scrollBehavior 消费 to.hash。
 * 内容按 B 视觉重写（不平移旧文案）。
 */
import BrandIcon from '../components/ui/BrandIcon.vue'

interface Section {
  id: string
  title: string
  paragraphs: string[]
}

const SECTIONS: Section[] = [
  {
    id: 'why-login',
    title: '为什么建房要登录，看片不用？',
    paragraphs: [
      '看片（观众）只需要房间码，免登录就能进房——这是产品刻意保留的路径：进房门槛越低，一起看越容易。',
      '建房（主播）必须登录：建房成功后服务端会发一枚一次性 hostToken，主播断线宽限期内要靠它接回主播位。账号只用于建房与社交，看片本身不需要账号。',
    ],
  },
  {
    id: 'source',
    title: '片源准备（本地切片）',
    paragraphs: [
      '月喵只同步「本地」视频：主播先把视频切成 fMP4 分片目录（可用项目提供的本地切片器），再在主播控制台点「选择本地分片目录」选中它，即可开播。',
      '视频字节只在你和观众之间走 WebRTC DataChannel，服务端不中转任何视频数据。',
    ],
  },
  {
    id: 'room-code',
    title: '房间码与分享',
    paragraphs: [
      '房间码是 4–12 位大写字母与数字（如 MOON04）。把房间码发给别人，对方在落地页「直接进放映室」里输入即可加入。',
      '你可以在主播控制台把房间设为「公开」，这样它会出现在「公开的房间」列表里，不靠房间码也能被发现。',
    ],
  },
  {
    id: 'playback',
    title: '同步播放与拓扑分发',
    paragraphs: [
      '主播播放本地视频，观众通过 DataChannel 同步接收分片，自动缓冲并跟随主播进度；暂停、跳转、倍速都会同步。',
      '观众之间还会按拓扑自动分工、彼此转发（卡顿换父、健康度监测），人越多越稳。',
    ],
  },
  {
    id: 'diagnostics',
    title: '卡顿与排障',
    paragraphs: [
      '同步里最常见的两类问题：上行不足（主播被实测码率限制）与拓扑抖动（观众之间换父）。系统会按实测上行给房间上限，并在父节点不健康时自动切换。',
      '本轮前端重写暂不提供诊断抽屉入口；遇到持续卡顿，先检查主播的上行带宽，再尝试让观众重新进房。',
    ],
  },
  {
    id: 'privacy',
    title: '隐私与安全边界',
    paragraphs: [
      '视频不经服务器中转；房间实时状态只存在内存里，数据库只保存房间元数据与周期快照。',
      '「你能不能进房、能不能建房」这些授权判断 100% 在服务端，前端只是按结果决定显示什么。',
    ],
  },
  {
    id: 'faq',
    title: '常见问题（重连 / 宽限期）',
    paragraphs: [
      '主播突然断线？房间会在宽限期（默认 60 秒）内保留，期间永不回收；用同一个房间码重新进房，并带上建房时拿到的 hostToken，就能接回主播位。',
      '观众掉线：直接用同一个房间码重新进房即可，进度会自动跟上。',
    ],
  },
]
</script>

<template>
  <div class="page">
    <main class="body">
      <h1 class="title">帮助</h1>
      <nav class="toc" aria-label="目录">
        <a v-for="s in SECTIONS" :key="s.id" :href="`#${s.id}`">{{ s.title }}</a>
      </nav>
      <section v-for="s in SECTIONS" :id="s.id" :key="s.id" class="sec">
        <h2>{{ s.title }}</h2>
        <p v-for="(p, i) in s.paragraphs" :key="i">{{ p }}</p>
      </section>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(760px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.toc { display: flex; flex-direction: column; gap: 6px; border: 1px solid var(--line); padding: 12px 16px; }
.toc a { color: var(--dim); font-size: 13px; }
.toc a:hover { color: var(--text); }
.sec { scroll-margin-top: 16px; }
.sec h2 { margin: 0 0 6px; font-family: var(--font-glitch); font-size: 19px; letter-spacing: 0.04em; }
.sec p { margin: 0 0 8px; color: var(--text); line-height: 1.8; }
.sec p:last-child { margin-bottom: 0; }
</style>
