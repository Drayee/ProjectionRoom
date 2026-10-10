<script setup lang="ts">
/**
 * `/verify` 页 —— 邮件链接里的一次性 token 完成邮箱验证。
 *
 * 链接形态：`<域名>/verify#token=<一次性串>`（token 在 fragment，理由见
 * `utils/fragmentToken.ts`：fragment 不进 nginx 访问日志；`createWebHistory()` 不消费它，
 * 所以这里挂载时自己解析，并在取到的那一刻立刻把 hash 抹掉）。
 *
 * **自动提交 vs 按钮：这里选自动提交**（与 `/reset` 同一套理由，但更强）：
 *   - 验证邮箱**没有任何参数需要用户填**，链接到手就是"可以执行了"；
 *   - 它是一次**只读/幂等**的状态翻转（服务端对已用的 token 回同一个 `VERIFY_TOKEN_INVALID`，
 *     不会产生"每点一次就多消耗一次凭据"的危险），不存在"误触造成不可逆后果"；
 *   - 代价与收益对比：让用户多点一次按钮，只是把"点链接"变成"点链接 + 点按钮"，
 *     没有任何控制权的增加。所以这里自动提交，同时在失败后给一个**显式**的
 *     「重新验证」按钮（网络不可达等瞬时失败值得重试，而且重试必须是用户看得见的动作）。
 *
 * 与 `/reset` 的关键差别：验证**不影响本地登录态**（它可能是在另一台设备、另一个浏览器
 * 里点的），所以这里**不做 `clearSession()`** —— 顺手把别人的登录态清掉是错的。
 *
 * 失败分诊：`400 VERIFY_TOKEN_INVALID`（非法/过期/已用同码）→ 说清并给**重新发送**入口
 * （忘记密码页签就是重发路径），给回登录页的出口；其余（429/503/网络）按瞬时失败处理，
 * 提供「重新验证」按钮原样重试。
 *
 * token 纪律与 `/reset` 完全一致：只从 fragment 读、立刻抹 hash、只进请求体 ——
 * 不进 URL、不进存储、不进 console。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import { toAuthError, verifyEmail } from '../api/auth'
import { parseHashChangeToken, stripFragment, takeFragmentToken } from '../utils/fragmentToken'

type Phase = 'idle' | 'busy' | 'success' | 'failed'

const phase = ref<Phase>('idle')
/** 失败说明（token 失效 / 瞬时失败各一句，恢复路径不同）。 */
const failureText = ref('')
/**
 * token 是否已经"被判死"。
 *
 * true = 服务端明确说了 `VERIFY_TOKEN_INVALID`：重试没有意义，恢复路径是**重新申请**；
 * false = 瞬时失败（网络/限速/服务端不可用）：重试有意义，给「重新验证」按钮。
 */
const tokenDead = ref(false)

/**
 * 从 fragment 取到的一次性 token（只在这个组件的闭包里存活）。
 *
 * **必须是 `ref`**：`hasToken` 是 `computed`，依赖集合里要有响应式源；否则"渲染之后
 * 才拿到 token"时会把取值为 false 的结果缓存住，界面一直停在"链接不完整"上。
 */
const token = ref('')

const hasToken = computed(() => token.value !== '')

/** 重新发送入口：登录面板的「忘记密码」页签既可重发验证邮件，也是找回密码的同一入口。 */
const RESEND_LINK = { name: 'login', query: { tab: 'forgot' } }

async function verify() {
  if (phase.value === 'busy' || phase.value === 'success' || !hasToken.value) return
  phase.value = 'busy'
  failureText.value = ''
  tokenDead.value = false
  try {
    // token 只出现在请求体里（api 层负责 JSON.stringify）。
    await verifyEmail({ token: token.value })
    phase.value = 'success'
  } catch (err) {
    const e = toAuthError(err)
    phase.value = 'failed'
    if (e.status === 400 && e.code === 'VERIFY_TOKEN_INVALID') {
      tokenDead.value = true
      failureText.value = '这个验证链接已经失效（可能过期、已经用过，或者链接被截断）。请重新发送一封验证邮件。'
      return
    }
    if (e.status === 429) {
      failureText.value = '请求太频繁了（服务端有限速窗口）：请等几分钟再点一次「重新验证」。'
      return
    }
    if (e.status === 503) {
      failureText.value = '服务端的账号 / 邮件能力暂时不可用（这是服务端的部署状态，不是链接的问题）。请稍后再试。'
      return
    }
    // 网络不可达等：`toAuthError` 已经给了本地兜底文案，原样展示。
    failureText.value = e.message
  }
}

/**
 * 把"拿到一枚 token"这件事落到状态上。返回 true = 这次拿到的与当前这枚不同。
 *
 * 换了令牌就是一次新的输入：清掉上一次的失败结论，避免旧提示挂在新链接上。
 */
function adoptToken(next: string): boolean {
  if (next === token.value) return false
  token.value = next
  failureText.value = ''
  tokenDead.value = false
  if (phase.value === 'failed' || phase.value === 'success') phase.value = 'idle'
  return true
}

/**
 * `hashchange`：**从事件的 `newURL` 里解析**，而不是读 `location.hash`。
 *
 * 实测踩到的坑：SPA 内导航到 `#token=…` 时，事件触发那一刻 `location.hash` 可能已被
 * URL 归一化/清理动过（读到空串），页面于是停在"链接不完整"上，看起来像链接坏了 ——
 * 而用户明明点的是邮件里那封带 token 的链接。事件里的 `newURL` 才是权威输入。
 *
 * 抹 hash 走 `history.replaceState`，它**不触发** `hashchange`，所以这个监听不会自激。
 */
function onHashChange(event: Event) {
  const next = parseHashChangeToken(event)
  if (next !== '' || window.location.hash !== '') {
    stripFragment(window.location, window.history)
  }
  if (next !== '' && adoptToken(next)) void verify()
}

/**
 * 挂载时：认领模块求值时取到的 token（取到了就**自动提交一次**），并挂上 hash 监听
 * （覆盖"已经在这个页面上、只有 hash 变了"的情形）。
 *
 * 为什么自动而不是只放按钮：验证邮箱**没有任何参数需要用户填**，链接到手就是"可以执行了"；
 * 它是一次幂等的状态翻转（已用的 token 服务端回同一个 `VERIFY_TOKEN_INVALID`），
 * 不存在"误触造成不可逆后果"。让用户多点一次按钮只是把"点链接"变成"点链接 + 点按钮"，
 * 没有增加任何控制权 —— 所以自动提交，失败后再给一个**显式**的「重新验证」按钮
 * （瞬时失败值得重试，而重试必须是用户看得见的动作）。
 */
onMounted(() => {
  window.addEventListener('hashchange', onHashChange)
  // 每次挂载都重新读：`/verify#token=A` 与 `/verify#token=B` 会复用同一个组件实例，
  // 只在模块求值时读一次的话，第二次导航会拿到上一枚令牌（或什么都没拿到）。
  if (adoptToken(takeFragmentToken(window.location, window.history))) void verify()
})

/** 离开时把内存里那枚 token 抹掉、摘掉监听（它不是安全边界，只是不再需要了）。 */
onBeforeUnmount(() => {
  window.removeEventListener('hashchange', onHashChange)
  token.value = ''
})
</script>

<template>
  <div class="page" data-testid="verify-page">
    <AppHeader variant="page" />

    <!-- 链接里没有 token：正常输入（用户直接打开了 /verify），如实说明并给重发入口。 -->
    <section class="card" v-if="!hasToken" data-testid="verify-no-token">
      <h1>
        <BrandIcon name="alert-triangle" label="链接不完整" :size="20" />
        这个链接不完整
      </h1>
      <p class="muted">
        验证链接里的令牌没被带上（可能是被邮件客户端截断、或者你直接打开了这个地址）。
        请去<strong>忘记密码</strong>重新发送一封验证邮件。
      </p>
      <p class="actions">
        <RouterLink class="btn" :to="RESEND_LINK" data-testid="verify-resend">
          <BrandIcon name="refresh" decorative :size="15" />
          重新发送验证邮件
        </RouterLink>
        <RouterLink class="btn" :to="{ name: 'login' }" data-testid="verify-to-login">
          <BrandIcon name="login" decorative :size="15" />
          回登录页
        </RouterLink>
      </p>
    </section>

    <section class="card" v-else-if="phase === 'success'" data-testid="verify-success">
      <h1 class="ok-head">
        <BrandIcon name="check-circle" label="成功" :size="20" />
        邮箱已验证
      </h1>
      <p class="muted">邮箱已验证，现在可以用它找回密码。</p>
      <p class="actions">
        <RouterLink class="btn" :to="{ name: 'login', query: { tab: 'forgot' } }" data-testid="verify-to-forgot">
          <BrandIcon name="lock" decorative :size="15" />
          用邮箱找回密码
        </RouterLink>
        <RouterLink class="btn" :to="{ name: 'home' }" data-testid="verify-to-home">
          <BrandIcon name="login" decorative :size="15" />
          回首页
        </RouterLink>
      </p>
    </section>

    <section class="card" v-else-if="phase === 'failed'" data-testid="verify-failed">
      <h1>
        <BrandIcon name="alert-triangle" label="验证失败" :size="20" />
        {{ tokenDead ? '这个验证链接已经失效' : '验证没能完成' }}
      </h1>
      <p class="muted" :data-testid="tokenDead ? 'verify-token-invalid' : 'verify-error'">{{ failureText }}</p>
      <p class="actions">
        <!-- 失效 → 恢复路径是重新发送；瞬时失败 → 恢复路径是同一枚 token 再试一次。 -->
        <RouterLink v-if="tokenDead" class="btn" :to="RESEND_LINK" data-testid="verify-resend">
          <BrandIcon name="refresh" decorative :size="15" />
          重新发送验证邮件
        </RouterLink>
        <button v-else type="button" class="btn" data-testid="verify-retry" @click="verify()">
          <BrandIcon name="refresh" decorative :size="15" />
          重新验证
        </button>
        <RouterLink class="btn" :to="{ name: 'login' }" data-testid="verify-to-login">
          <BrandIcon name="login" decorative :size="15" />
          回登录页
        </RouterLink>
      </p>
    </section>

    <section class="card" v-else aria-busy="true" data-testid="verify-busy">
      <h1>
        <BrandIcon name="refresh" label="进行中" :size="20" class="spinning" />
        正在验证邮箱…
      </h1>
      <p class="muted">正在向服务端提交这枚一次性令牌，请稍候（通常不到一秒）。</p>
    </section>
  </div>
</template>

<style scoped>
.page {
  max-width: 560px;
  margin: 0 auto;
  padding: 28px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.card {
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--panel);
}

.card h1 {
  margin: 0 0 10px;
  font-size: 20px;
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 成功标题用成功色（与错误/中性区分开，不依赖图标单独承担语义）。 */
.ok-head {
  color: var(--ok);
}

.card p {
  margin: 0 0 12px;
  font-size: 13px;
  line-height: 1.8;
}

.actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 16px 0 0;
}

button.btn,
a.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--panel-2);
  color: var(--text);
  font-size: 13px;
  text-decoration: none;
}

button.btn:hover,
a.btn:hover {
  border-color: var(--accent);
  text-decoration: none;
}

.spinning {
  animation: verify-spin 0.9s linear infinite;
}

@keyframes verify-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 460px) {
  .page {
    padding: 18px 12px 32px;
  }
}
</style>
