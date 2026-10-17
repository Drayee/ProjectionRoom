/**
 * 注册两段式 + 邮箱验证码的展示层逻辑（与后端 `register/start|email|verify` 对应）。
 *
 * # 交互形态
 *
 *  第 1 页：账号 + 密码                      → 提交后拿到 ticket
 *  第 2 页：邮箱 → 人机验证（**页内子窗体**）→ 发验证码 → 填码 → 完成
 *
 * 三条刻意的约定：
 *
 *  1. **不收昵称**：昵称默认等于用户名（服务端 `SanitizeDisplayName` 的同一条口径），
 *     以后在设置页面改。注册少一个必填项，是因为它此刻并不承载任何决策 ——
 *     用户可以事后改，而多一个输入框只会让注册更长。
 *  2. **人机验证在发码之前**，且是**页内子窗体**而不是 `window.open`：
 *     弹窗会被拦截，被挡住时用户只能看到"请允许弹窗"，那是一次白跑。
 *  3. **每次发码都要重新过验证**（包括"重新发送"）：否则"过了验证就能无限重发"，
 *     摩擦只值一次。
 *
 * # 幂等
 *
 * 三段各自一个幂等键槽（`utils/idempotency.ts`）：双击不会建两个账号、
 * 也不会让第一封码立刻作废。**输入变了就换键**（改用户名再提交是正常行为）。
 *
 * # 错误分诊
 *
 * 不用文案匹配，全部按 `AuthApiError.status` + `code`：
 *   400 `OTP_INVALID`              → "验证码不对，还能再试"
 *   400 `OTP_ATTEMPTS_EXCEEDED`    → "这条码已作废，请重新获取"（必须重新发）
 *   400 `REGISTER_TICKET_INVALID`  → "注册会话过期，请回到第一步"
 *   409 `USERNAME_TAKEN`/`EMAIL_TAKEN`
 *   429                            → 太频繁
 *   503 `MAIL_UNAVAILABLE`         → 验证码通道未开启
 */
import { computed, reactive, ref } from 'vue'
import * as authApi from '../../../api/auth'
import { toAuthError } from '../../../api/auth'
import { useAuthStore } from '../../../stores/auth'
import { createIdempotencySlot } from '../../../utils/idempotency'

export type RegisterStep = 'credentials' | 'email'

export function useRegisterFlow(onDone: () => Promise<void> | void) {
  const auth = useAuthStore()
  const step = ref<RegisterStep>('credentials')
  const busy = ref(false)
  const failed = ref('')
  const notice = ref('')

  /** 第一段的输入。**没有昵称**：注册后默认等于用户名。 */
  const cred = reactive({ username: '', password: '' })
  /** 第二段的输入。 */
  const mail = reactive({ email: '', code: '' })

  /** 第一段拿到的 ticket（后续两段的凭据）。 */
  const ticket = ref('')
  /** ticket 剩余有效期（秒），用于提示。 */
  const ticketExpiresIn = ref(0)

  /** 验证码是否已发出（发出后才显示验证码输入框）。 */
  const codeSent = ref(false)
  /**
   * 本机开发的验证码回显（`PR_OTP_DEV_ECHO=1` 时服务端才给，生产恒为空）。
   *
   * 它存在的唯一理由：没有 SMTP 的机器上，"注册 → 收码 → 建号"否则无法走通。
   * 界面只在非空时显示，并且明确标为调试信息——不让它看起来像正式功能。
   */
  const devCode = ref('')
  /** 人机验证子窗体是否打开。 */
  const captchaOpen = ref(false)

  // 三段的幂等键槽：各自的输入变化会换新键。
  const startKey = createIdempotencySlot()
  const emailKey = createIdempotencySlot()
  const verifyKey = createIdempotencySlot()

  /** 邮箱格式的本地校验（只在用户开始输入后提示）。 */
  const emailProblem = computed(() => {
    const v = mail.email.trim()
    if (v === '') return ''
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) ? '' : '邮箱格式看起来不对'
  })

  const passwordProblem = computed(() => {
    if (cred.password === '') return ''
    return cred.password.length >= 8 ? '' : '密码至少 8 位'
  })

  const canStart = computed(() => {
    if (busy.value) return false
    if (cred.username.trim() === '' || cred.password === '') return false
    return passwordProblem.value === ''
  })

  /** 邮箱是否已填且格式合法（决定"发送验证码"按钮能不能点）。 */
  const emailReady = computed(() => mail.email.trim() !== '' && emailProblem.value === '')

  /**
   * 按钮可用性：只要求邮箱就绪。
   *
   * ⚠ **不能**要求"人机验证已通过"：那颗按钮本身就是打开子窗体的入口，
   * 若它要求"已通过验证"才可点，就成了"要通过验证才能点开始验证"的死锁
   * （真机首验卡在这里过）。
   */
  const canSendCode = computed(() => !busy.value && emailReady.value)

  const canVerify = computed(() => {
    if (busy.value) return false
    return codeSent.value && mail.code.trim().length >= 4
  })

  /** 统一的错误翻译（按 status/code，不按文案）。 */
  function explain(err: unknown): string {
    const e = toAuthError(err)
    if (e.offline) return e.message
    if (e.status === 404) {
      // 与 useAuthForm 同一条约定：404 在这组端点上只有一个含义 —— 账号能力未启用。
      return '服务端当前未启用账号能力（游客模式）：看片不需要账号，有房间码即可进房。'
    }
    if (e.status === 503) return '验证码通道暂不可用（服务端未配置发信），请稍后再试或联系管理员。'
    if (e.status === 429) return '操作太频繁，请稍后再试。'
    if (e.status === 409) return e.message || '这个用户名或邮箱已经被占用了。'
    switch (e.code) {
      case 'OTP_INVALID':
        return '验证码不对。请检查邮件里的 6 位数字，还可以再试几次。'
      case 'OTP_ATTEMPTS_EXCEEDED':
        return '这条验证码错误次数过多，已作废。请重新获取一条。'
      case 'REGISTER_TICKET_INVALID':
        return '注册会话已失效（超过 15 分钟）。请回到第一步重新填写。'
      default:
        return e.message || '操作失败，请重试。'
    }
  }

  /** 第 1 页 → 第 2 页。 */
  async function submitCredentials(): Promise<void> {
    if (!canStart.value) return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      const username = cred.username.trim()
      const input = {
        username,
        // 不输入昵称：默认等于用户名（服务端也有同一条兜底，这里写明是为了意图清晰）。
        displayName: username,
        password: cred.password,
      }
      const res = await authApi.registerStart(input, startKey.for(input))
      ticket.value = res.ticket
      ticketExpiresIn.value = res.expiresIn
      step.value = 'email'
    } catch (err) {
      failed.value = explain(err)
    } finally {
      busy.value = false
    }
  }

  /**
   * 第 2 页主按钮：打开人机验证子窗体。
   *
   * 验证通过后由 `onCaptchaPass` 接着发码 —— 两个动作分开是刻意的：
   * 子窗体里的事件不该直接触发网络请求，否则"取消"与"通过"的路径会纠缠在一起。
   */
  function openCaptcha(): void {
    if (!emailReady.value) {
      failed.value = '请先填写一个合法的邮箱。'
      return
    }
    failed.value = ''
    notice.value = ''
    captchaOpen.value = true
  }

  /** 子窗体取消：什么都不做（不发码），只关掉它。 */
  function onCaptchaCancel(): void {
    captchaOpen.value = false
  }

  /** 子窗体通过：关掉它并发码。 */
  async function onCaptchaPass(): Promise<void> {
    captchaOpen.value = false
    await sendCode()
  }

  /** 真正发码（人机验证通过之后调用；"重新发送"也走这里，但会先重新过验证）。 */
  async function sendCode(): Promise<void> {
    if (busy.value) return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      const input = { ticket: ticket.value, email: mail.email.trim() }
      const echoed = await authApi.registerEmail(input.ticket, input.email, emailKey.for(input))
      devCode.value = echoed
      codeSent.value = true
      notice.value = echoed
        ? '验证码已生成（本机开发模式：没有配置 SMTP，验证码直接显示在下方）。'
        : '验证码已发出。请查看邮箱（含垃圾箱），5 分钟内有效。'
    } catch (err) {
      failed.value = explain(err)
    } finally {
      busy.value = false
    }
  }

  /**
   * 重新发送：**重新过一次人机验证**。
   *
   * 不能"过了验证就能无限重发"——那会把摩擦降到一次，而发码是花邮件配额、
   * 且是"给别人发骚扰信"的入口。
   */
  function resendCode(): void {
    if (!emailReady.value) {
      failed.value = '请先填写一个合法的邮箱。'
      return
    }
    codeSent.value = false
    mail.code = ''
    notice.value = ''
    failed.value = ''
    captchaOpen.value = true
  }

  /** 第 2 页完成：建号并登录。 */
  async function submitCode(): Promise<void> {
    if (!canVerify.value) return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      const input = { ticket: ticket.value, code: mail.code.trim() }
      const sess = await authApi.registerVerify(input.ticket, input.code, verifyKey.for(input))
      // 会话落地只有一个实现点（stores/auth 的 applySession）：这里把结果交给它，
      // 而不是自己写 accessToken —— 那会立刻出现第二处写 token 的代码。
      auth.adoptSession(sess)
      await onDone()
    } catch (err) {
      failed.value = explain(err)
    } finally {
      busy.value = false
    }
  }

  /** 回第 1 页（改用户名 / 口令）。ticket 作废，幂等键也换新。 */
  function backToCredentials(): void {
    step.value = 'credentials'
    ticket.value = ''
    ticketExpiresIn.value = 0
    codeSent.value = false
    captchaOpen.value = false
    mail.code = ''
    failed.value = ''
    notice.value = ''
    startKey.reset()
    emailKey.reset()
    verifyKey.reset()
  }

  return {
    step,
    busy,
    failed,
    notice,
    cred,
    mail,
    ticketExpiresIn,
    codeSent,
    captchaOpen,
    devCode,
    emailProblem,
    passwordProblem,
    emailReady,
    canStart,
    canSendCode,
    canVerify,
    submitCredentials,
    openCaptcha,
    onCaptchaPass,
    onCaptchaCancel,
    resendCode,
    submitCode,
    backToCredentials,
  }
}
