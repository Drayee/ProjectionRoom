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
 * 换绑邮箱（#72）与注销（#73）已接进本块；头像拆在 `MeAvatarBlock.vue`
 *（#74 入口占位、#75 裁剪器）—— 那个块自带一套状态机，塞进来会让本文件失控。
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
import MeAvatarBlock from './MeAvatarBlock.vue'
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

// ---------- 换绑邮箱（票 #72） ----------

/**
 * 两段式：先把码发到**新地址**，再凭码确认。
 *
 * 为什么不能一步完成（把码发到当前邮箱）：那样证明的只是"我是主人"（会话已经
 * 证明了），而"我能读到这个新邮箱"根本没被验证。拿到会话的攻击者只要让受害者
 * 收到一封信（那封信受害者不会细看）就能把账号绑到自己邮箱上，随后走找回密码接管。
 *
 * 阶段用 `emailStep` 表示而不是两个独立表单：用户在同一次操作里先发码后确认，
 * 中间那个"码已寄出"的状态必须可见（否则他会以为按钮没生效而连点）。
 */
const emailForm = reactive({ next: '', code: '' })
const emailStep = ref<'idle' | 'sent'>('idle')
const emailBusy = ref(false)
const emailNotice = ref('')
const emailError = ref('')
/** 逐栏错误：地址那一栏与验证码那一栏。 */
const emailFieldError = reactive({ next: '', code: '' })
const emailSlot = createIdempotencySlot()

/** dev echo 回显的验证码（仅本机 `PR_OTP_DEV_ECHO=1`）。生产恒为空串。 */
const devCodeHint = ref('')

function resetEmailErrors(): void {
  emailFieldError.next = ''
  emailFieldError.code = ''
}

/** 发码：把 6 位数字寄到用户填的新地址。 */
async function sendEmailCode(): Promise<void> {
  if (emailBusy.value) return
  emailNotice.value = ''
  emailError.value = ''
  resetEmailErrors()
  devCodeHint.value = ''

  const target = emailForm.next.trim()
  if (target === '') {
    emailFieldError.next = '请填写新邮箱'
    return
  }

  emailBusy.value = true
  try {
    const result = await auth.sendEmailChangeOTP({
      email: target,
      idempotencyKey: emailSlot.for({ e: target }),
    })
    emailStep.value = 'sent'
    emailForm.code = ''
    emailNotice.value = `验证码已发到 ${target}，请查收后填入下方。`
    // dev echo 时顺手把它显示出来 —— 本机开发没有真邮箱，看不到码就走不完流程。
    if (result.devCode !== '') devCodeHint.value = result.devCode
  } catch (err) {
    const e = toHttpError(err)
    if (e.offline) {
      emailError.value = NETWORK_MESSAGE
    } else if (e.code === 'EMAIL_TAKEN') {
      // 服务端只说"这个地址已被别的账号绑定"，不透露是谁 —— 前端也不该猜。
      emailFieldError.next = '这个邮箱已被其他账号绑定，换一个吧。'
    } else if (e.status === 401) {
      emailError.value = '登录状态已失效，请重新登录后再换绑。'
    } else if (e.status === 400 || e.status === 429) {
      // 400：格式不对 / 与当前相同；429：60 秒冷却。
      emailFieldError.next = e.message
    } else {
      emailError.value = e.message
    }
  } finally {
    emailBusy.value = false
  }
}

/** 确认换绑：凭码把邮箱换成新地址，成功后服务端回的 Profile 由 store 落地。 */
async function confirmEmailCode(): Promise<void> {
  if (emailBusy.value) return
  emailNotice.value = ''
  emailError.value = ''
  resetEmailErrors()

  const target = emailForm.next.trim()
  const code = emailForm.code.trim()
  if (code === '') {
    emailFieldError.code = '请填写验证码'
    return
  }

  emailBusy.value = true
  try {
    await auth.confirmEmailChange({
      email: target,
      code,
      idempotencyKey: emailSlot.for({ e: target, c: code }),
    })
    emailStep.value = 'idle'
    emailForm.next = ''
    emailForm.code = ''
    devCodeHint.value = ''
    // 清空 = 新意图，旧幂等键作废（否则下次换绑会回放这次的成功响应）。
    emailSlot.reset()
    emailNotice.value = '邮箱已更新，之后的找回密码会发到新地址。'
  } catch (err) {
    const e = toHttpError(err)
    if (e.offline) {
      emailError.value = NETWORK_MESSAGE
    } else if (e.status === 401) {
      emailError.value = '登录状态已失效，请重新登录后再换绑。'
    } else if (e.status === 409) {
      // 发码与确认之间这个地址被别人抢走了 —— 用户要换一个。
      emailFieldError.next = '这个邮箱刚被其他账号绑定了，换一个吧。'
    } else if (e.status === 400) {
      emailFieldError.code = e.message
    } else {
      emailError.value = e.message
    }
  } finally {
    emailBusy.value = false
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

// ---------- 注销账号（票 #73） ----------
//
// 两道前置条件，缺一不可：
//
//   1. **当前密码** —— 与改密同一条理由：注销是不可逆方向的第一步，
//      只劫持到会话不足以注销别人的账号；
//   2. **勾选「我已知晓…」** —— 纯前端条件（决议 #51 §5）。刻意**不**进请求体：
//      传到服务端只会变成第二个可以伪造的字段，服务端该判的是"是不是你"。
//
// 两者都满足才出现"确认注销"按钮，且点它之后还要过一条**页内确认条**
// （与 #71 的「退出所有设备」同一个样板，不用 window.confirm —— 它在真机
// 脚本里既不可读也不可点）。

const deletingPassword = ref('')
const deletingAck = ref(false)
const deletingBusy = ref(false)
const deletingError = ref('')
const deletingNotice = ref('')
/** 页内确认条：第一次点"确认注销"先展开它，第二次才真的发请求。 */
const confirmingDelete = ref(false)
const deleteSlot = createIdempotencySlot()

/** 两道前置条件都满足才允许点"确认注销"。 */
const canRequestDeletion = computed(
  () => deletingPassword.value.trim() !== '' && deletingAck.value,
)

/** 已经在冷静期里就不该再出现发起入口（横幅在 /me 顶部负责撤销）。 */
const alreadyPending = computed(() => (auth.profile?.deletionRequestedAt ?? '') !== '')

function askDelete(): void {
  deletingError.value = ''
  deletingNotice.value = ''
  confirmingDelete.value = true
}

function cancelDelete(): void {
  confirmingDelete.value = false
}

async function doRequestDeletion(): Promise<void> {
  if (deletingBusy.value) return
  deletingBusy.value = true
  deletingError.value = ''
  deletingNotice.value = ''
  try {
    const state = await auth.requestAccountDeletion({
      currentPassword: deletingPassword.value,
      // 幂等键：注销不可逆，一次"网络超时后重发"如果真跑两遍会撞上
      // 服务端的"不延期"语义 —— 那倒不危险，但拿同一个成功响应更干净。
      idempotencyKey: deleteSlot.for({ password: deletingPassword.value }),
    })
    // 清掉密码（它已经用完了）与勾选，但**保留**成功提示 ——
    // 否则用户看不出发生了什么（横幅在页面顶部，设置区滚下去看不见）。
    deletingPassword.value = ''
    deletingAck.value = false
    confirmingDelete.value = false
    deleteSlot.reset()
    deletingNotice.value =
      state.deadline === null
        ? '已提交注销申请。在冷静期内你可以随时在页面顶部撤销。'
        : '已提交注销申请。在到期之前，你可以随时在页面顶部撤销。'
  } catch (err) {
    const e = toHttpError(err)
    if (e.status === 401) {
      deletingError.value = '登录状态已失效，请重新登录后再试'
      return
    }
    if (e.status === 403) {
      deletingError.value = `${e.message}（被封禁的账号不能注销；如有疑问请联系管理员）`
      return
    }
    if (e.code === 'PASSWORD_MISMATCH') {
      // 错密码要定位到输入框，而不是整页一条报错 —— 用户要采取的动作是"改这一栏"。
      deletingError.value = '当前密码不对，请重新输入'
      return
    }
    deletingError.value = e.offline ? NETWORK_MESSAGE : e.message
  } finally {
    deletingBusy.value = false
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
    deletingPassword.value = ''
    deletingAck.value = false
    deletingError.value = ''
    deletingNotice.value = ''
    confirmingDelete.value = false
    resetFieldErrors()
    clearForm()
  },
)
</script>

<template>
  <div class="settings">
    <!-- ---------- 头像（票 #74 入口 / #75 裁剪器） ---------- -->
    <MeAvatarBlock />

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

    <!-- ---------- 换绑邮箱（票 #72） ---------- -->
    <section class="card" aria-labelledby="me-email-head">
      <h2 id="me-email-head" class="sect-title">换绑邮箱</h2>
      <p class="dim small">
        验证码会发到**新**邮箱 —— 那是在证明你能读到它。换绑成功后旧邮箱立刻失效，
        之后的「找回密码」会发到新地址。
      </p>

      <form class="form" @submit.prevent="emailStep === 'idle' ? sendEmailCode() : confirmEmailCode()">
        <div class="field">
          <label for="me-email-next">新邮箱</label>
          <input
            id="me-email-next"
            v-model="emailForm.next"
            type="email"
            autocomplete="email"
            inputmode="email"
            data-testid="email-next"
            :disabled="emailBusy"
            :aria-invalid="emailFieldError.next !== ''"
          />
          <p v-if="emailFieldError.next" class="alert" role="alert" data-testid="email-next-error">
            {{ emailFieldError.next }}
          </p>
        </div>

        <div v-if="emailStep === 'sent'" class="field">
          <label for="me-email-code">验证码</label>
          <input
            id="me-email-code"
            v-model="emailForm.code"
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="6"
            data-testid="email-code"
            :disabled="emailBusy"
            :aria-invalid="emailFieldError.code !== ''"
          />
          <p v-if="emailFieldError.code" class="alert" role="alert" data-testid="email-code-error">
            {{ emailFieldError.code }}
          </p>
          <p v-if="devCodeHint" class="dim small" data-testid="email-dev-code">
            本机开发回显：{{ devCodeHint }}
          </p>
        </div>

        <p v-if="emailNotice" class="ok" role="status" data-testid="email-notice">{{ emailNotice }}</p>
        <p v-if="emailError" class="alert" role="alert" data-testid="email-error">{{ emailError }}</p>

        <div class="actions">
          <Button
            type="submit"
            variant="primary"
            data-testid="email-submit"
            :loading="emailBusy"
          >
            {{ emailStep === 'idle' ? '发送验证码' : '确认换绑' }}
          </Button>
          <Button
            v-if="emailStep === 'sent'"
            variant="ghost"
            data-testid="email-resend"
            :disabled="emailBusy"
            @click="sendEmailCode"
          >
            重新发送
          </Button>
        </div>
      </form>
    </section>

    <!-- ---------- 注销账号（票 #73） ---------- -->
    <section class="card danger-card" aria-labelledby="me-deletion-head">
      <h2 id="me-deletion-head" class="sect-title">注销账号</h2>

      <!-- 已在冷静期：这里只说明"去哪撤销"，不再给发起入口。
           横幅在 /me 顶部（跨分区常驻），所以这里不重复一遍撤销按钮。 -->
      <template v-if="alreadyPending">
        <p class="dim small" data-testid="deletion-pending-note">
          这个账号已在注销冷静期里。页面顶部有一条横幅，可以在那里撤销。
        </p>
      </template>

      <template v-else>
        <p class="dim small">
          注销后，你的帖子、私聊、关注关系与放映室会在冷静期结束时被<strong>永久删除</strong>，无法恢复。
          在冷静期结束之前，你可以随时撤销。
        </p>

        <form class="form" @submit.prevent="askDelete">
          <div class="field">
            <label for="me-delete-password">当前密码</label>
            <input
              id="me-delete-password"
              v-model="deletingPassword"
              type="password"
              autocomplete="current-password"
              data-testid="deletion-password"
            />
          </div>

          <label class="ack">
            <input v-model="deletingAck" type="checkbox" data-testid="deletion-ack" />
            <span>我已知晓：冷静期结束后，这个账号的数据将被永久删除。</span>
          </label>

          <!-- 页内确认条（第一次点按钮先展开它）。 -->
          <div v-if="confirmingDelete" class="confirm" role="alertdialog" aria-labelledby="me-delete-confirm-text">
            <p id="me-delete-confirm-text" class="confirm-text">
              确定要注销这个账号吗？你会立刻从社区里消失，数据将在冷静期结束后被永久删除。
            </p>
            <div class="confirm-acts">
              <Button variant="danger" :loading="deletingBusy" data-testid="deletion-confirm" @click="doRequestDeletion">
                确定注销
              </Button>
              <Button variant="ghost" :disabled="deletingBusy" data-testid="deletion-confirm-cancel" @click="cancelDelete">
                再想想
              </Button>
            </div>
          </div>
          <div v-else class="actions">
            <Button
              type="submit"
              variant="danger"
              :disabled="!canRequestDeletion"
              data-testid="deletion-start"
            >
              注销账号
            </Button>
          </div>
        </form>

        <p v-if="!canRequestDeletion" class="dim small" data-testid="deletion-hint">
          需要先填写当前密码并勾选上面的确认项。
        </p>
      </template>

      <p v-if="deletingError" class="alert" role="alert" data-testid="deletion-request-error">{{ deletingError }}</p>
      <p v-if="deletingNotice" class="ok" role="status" data-testid="deletion-request-notice">{{ deletingNotice }}</p>
    </section>

    <!-- ---------- 只读信息 ---------- -->
    <section class="card" aria-labelledby="me-readonly-head">
      <h2 id="me-readonly-head" class="sect-title">账号信息</h2>
      <p class="dim small">这些字段不能在这里改：用户名固定（改用户名不在计划里），邮箱请用上面的「换绑邮箱」，注销账号请用上面的「注销账号」。</p>
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
.ack {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-dim);
  cursor: pointer;
}
.ack input {
  margin-top: 2px;
  flex: 0 0 auto;
}
/*
 * 危险区（票 #73）。
 *
 * 只用边框与标题着色，不做整块红底：这一页里"危险"的东西有两处
 * （退出所有设备、注销账号），整块红底会让真正更严重的那个失去区分度。
 * 红边框足以让它在滚动时被认出来。
 */
.danger-card {
  border-color: var(--danger);
}
.danger-card .sect-title {
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
