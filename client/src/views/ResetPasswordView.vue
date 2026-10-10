<script setup lang="ts">
/**
 * `/reset` 页 —— 用邮件链接里的一次性 token 设置新密码。
 *
 * 链接形态：`<域名>/reset#token=<一次性串>`。token 在 **fragment** 里（刻意不进 nginx
 * 访问日志），而 `router.ts` 用 `createWebHistory()` —— vue-router 不消费 fragment，
 * 所以这里必须在挂载时自己解析（`utils/fragmentToken.ts`），并在**取到的那一刻**
 * 就把地址栏上的 hash 抹掉：刷新、截图、把地址栏分享给别人都不该带上一次性凭据。
 *
 * 三条与本页绑定的纪律：
 *   1. token **只进请求体**（`POST /api/auth/reset` 的 body），不进查询串、不进存储、不进 console；
 *   2. 抹掉 hash 之后 token 只活在内存里，所以"重新提交"按钮复用的是同一枚 token（不重新读 URL）；
 *   3. 成功后**清空本地凭据**并回登录页 —— 旧 access token / 档案是改密码之前的凭据，
 *      留着就等于"改了密码却还是登录态"。清理复用 `auth.clearSession()`（"少清一处就只是
 *      看起来退出了"那条教训只有一份实现），再 `replace` 到 `/login?reset=1` 让登录页显示
 *      "密码已重置，请用新密码登录"（用 `replace` 是为了让后退键不会回到这一页）。
 *
 * 失败分诊（不看文案，只看 `AuthApiError.status` / `.code`）：
 *   400 `RESET_TOKEN_INVALID` → 非法 / 过期 / 已用过（服务端三态同码，刻意不区分）：
 *      提示重新申请，并给**回登录页（忘记密码在登录面板的页签里）**的入口；
 *   400 其它 → 弱密码等，服务端文案原样展示；
 *   429 → 限速，等一下再点"重新提交"；503 → 邮件能力未配置（服务端部署事实）；
 *   status 0 → 网络不可达，`toAuthError` 已经给了本地兜底文案，原样展示。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import { reset, toAuthError } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import { parseHashChangeToken, stripFragment, takeFragmentToken } from '../utils/fragmentToken'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

/**
 * 密码策略提示：与注册页那条 (`password-policy-hint`) **同源文案**。
 *
 * 两处字符串刻意保持一致（改一处要改两处）—— 这里的检查与注册页一样只做"省一次注定
 * 失败请求"的本地拦截，**服务端才是最终判据**，它返回的弱密码文案照样原样展示。
 */
const PASSWORD_POLICY_TEXT = '密码至少 8 位，且必须同时包含字母与数字'

type Phase = 'form' | 'busy' | 'success'

const phase = ref<Phase>('form')
const form = ref({ password: '', confirm: '' })
/** 表单内的失败说明（弱密码 / 网络不可达 / 服务端其它 400）。 */
const error = ref('')
/** token 本身不可用（非法/过期/已用）——与"表单填错了"分开，恢复路径不同。 */
const tokenDead = ref(false)

/**
 * 从 fragment 取到的一次性 token（只在这个组件的闭包里存活）。
 *
 * **必须是 `ref` 而不是普通 `let`**：`hasToken` 是个 `computed`，它的依赖集合里必须有
 * 一个响应式源，否则 `token` 在渲染之后才被赋值时，取值为 false 的 `hasToken` 会被
 * 缓存住 —— 界面会一直停在"链接不完整"上（即使内存里已经有 token 了）。这类"变量不是
 * 响应式的"bug 只会在赋值晚于首渲染时出现，所以这里用类型系统把依赖关系钉住。
 */
const token = ref('')

const hasToken = computed(() => token.value !== '')
/** 提示条：既解释"这个链接为什么不能用了"，也解释"重试会不会有用"。 */
const tokenDeadText = ref('')

function passwordProblem(value: string): string {
  if (value.length < 8) return PASSWORD_POLICY_TEXT
  if (!/[A-Za-z]/.test(value)) return PASSWORD_POLICY_TEXT
  if (!/[0-9]/.test(value)) return PASSWORD_POLICY_TEXT
  return ''
}

/**
 * 提交一次重置请求。
 *
 * 任何一次提交都用**同一枚内存里的 token**：token 只在挂载（或 hash 变化）时从 fragment
 * 读一次，读到就抹掉 hash，之后再也不会回到 URL 上。因此"重新提交"不需要、也不可能
 * 从地址栏重新取 token。
 */
async function submit() {
  if (phase.value === 'busy' || phase.value === 'success') return
  error.value = ''
  tokenDead.value = false
  tokenDeadText.value = ''

  if (!hasToken.value) return

  const weak = passwordProblem(form.value.password)
  if (weak !== '') {
    error.value = weak
    return
  }
  if (form.value.confirm !== form.value.password) {
    error.value = '两次输入的密码不一致'
    return
  }

  phase.value = 'busy'
  try {
    // token 只在这次请求体里出现（api 层的 request() 会 JSON.stringify 进来）。
    await reset({ token: token.value, password: form.value.password })
    // 清空本地凭据（改密码前的 access token / 档案都不能再用），再回登录页。
    auth.clearSession()
    phase.value = 'success'
    // 让"密码已重置"真的渲染一帧再走：直接 replace 会让这一屏永远不可见，
    // 而"提交之后界面毫无变化地跳到别处"正是最容易被理解成"没成功"的那种表现。
    await nextTick()
    await router.replace({ name: 'login', query: { reset: '1' } })
  } catch (err) {
    const e = toAuthError(err)
    phase.value = 'form'
    if (e.status === 400 && e.code === 'RESET_TOKEN_INVALID') {
      tokenDead.value = true
      tokenDeadText.value = '这个重置链接已经失效（可能过期、已经用过，或者链接被截断）。请重新申请一封重置邮件。'
      return
    }
    if (e.status === 429) {
      error.value = '提交太频繁了：请等几分钟再点一次「重新提交」（令牌有限速窗口）。'
      return
    }
    if (e.status === 503) {
      error.value = '服务端的邮件 / 账号能力暂时不可用（这是服务端的部署状态，不是你的输入有问题）。请稍后再试。'
      return
    }
    // 其它（含 400 弱密码、网络不可达）：原样展示服务端/网络兜底文案，不自己编。
    error.value = e.message
  }
}

/**
 * 把"拿到一枚 token"这件事落到状态上。
 *
 * 返回 true 表示"这次拿到的与当前这枚不同"（含从"没有"变成"有"）。
 * 相同就不动状态：避免同一枚 token 被处理两遍把用户正在填的表单清掉。
 */
function adoptToken(next: string): boolean {
  if (next === token.value) return false
  token.value = next
  // 换了令牌就是一次新的输入：把上一次的失败结论清掉（用户不该看到"上一枚令牌失效"
  // 的旧提示继续挂在新链接上）。
  error.value = ''
  tokenDead.value = false
  tokenDeadText.value = ''
  return true
}

/** 挂载时读一次 token（并抹掉地址栏那一段），同时挂上 hash 监听。 */
function startTokenWatch() {
  window.addEventListener('hashchange', onHashChange)
  // 每次挂载都重新读：`/reset#token=A` 与 `/reset#token=B` 会复用同一个组件实例，
  // 只在模块求值时读一次的话，第二次导航会拿到上一枚令牌（或什么都没拿到）。
  adoptToken(takeFragmentToken(window.location, window.history))
}

/**
 * `hashchange`：**从事件的 `newURL` 里解析**，而不是读 `location.hash`。
 *
 * 与上面同一个坑的另一半：事件触发那一刻 `location.hash` 可能已经被 URL 归一化动过
 * （读到空串），而事件里的 `newURL` 是事件产生那一刻的完整地址，它才是权威输入。
 *
 * 这个监听不会与自己打架：抹 hash 走 `history.replaceState`，它**不触发** `hashchange`。
 */
function onHashChange(event: Event) {
  const next = parseHashChangeToken(event)
  // 无论解析结果如何，先把地址栏那一段抹掉（分享/截图/刷新都不该带出去）。
  if (next !== '' || window.location.hash !== '') {
    stripFragment(window.location, window.history)
  }
  if (next !== '') adoptToken(next)
}

onMounted(() => {
  startTokenWatch()
})

/**
 * 离开页面时把内存里那枚 token 抹掉、并摘掉监听。
 *
 * 它不是"安全边界"（token 一次性、且服务端已判过），只是顺手把不再需要的东西放掉：
 * 用户从这里离开之后，这枚串与那个监听都没有理由继续活着。
 */
onBeforeUnmount(() => {
  window.removeEventListener('hashchange', onHashChange)
  token.value = ''
})
</script>

<template>
  <div class="page" data-testid="reset-page">
    <AppHeader variant="page" />

    <!-- 链接里没有 token（用户直接打开 /reset、或链接被截断）：这是正常输入，如实说明。 -->
    <section class="card" v-if="!hasToken" data-testid="reset-no-token">
      <h1>
        <BrandIcon name="alert-triangle" label="链接不完整" :size="20" />
        这个链接不完整
      </h1>
      <p class="muted">
        重置链接里的令牌没被带上（可能是被邮件客户端截断、或者你直接打开了这个地址）。
        请回到<strong>忘记密码</strong>重新申请一封重置邮件。
      </p>
      <p class="actions">
        <RouterLink class="btn" :to="{ name: 'login' }" data-testid="reset-to-login">
          <BrandIcon name="login" decorative :size="15" />
          回登录页重新申请
        </RouterLink>
      </p>
    </section>

    <!-- 成功：这一屏通常一闪而过（成功后马上 replace 到 /login?reset=1），
         保留它是为了让"跳转很慢"或"被拦下"的情况下用户仍然看得到结论。 -->
    <section class="card ok" v-else-if="phase === 'success'" data-testid="reset-success">
      <h1>
        <BrandIcon name="check-circle" label="成功" :size="20" />
        密码已重置
      </h1>
      <p class="muted">本机已退出登录，正在回到登录页；请用新密码登录。</p>
      <p class="actions">
        <RouterLink class="btn" :to="{ name: 'login', query: { reset: '1' } }" data-testid="reset-to-login">
          <BrandIcon name="login" decorative :size="15" />
          去登录
        </RouterLink>
      </p>
    </section>

    <section class="card" v-else>
      <h1>
        <BrandIcon name="lock" label="重置密码" :size="20" />
        设置新密码
      </h1>
      <p class="muted">
        输入两次新密码。提交成功后本机会退出登录，请用新密码重新登录。
      </p>

      <!-- token 失效（400 RESET_TOKEN_INVALID）：非法 / 过期 / 已用同一句话，恢复路径是"重新申请"。 -->
      <div class="error-bar" v-if="tokenDead" data-testid="reset-token-invalid">
        <BrandIcon name="alert-triangle" label="链接失效" :size="15" />
        <span class="msg">{{ tokenDeadText }}</span>
      </div>
      <div class="error-bar" v-else-if="error" data-testid="reset-error">
        <BrandIcon name="alert-triangle" label="错误" :size="15" />
        <span class="msg">{{ error }}</span>
      </div>

      <form novalidate @submit.prevent="submit">
        <div class="field">
          <label for="reset-password">新密码</label>
          <input
            id="reset-password"
            v-model="form.password"
            name="password"
            type="password"
            autocomplete="new-password"
            maxlength="128"
            data-testid="reset-password"
          />
          <p class="muted hint" data-testid="reset-policy-hint">{{ PASSWORD_POLICY_TEXT }}</p>
        </div>

        <div class="field">
          <label for="reset-password-confirm">确认新密码</label>
          <input
            id="reset-password-confirm"
            v-model="form.confirm"
            name="confirm"
            type="password"
            autocomplete="new-password"
            maxlength="128"
            data-testid="reset-confirm"
          />
        </div>

        <p class="actions">
          <button
            class="primary"
            type="submit"
            :disabled="phase === 'busy'"
            :aria-busy="phase === 'busy'"
            data-testid="reset-submit"
          >
            <BrandIcon :name="phase === 'busy' ? 'refresh' : 'lock'" decorative :size="15" :class="{ spinning: phase === 'busy' }" />
            {{ phase === 'busy' ? '提交中…' : tokenDead ? '重新提交' : '重置密码' }}
          </button>
          <RouterLink class="btn" :to="{ name: 'login' }" data-testid="reset-to-login">
            <BrandIcon name="login" decorative :size="15" />
            回登录页（重新申请）
          </RouterLink>
        </p>
      </form>
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

/* 成功态：绿色描边，与错误态（红色）区分开。 */
.card.ok {
  border-color: var(--ok);
}

.card h1 {
  margin: 0 0 10px;
  font-size: 20px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.card p {
  margin: 0 0 12px;
  font-size: 13px;
  line-height: 1.8;
}

.field {
  margin-bottom: 14px;
}

.hint {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.5;
}

/* 行动区：主按钮 + 次级链接并排，窄屏自动折行。 */
.actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 16px 0 0;
}

button.primary,
a.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 13px;
  text-decoration: none;
}

button.primary {
  background: var(--accent);
  border-color: var(--accent);
  color: #04141b;
  font-weight: 600;
}

a.btn {
  background: var(--panel-2);
  color: var(--text);
}

a.btn:hover {
  border-color: var(--accent);
  text-decoration: none;
}

.spinning {
  animation: reset-spin 0.9s linear infinite;
}

@keyframes reset-spin {
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
