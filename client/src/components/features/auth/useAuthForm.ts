/**
 * 展示层 composable：登录 / 注册 / 找回密码三页签表单的交互逻辑。
 * 不含协议实现（协议在 api/auth.ts，逻辑层）。
 *
 * 从旧 AuthPanel.vue（696 行）下沉：三页签状态机、本地校验、
 * forgot 的 429/503 分诊、submit 全流程。
 */
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import * as authApi from '../../../api/auth'
import { toAuthError } from '../../../api/auth'
import { useAuthStore } from '../../../stores/auth'
import { safeRedirectPath } from '../../../utils/safeRedirect'

export type AuthTab = 'login' | 'register' | 'forgot'

export function useAuthForm(initialTab: AuthTab = 'login') {
  const auth = useAuthStore()
  const route = useRoute()
  const router = useRouter()

  const tab = ref<AuthTab>(initialTab)
  const busy = ref(false)
  const notice = ref('')
  const failed = ref('')

  const form = reactive({
    username: '',
    password: '',
    displayName: '',
    email: '',
  })

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

  const canSubmit = computed(() => {
    if (busy.value) return false
    if (tab.value === 'forgot') return form.email.trim() !== '' && emailProblem.value === ''
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
      // forgot：恒定响应，不泄露账号是否存在
      await authApi.forgot({ email: form.email.trim() })
      notice.value = '如果这个邮箱注册过，重置链接已经发出。请查收（含垃圾箱）。'
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
  }
}
