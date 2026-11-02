<script setup lang="ts">
/**
 * 账号设置块（`/me?tab=settings`，票 #71）。
 *
 * ## 三块内容
 *
 *   1. **改密码**（凭当前密码）—— 决议 #51 的两条门之一；
 *   2. **退出所有设备** —— 接既有的 `api.auth.logoutAll`；
 *   3. **账号只读信息** —— 注册时间（RelativeTime absolute 形）/ 角色 / 邮箱 / 邮箱验证态。
 *
 * 换绑邮箱（#72）与注销（#73）不在这里，各自是独立票。
 *
 * ## 两条改密门为什么并存
 *
 *   - 记得密码 → 这里，只要当前密码（`PUT /api/me/password`）；
 *   - 忘了密码 → 登录页「找回密码」，邮箱验证码（`password/otp` + `password/otp/reset`）。
 *
 * 它们**不互相替代**：本页的门要求"已经登录"，而"忘了密码的人"恰恰登录不了。
 * 已接受的取舍是：拿到有效会话的人就能改密码 —— 但那需要先有会话，
 * 而会话来自登录，登录要密码，第二道防线（邮箱码）仍在找回路径上。
 *
 * ## 改密成功之后发生了什么（界面必须跟得上）
 *
 * 服务端会撤销该账号**全部**会话并 `token_version+1`，同时给当前设备补发一枚新会话。
 * 前端必须把新会话落地（`auth.changePassword` 内部走 `applySession`），
 * 否则下一个请求就 401 —— 用户看到的会是"改完密码立刻被踢出去"。
 *
 * ## 幂等键
 *
 * 改密不可逆：真的跑第二遍会因"当前密码已不是那个"而 400，而用户看到的是
 * "我明明改成功了却说密码不对"。所以表单提交时生成一把键，重试期间**原样复用**，
 * 成功或用户改了输入才换新键（`createIdempotencySlot` 就是这个语义）。
 */
import { computed, reactive, ref, watch } from 'vue'
import Button from '../../ui/Button.vue'
import RelativeTime from '../../ui/RelativeTime.vue'
import { toHttpError, NETWORK_MESSAGE } from '../../../api/http'
import { createIdempotencySlot } from '../../../utils/idempotency'
import { useAuthStore } from '../../../stores/auth'

const auth = useAuthStore()

// ---------- 改密码 ----------

const form = reactive({ current: '', next: '', confirm: '' })
const busy = ref(false)
const notice = ref('')
const errorText = ref('')
/** 逐栏错误（与服务端字段一一对应；空串 = 该栏没问题）。 */
const fieldError = reactive({ current: '', next: '', confirm: '' })
const slot = createIdempotencySlot()

/** 客户端侧的最小校验：只挡"明显不用发请求"的那几种，策略真源仍在服务端。 */
const localProblem = computed<string>(() => {
  if (form.current.trim() === '') return '请填写当前密码'
  if (form.next.trim() === '') return '请填写新密码'
  if (form.confirm === '') return '请再输入一次新密码'
  if (form.next !== form.confirm) return '两次输入的新密码不一致'
  if (form.next === form.current) return '新密码不能与当前密码相同'
  return ''
})

function resetFieldErrors(): void {
  fieldError.current = ''
  fieldError.next = ''
  fieldError.confirm = ''
}

function clearForm(): void {
  form.current = ''
  form.next = ''
  form.confirm = ''
  // 清空输入 = 这是一次新的意图，旧键作废（否则下一次提交会复用一把
  // 已经"完成"的键，服务端直接回放上一次的成功响应 —— 而那会让用户以为
  // 这次也改成功了）。
  slot.reset()
}

/**
 * 提交改密。
 *
 * 失败分诊：
 *   - `PASSWORD_MISMATCH` → 定位到「当前密码」那一栏（服务端明确说了是哪一栏错）；
 *   - 口令策略（`BAD_REQUEST` + 文案里带"至少/必须"）→ 定位到「新密码」；
 *   - 401 → 会话失效，提示重新登录（`authedFetch` 不在这条路上，这里没有自动刷新）；
 *   - 网络不可达 → 用 `NETWORK_MESSAGE`，不要把它说成"密码不对"。
 */
async function submit(): Promise<void> {
  if (busy.value) return
  notice.value = ''
  errorText.value = ''
  resetFieldErrors()

  const problem = localProblem.value
  if (problem !== '') {
    errorText.value = problem
    return
  }

  busy.value = true
  try {
    await auth.changePassword({
      currentPassword: form.current,
      newPassword: form.next,
      idempotencyKey: slot.for({ c: form.current, n: form.next }),
    })
    // 成功：清空输入并换新键。提示语点明"其他设备已退出"——那是这次操作的
    // 真实后果，不说的话用户会以为别的设备还开着。
    clearForm()
    notice.value = '密码已更新，其他设备已退出登录。'
  } catch (err) {
    const e = toHttpError(err)
    if (e.offline) {
      errorText.value = NETWORK_MESSAGE
    } else if (e.code === 'PASSWORD_MISMATCH') {
      fieldError.current = e.message
    } else if (e.status === 401) {
      errorText.value = '登录状态已失效，请重新登录后再改密码。'
    } else if (e.status === 400) {
      // 其余 400 都是新口令不合策略（服务端文案已经说清原因）。
      fieldError.next = e.message
    } else {
      errorText.value = e.message
    }
  } finally {
    busy.value = false
  }
}

// ---------- 退出所有设备 ----------

const kickingAll = ref(false)
const kickError = ref('')
/** 页内确认条（不用 window.confirm：它在真机脚本里既不可读也不可点）。 */
const confirmingKick = ref(false)

function askKickAll(): void {
  kickError.value = ''
  confirmingKick.value = true
}

function cancelKickAll(): void {
  confirmingKick.value = false
}

async function doKickAll(): Promise<void> {
  if (kickingAll.value) return
  kickingAll.value = true
  kickError.value = ''
  try {
    await auth.logoutAll()
    confirmingKick.value = false
    // 会话已清空 —— 本页的游客分支会接管渲染（与 /me 的既有行为一致，
    // 不需要在这里自己 router.push）。
  } catch (err) {
    const e = toHttpError(err)
    kickError.value = e.offline
      ? NETWORK_MESSAGE
      : `退出所有设备的请求没有成功（${e.message}）。本地登录状态已清除。`
    confirmingKick.value = false
  } finally {
    kickingAll.value = false
  }
}

// ---------- 只读信息 ----------

const me = computed(() => auth.profile)
const roleLabel = computed(() => (me.value?.role === 'admin' ? '管理员' : '普通用户'))
const emailText = computed(() => {
  const value = me.value?.email ?? ''
  return value === '' ? '未绑定邮箱' : value
})
/** 三态：已验证 / 未验证 / 服务端未返回（不编造结论，与 AuthUser.emailVerified 的说明一致）。 */
const emailState = computed(() => {
  const flag = me.value?.emailVerified
  if (typeof flag !== 'boolean') return '服务端未返回验证状态'
  return flag ? '已验证' : '未验证'
})

// 换账号（登出再登录）时把上一轮的提示与输入清掉 —— 否则新用户会看到
// 前一个人留下的"密码已更新"。
watch(
  () => auth.profile?.id,
  () => {
    notice.value = ''
    errorText.value = ''
    kickError.value = ''
    resetFieldErrors()
    clearForm()
  },
)
</script>

<template>
  <div class="settings">
    <!-- ---------- 改密码 ---------- -->
    <section class="card" aria-labelledby="me-password-head">
      <h2 id="me-password-head" class="sect-title">改密码</h2>
      <p class="dim small">
        改完会退出其他所有设备，这一台会继续在线。忘了当前密码？
        退出登录后在登录页用「找回密码」走邮箱验证码。
      </p>

      <form class="form" novalidate @submit.prevent="submit">
        <div class="field">
          <label for="me-current-password">当前密码</label>
          <input
            id="me-current-password"
            v-model="form.current"
            type="password"
            autocomplete="current-password"
            :aria-invalid="fieldError.current !== '' || undefined"
            :aria-describedby="fieldError.current !== '' ? 'me-current-password-error' : undefined"
            data-testid="current-password"
          />
          <p v-if="fieldError.current" id="me-current-password-error" class="alert" role="alert">
            {{ fieldError.current }}
          </p>
        </div>

        <div class="field">
          <label for="me-new-password">新密码</label>
          <input
            id="me-new-password"
            v-model="form.next"
            type="password"
            autocomplete="new-password"
            :aria-invalid="fieldError.next !== '' || undefined"
            :aria-describedby="fieldError.next !== '' ? 'me-new-password-error' : 'me-password-hint'"
            data-testid="new-password"
          />
          <p v-if="fieldError.next" id="me-new-password-error" class="alert" role="alert">
            {{ fieldError.next }}
          </p>
          <p v-else id="me-password-hint" class="dim small">至少 8 位，同时包含字母与数字。</p>
        </div>

        <div class="field">
          <label for="me-confirm-password">再输一次新密码</label>
          <input
            id="me-confirm-password"
            v-model="form.confirm"
            type="password"
            autocomplete="new-password"
            :aria-invalid="fieldError.confirm !== '' || undefined"
            data-testid="confirm-password"
          />
          <p v-if="fieldError.confirm" class="alert" role="alert">{{ fieldError.confirm }}</p>
        </div>

        <p v-if="errorText" class="alert" role="alert" data-testid="settings-error">{{ errorText }}</p>
        <p v-if="notice" class="ok" role="status" data-testid="settings-notice">{{ notice }}</p>

        <div class="actions">
          <Button type="submit" variant="primary" :loading="busy" data-testid="change-password-submit">
            更新密码
          </Button>
        </div>
      </form>
    </section>

    <!-- ---------- 退出所有设备 ---------- -->
    <section class="card" aria-labelledby="me-kickall-head">
      <h2 id="me-kickall-head" class="sect-title">退出所有设备</h2>
      <p class="dim small">
        撤销这个账号在所有设备上的登录，包括这一台。下次使用要重新登录。
        怀疑密码泄露时，先改密码再点这里。
      </p>

      <div v-if="confirmingKick" class="confirm" role="alertdialog" aria-labelledby="me-kickall-confirm-text">
        <p id="me-kickall-confirm-text" class="confirm-text">
          确定要退出所有设备吗？这一台也会退出。
        </p>
        <div class="confirm-acts">
          <Button variant="danger" :loading="kickingAll" data-testid="kickall-confirm" @click="doKickAll">
            确定，全部退出
          </Button>
          <Button variant="ghost" :disabled="kickingAll" data-testid="kickall-cancel" @click="cancelKickAll">
            取消
          </Button>
        </div>
      </div>
      <div v-else class="actions">
        <Button variant="ghost" data-testid="kickall-start" @click="askKickAll">退出所有设备</Button>
      </div>

      <p v-if="kickError" class="alert" role="alert" data-testid="kickall-error">{{ kickError }}</p>
    </section>

    <!-- ---------- 只读信息 ---------- -->
    <section class="card" aria-labelledby="me-readonly-head">
      <h2 id="me-readonly-head" class="sect-title">账号信息</h2>
      <p class="dim small">这些字段目前不能在这里改：用户名固定，邮箱换绑与注销是单独的票。</p>
      <dl class="kv" data-testid="account-readonly">
        <dt>用户名</dt>
        <dd class="mono">{{ me?.username || '—' }}</dd>

        <dt>昵称</dt>
        <dd>{{ me?.displayName || '—' }}</dd>

        <dt>加入时间</dt>
        <dd>
          <!-- absolute 形：设置页要的是"哪一天注册的"，不是"三年前"。
               相对时间在这里没有信息量，反而会让人算半天（对一个刚注册的账号
               更糟：会显示"刚刚"，而这正是这个字段最需要说清的时刻）。 -->
          <RelativeTime v-if="me?.createdAt" :value="me.createdAt" :show-absolute="true" absolute />
          <span v-else class="dim">服务端未返回</span>
        </dd>

        <dt>角色</dt>
        <dd>{{ roleLabel }}</dd>

        <dt>邮箱</dt>
        <dd>{{ emailText }}</dd>

        <dt>邮箱状态</dt>
        <dd>{{ emailState }}</dd>
      </dl>
    </section>
  </div>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.sect-title {
  margin: 0;
  font-size: 15px;
}
.form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.field label {
  font-size: 13px;
  color: var(--dim);
}
.field input {
  min-height: 36px;
  padding: 6px 10px;
  font: inherit;
  font-size: 14px;
  color: var(--text);
  background: var(--bg-mid);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.field input:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}
.field input[aria-invalid='true'] {
  border-color: var(--danger);
}
.actions {
  display: flex;
  gap: 8px;
}
.confirm {
  border: 1px solid var(--danger);
  background: var(--bg-mid);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.confirm-text {
  margin: 0;
  font-size: 13px;
}
.confirm-acts {
  display: flex;
  gap: 8px;
}
.kv {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 16px;
  margin: 0;
  font-size: 13px;
}
.kv dt {
  color: var(--dim);
}
.kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.ok {
  margin: 0;
  font-size: 13px;
  color: var(--ok);
}
.alert {
  margin: 0;
  font-size: 13px;
  color: var(--danger);
}
.small {
  font-size: 12px;
}
@media (max-width: 560px) {
  .confirm-acts,
  .actions {
    flex-wrap: wrap;
  }
}
</style>
