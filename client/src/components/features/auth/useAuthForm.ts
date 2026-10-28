/**
 * 展示层 composable：登录 / 注册 / 找回密码三页签表单的交互逻辑。
 * 不含协议实现（协议在 api/auth.ts，逻辑层）。
 *
 * # 找回密码是**两段式**（邮箱验证码）
 *
 * 旧形态是"填邮箱 → 服务端发一封重置链接"。它已随 `/api/auth/forgot` +
 * `/api/auth/reset` 一起下线，原因不是链接不安全（恰恰相反，256 bit 的链接
 * 比 6 位数字更抗爆破），而是它**解决不了那个真实的问题**：
 * 链接通道必须 gate 在 `email_verified_at` 上（否则攻击者填受害者的邮箱，
 * 受害者点开链接就把**攻击者账号**的密码改掉了），于是"邮箱未验证"的老账号
 * 永远找不回密码 —— 而对外响应又是恒定的 200，用户只会看到"已发送"却收不到信。
 *
 * 验证码没有这个两难：码只发到这个邮箱的真实持有者手里，攻击者填别人的地址
 * 只能让受害者收到一封莫名其妙的验证码。所以它**对未验证邮箱也开放**。
 *
 * 两段：
 *   1. `email`  —— 填邮箱 → 人机验证（页内子窗体）→ 发码；
 *   2. `reset`  —— 填验证码 + 新密码 → 改密成功（清本地会话，跳登录）。
 *
 * 人机验证与注册那条路一致：发码是花邮件配额、且是"给别人发骚扰信"的入口。
 */
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import * as authApi from '../../../api/auth'
import { toAuthError } from '../../../api/auth'
import { useAuthStore } from '../../../stores/auth'
import { safeRedirectPath } from '../../../utils/safeRedirect'
import { createIdempotencySlot } from '../../../utils/idempotency'

export type AuthTab = 'login' | 'register' | 'forgot'

/** 找回密码的两个阶段。 */
export type ForgotStep = 'email' | 'reset'

/** 从路由 query 里读页签（只认三个合法值，其余一律忽略）。 */
function readTabFromQuery(raw: unknown): AuthTab | null {
  const v = Array.isArray(raw) ? raw[0] : raw
  return v === 'forgot' || v === 'register' || v === 'login' ? v : null
}

export function useAuthForm(initialTab: AuthTab = 'login') {
  const auth = useAuthStore()
  const route = useRoute()
  const router = useRouter()

  /**
   * 首屏页签：`?tab=forgot|register|login` **优先于** `initialTab`。
   *
   * 这条不是装饰，是三个入口的依赖：
   *   - `VerifyEmailPage` 验证失败/成功后的跳转（`/login?tab=forgot`）；
   *   - 旧验收脚本与文档里写死的深链；
   *   - 用户从邮件页退回时"回到我刚才是哪个页签"。
   * 丢了它，症状是"链接打开后停在登录页签，找回密码的表单根本没渲染"。
   */
  const tab = ref<AuthTab>(readTabFromQuery(route.query.tab) ?? initialTab)
  const busy = ref(false)
  const notice = ref('')
  const failed = ref('')

  const form = reactive({
    username: '',
    password: '',
    displayName: '',
    email: '',
  })

  // ---------- 找回密码的状态 ----------

  /** 当前阶段：填邮箱发码 / 填码改密。 */
  const forgotStep = ref<ForgotStep>('email')
  /** 验证码输入（只在 `reset` 阶段有意义）。 */
  const otpCode = ref('')
  /** 新密码（只在 `reset` 阶段有意义）。 */
  const newPassword = ref('')
  /** 是否已发出验证码（决定第二阶段能不能提交）。 */
  const codeSent = ref(false)
  /**
   * 本机开发的验证码回显（`PR_OTP_DEV_ECHO=1` 时服务端才给，生产恒为空）。
   *
   * 与注册那条路同一个理由：没有 SMTP 的机器上，"发码 → 输码 → 改密"否则
   * 无法端到端走通。界面只在非空时显示，并明确标为调试信息。
   */
  const devCode = ref('')
  /** 人机验证子窗体是否打开。 */
  const captchaOpen = ref(false)
  /** 改密端点的幂等键槽（输入变化会换新键）。 */
  const resetKey = createIdempotencySlot()

  /** 登录成功后的回跳目标（白名单校验，防开放重定向）。 */
  const redirectTo = computed(() => safeRedirectPath(route.query.redirect, '/'))

  /** 本地校验：只在用户已经开始输入后才提示，避免一进页面就飘红。 */
  const emailProblem = computed(() => {
    const v = form.email.trim()
    if (v === '') return ''
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) ? '' : '邮箱格式看起来不对'
  })

  const passwordProblem = computed(() => {
    if (form.password === '') return ''
    return form.password.length >= 8 ? '' : '密码至少 8 位'
  })

  /** 新密码的本地校验（与注册同一条策略：至少 8 位）。 */
  const newPasswordProblem = computed(() => {
    if (newPassword.value === '') return ''
    return newPassword.value.length >= 8 ? '' : '密码至少 8 位'
  })

  const canSubmit = computed(() => {
    if (busy.value) return false
    if (tab.value === 'forgot') {
      if (forgotStep.value === 'email') return form.email.trim() !== '' && emailProblem.value === ''
      // 第二阶段：码与口令都要有。码的位数由服务端最终判定，这里只挡明显空的。
      return (
        codeSent.value &&
        otpCode.value.trim().length >= 4 &&
        newPassword.value.length >= 8 &&
        newPasswordProblem.value === ''
      )
    }
    if (form.username.trim() === '' || form.password === '') return false
    if (tab.value === 'register') {
      return form.email.trim() !== '' && emailProblem.value === '' && passwordProblem.value === ''
    }
    return true
  })

  function switchTab(next: AuthTab): void {
    tab.value = next
    failed.value = ''
    notice.value = ''
  }

  /**
   * 找回密码：打开人机验证子窗体。
   *
   * 与注册那条路一致：发码前先过摩擦（`CaptchaDialog`）。它**不是**安全边界
   * （题面与校验都在前端），定位是"让脚本轰炸变贵"。
   */
  function openForgotCaptcha(): void {
    if (form.email.trim() === '') {
      failed.value = '请先填写邮箱'
      return
    }
    if (emailProblem.value !== '') {
      failed.value = emailProblem.value
      return
    }
    failed.value = ''
    notice.value = ''
    captchaOpen.value = true
  }

  function onForgotCaptchaCancel(): void {
    captchaOpen.value = false
  }

  /** 子窗体通过：关掉它并发码。 */
  async function onForgotCaptchaPass(): Promise<void> {
    captchaOpen.value = false
    await sendForgotCode()
  }

  /** 真正发码（人机验证通过之后调用；"重新发送"也走这里，但会先重新过验证）。 */
  async function sendForgotCode(): Promise<void> {
    if (busy.value) return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      const echoed = await authApi.passwordOTP(form.email.trim())
      devCode.value = echoed
      codeSent.value = true
      forgotStep.value = 'reset'
      notice.value = echoed
        ? '验证码已生成（本机开发模式：没有配置 SMTP，验证码直接显示在下方）。'
        : '验证码已发出。请查看邮箱（含垃圾箱），5 分钟内有效。'
    } catch (err) {
      const e = toAuthError(err)
      if (e.status === 429) failed.value = '操作太频繁，请稍后再试。'
      else if (e.status === 503) failed.value = '邮件服务暂不可用，请稍后再试或联系管理员。'
      else if (e.status === 404) failed.value = '服务端当前未启用账号能力（游客模式）：看片不需要账号，有房间码即可进房。'
      else failed.value = e.message || '发送失败，请重试。'
    } finally {
      busy.value = false
    }
  }

  /** 重新发送：**重新过一次人机验证**（理由同注册那条路）。 */
  function resendForgotCode(): void {
    otpCode.value = ''
    codeSent.value = false
    devCode.value = ''
    notice.value = ''
    failed.value = ''
    openForgotCaptcha()
  }

  /**
   * 第二阶段：凭码改密。
   *
   * ⚠ **不要**在这里再写 `if (!canSubmit.value) return`。
   * 上一版这么写过，症状是"点『设置新密码』毫无反应"：调用方 `submit()` 在进来之前
   * 已经 `busy = true`，而 `canSubmit` 的第一条就是 `if (busy.value) return false` ——
   * 于是被自己刚设的 busy 挡回、静默 return，既不发请求也不报错，
   * 只有 notice 被清空留作痕迹。判据只能有一处（`submit()` 顶部），这里重复守卫就是自锁。
   *
   * 保留的字段检查**刻意不看 busy**：它防的是"别处将来直接调用这个函数"，
   * 而不是防重复提交（那个职责在 canSubmit）。
   */
  async function submitForgotReset(): Promise<void> {
    if (otpCode.value.trim() === '' || newPassword.value.length < 8 || form.email.trim() === '') return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      const input = { email: form.email.trim(), code: otpCode.value.trim(), password: newPassword.value }
      await authApi.passwordOTPReset(input, resetKey.for(input))
      // 改密会撤销该账号**全部**会话（含本机）：本地还留着旧 access token 只会
      // 立刻 401。清掉它，并把用户送到登录页用新口令重新登。
      auth.clearSession()
      // ⚠ 必须**在这里**把页签与找回密码状态重置，不能指望 `router.push` 帮你重渲染：
      // /login 与 /login?tab=forgot 命中的是同一个组件实例，`tab` 只在实例创建时
      // 从 query 读一次。少这三步的症状是"提示让你去登录，但屏幕上还是找回密码的
      // 第二阶段，验证码与密码都还填在里面"。
      notice.value = '密码已重置。请用新密码重新登录。'
      resetForgotState()
      tab.value = 'login'
      await router.push({ name: 'login' })
    } catch (err) {
      const e = toAuthError(err)
      if (e.status === 429) failed.value = '操作太频繁，请稍后再试。'
      else if (e.code === 'OTP_ATTEMPTS_EXCEEDED') failed.value = '这条验证码错误次数过多，已作废。请重新获取一条。'
      else if (e.code === 'OTP_INVALID') failed.value = '验证码不对。请检查邮件里的 6 位数字，还可以再试几次。'
      else failed.value = e.message || '重置失败，请重试。'
    } finally {
      busy.value = false
    }
  }

  /** 回到找回密码第 1 段（改邮箱 / 重新发码）。 */
  function backToForgotEmail(): void {
    resetForgotState()
    failed.value = ''
    notice.value = ''
  }

  /**
   * 清掉找回密码的全部阶段状态。
   *
   * 刻意**不碰** `notice` / `failed`：那两个提示槽的 owner 是调用方 ——
   * 改密成功那条路径要先写下"请用新密码重新登录"再调这里，如果这里顺手清了，
   * 那句提示就没了（而这正是用户当下唯一需要看见的话）。
   */
  function resetForgotState(): void {
    forgotStep.value = 'email'
    otpCode.value = ''
    newPassword.value = ''
    codeSent.value = false
    devCode.value = ''
  }

  async function submit(): Promise<void> {
    if (!canSubmit.value) return
    busy.value = true
    failed.value = ''
    notice.value = ''
    try {
      if (tab.value === 'login') {
        await auth.login({ username: form.username.trim(), password: form.password })
        await router.push(redirectTo.value)
        return
      }
      if (tab.value === 'register') {
        await auth.register({
          username: form.username.trim(),
          password: form.password,
          displayName: form.displayName.trim() || form.username.trim(),
          email: form.email.trim(),
        })
        notice.value = '注册成功。验证邮件已发出，验证后即可用于找回密码。'
        await router.push(redirectTo.value)
        return
      }
      // 找回密码：第一阶段（发码）走这条，第二阶段由 submitForgotReset 负责。
      if (forgotStep.value === 'email') {
        openForgotCaptcha()
        return
      }
      await submitForgotReset()
    } catch (err) {
      const e = toAuthError(err)
      // 分诊：网络不可达 / 账号能力未启用 / 429 太频繁 / 503 邮件不可用 / 凭据错，处置各不相同
      if (e.offline) failed.value = e.message
      else if (e.status === 404) {
        // 404 在这组端点上只有一个含义：**服务端没注册 /api/auth/***
        //（账号能力关闭，即 PR_DB_DSN 为空，见 CLAUDE §5 的"两步部署"）。
        // 它不是"地址写错了"，所以绝不能把裸的「请求失败（HTTP 404）」丢给用户 ——
        // 那句话既解释不了状态，也指不出出路，看起来就像整个后端都挂了。
        // 这里如实说明当前形态，并给出仍然可用的那条路（游客进房）。
        failed.value = '服务端当前未启用账号能力（游客模式）：看片不需要账号，有房间码即可进房。'
      } else if (e.status === 429) failed.value = '操作太频繁，请稍后再试。'
      else if (e.status === 503) failed.value = '邮件服务暂不可用，请稍后再试或联系管理员。'
      else failed.value = e.message || '操作失败，请重试。'
    } finally {
      busy.value = false
    }
  }

  return {
    tab,
    busy,
    notice,
    failed,
    form,
    emailProblem,
    passwordProblem,
    canSubmit,
    switchTab,
    submit,

    // 找回密码（两段式）
    forgotStep,
    otpCode,
    newPassword,
    newPasswordProblem,
    codeSent,
    devCode,
    captchaOpen,
    openForgotCaptcha,
    onForgotCaptchaCancel,
    onForgotCaptchaPass,
    resendForgotCode,
    backToForgotEmail,
  }
}
