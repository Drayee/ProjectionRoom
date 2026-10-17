<script setup lang="ts">
/**
 * 登录 / 注册 / 找回密码表单（三页签）。
 *
 * 逻辑在 useAuthForm（展示层 composable）；本组件只渲染。
 * 归 features/auth/：它 import api/auth，有业务能力。
 *
 * 视觉（B「数字切片」）：四角直角标记、上下 RGB 分离装饰线、
 * 聚焦分离描边、会抖的故障分割线、矩形主按钮。
 * 窗内标题不在此组件——由外层 LandingView 的 win-bar 承担。
 */
import { useRoute, useRouter } from 'vue-router'
import BrandIcon from '../../ui/BrandIcon.vue'
import CaptchaDialog from './CaptchaDialog.vue'
import { useAuthForm, type AuthTab } from './useAuthForm'
import { useRegisterFlow } from './useRegisterFlow'
import { safeRedirectPath } from '../../../utils/safeRedirect'

const props = withDefaults(
  defineProps<{
    /** 初始页签。 */
    initialTab?: AuthTab
    /** 是否显示「直接进放映室」入口（落地页形态要，独立 /login 页不要）。 */
    directEntry?: boolean
  }>(),
  { initialTab: 'login', directEntry: false },
)

const emit = defineEmits<{ (e: 'direct'): void }>()

const f = useAuthForm(props.initialTab)

const route = useRoute()
const router = useRouter()

/**
 * 注册走**两段式**（先用户名口令，再邮箱验证码）。
 *
 * 它与 `useAuthForm` 的 register 页签并存：
 *   - 页签仍然是「注册」，但表单内容由 `reg.step` 决定渲染哪一页；
 *   - 完成之后跳到登录前的回跳目标（与登录同一条安全白名单）。
 */
const reg = useRegisterFlow(async () => {
  await router.push(safeRedirectPath(route.query.redirect, '/'))
})

const TABS: { key: AuthTab; label: string }[] = [
  { key: 'login', label: '登录' },
  { key: 'register', label: '注册' },
  { key: 'forgot', label: '找回密码' },
]
</script>

<template>
  <div class="panel">
    <!-- 四角直角标记：把“窗口”框出来（纯装饰） -->
    <span class="corner c-tl" aria-hidden="true" />
    <span class="corner c-tr" aria-hidden="true" />
    <span class="corner c-bl" aria-hidden="true" />
    <span class="corner c-br" aria-hidden="true" />

    <div class="tabs" role="tablist">
      <button
        v-for="t in TABS"
        :key="t.key"
        type="button"
        role="tab"
        :aria-selected="f.tab.value === t.key"
        :class="{ on: f.tab.value === t.key }"
        @click="f.switchTab(t.key)"
      >
        {{ t.label }}
      </button>
    </div>

    <!-- ================= 注册：两页式（用户要求的形态） =================
         第 1 页 账号 + 口令；第 2 页 邮箱 → 人机验证（页内子窗体）→ 验证码。
         它与登录 / 找回共用同一个窗口外壳，所以视觉上是"翻了一页"而不是换了个页面。 -->
    <form v-if="f.tab.value === 'register'" class="reg" @submit.prevent>
      <!-- 步骤指示：两页式必须让人知道自己在第几步、还剩几步。
           下面的进度条按步填充（transform: scaleX），是这一屏唯一的常驻动效。 -->
      <ol class="steps" aria-label="注册进度">
        <li :class="{ on: reg.step.value === 'credentials', done: reg.step.value === 'email' }">
          <span class="dot" aria-hidden="true" />1 账号与密码
        </li>
        <li :class="{ on: reg.step.value === 'email' }"><span class="dot" aria-hidden="true" />2 验证邮箱</li>
      </ol>
      <div class="bar-track" aria-hidden="true">
        <span class="bar-fill" :class="{ full: reg.step.value === 'email' }" />
      </div>

      <!-- 翻页过渡：**不用 `<Transition mode="out-in">`**。
           那个模式要等旧页的 transitionend 才挂载新页，而它的驱动依赖双 rAF ——
           标签页在后台/无头环境里 rAF 会被节流，于是"旧页永远不离开、新页永不出现"，
           整张表单卡死（真机验证时就卡成这样）。
           改法是让新页**自带入场动画**：`:key` 变 → 元素重建 → 动画自动播一次。
           没有离场阶段，就没有"等不到 transitionend"这回事。 -->
      <div v-if="reg.step.value === 'credentials'" key="credentials" class="pane">
        <div class="field">
          <label for="reg-username">账号</label>
          <input
            id="reg-username"
            v-model="reg.cred.username"
            name="username"
            autocomplete="username"
            placeholder="3-20 位字母、数字或下划线"
          />
        </div>
        <div class="field">
          <label for="reg-password">密码</label>
          <input
            id="reg-password"
            v-model="reg.cred.password"
            name="password"
            type="password"
            autocomplete="new-password"
            placeholder="至少 8 位，含字母与数字"
          />
          <p v-if="reg.passwordProblem.value" class="hint">{{ reg.passwordProblem.value }}</p>
        </div>

        <!-- 不收昵称：注册只填账号与密码。昵称由服务端默认取用户名，之后在设置里改。 -->

        <!-- 错误条自带入场动画（见 .alert 的 animation），不需要 <Transition>：
             Transition 的离场依赖 transitionend + 双 rAF，后台/无头标签页里 rAF 会被
             节流到十几秒，元素会卡在 DOM 里继续占位甚至挡住点击。 -->
        <p v-if="reg.failed.value" class="alert" role="alert">{{ reg.failed.value }}</p>

        <div class="row">
          <button
            class="primary"
            type="button"
            :disabled="!reg.canStart.value || reg.busy.value"
            data-testid="reg-next"
            @click="reg.submitCredentials()"
          >
            <BrandIcon v-if="reg.busy.value" name="load-spinner" decorative :size="15" />
            <span>下一步</span>
          </button>
        </div>
      </div>

        <!-- 第 2 页 -->
        <div v-else key="email" class="pane">
          <div class="field">
            <label for="reg-email">邮箱</label>
            <input
              id="reg-email"
              v-model="reg.mail.email"
              name="email"
              type="email"
              autocomplete="email"
              placeholder="用于接收验证码"
              :disabled="reg.codeSent.value"
            />
            <p v-if="reg.emailProblem.value" class="hint">{{ reg.emailProblem.value }}</p>
          </div>

          <!-- 人机验证在**页内子窗体**里完成，通过后自动发码。
               按钮只负责"打开子窗体"——把验证与发码分成两步，取消路径才干净。 -->
          <div v-if="!reg.codeSent.value" class="row">
            <button
              class="primary"
              type="button"
              :disabled="!reg.canSendCode.value || reg.busy.value"
              data-testid="reg-captcha-send"
              @click="reg.openCaptcha()"
            >
              <BrandIcon v-if="reg.busy.value" name="load-spinner" decorative :size="15" />
              <span>人机验证并发送验证码</span>
            </button>
          </div>

          <!-- 验证码区整块浮现（.code-block 自带 rise-in 动画），让人看清多了一步 -->
          <div v-if="reg.codeSent.value" class="code-block">
            <div class="field">
              <label for="reg-code">验证码</label>
              <input
                id="reg-code"
                v-model="reg.mail.code"
                name="code"
                inputmode="numeric"
                autocomplete="one-time-code"
                maxlength="6"
                placeholder="邮件里的 6 位数字"
                data-testid="reg-code"
              />
            </div>

            <!-- 本机开发的验证码回显：服务端只在 PR_OTP_DEV_ECHO=1 时给 devCode，
                 生产环境该字段不存在，这一块整段不渲染。 -->
            <p v-if="reg.devCode.value" class="dev-hint" data-testid="reg-dev-code">
              调试（本机无 SMTP）：验证码是 <strong class="mono">{{ reg.devCode.value }}</strong>
            </p>
            <div class="row">
              <button
                class="primary"
                type="button"
                :disabled="!reg.canVerify.value || reg.busy.value"
                data-testid="reg-verify"
                @click="reg.submitCode()"
              >
                <BrandIcon v-if="reg.busy.value" name="load-spinner" decorative :size="15" />
                <span>完成注册</span>
              </button>
              <button class="ghost" type="button" :disabled="reg.busy.value" data-testid="reg-resend" @click="reg.resendCode()">
                重新发送
              </button>
            </div>
          </div>

          <p v-if="reg.failed.value" class="alert" role="alert">{{ reg.failed.value }}</p>
          <p v-if="reg.notice.value" class="ok" role="status">{{ reg.notice.value }}</p>

          <div class="row">
            <button class="ghost" type="button" :disabled="reg.busy.value" @click="reg.backToCredentials()">← 上一步</button>
          </div>
        </div>
    </form>

    <!-- ================= 登录 / 找回密码（既有形态，未改） ================= -->
    <form v-else @submit.prevent="f.submit()">
      <template v-if="f.tab.value !== 'forgot'">
        <div class="field">
          <label for="auth-username">账号</label>
          <input
            id="auth-username"
            v-model="f.form.username"
            name="username"
            autocomplete="username"
            placeholder="用户名"
          />
        </div>
      </template>

      <!-- 注册已迁到上面的两页式表单，这里的邮箱只剩"找回密码"一条用途 -->
      <template v-if="f.tab.value === 'forgot'">
        <div class="field">
          <label for="auth-email">邮箱</label>
          <input
            id="auth-email"
            v-model="f.form.email"
            name="email"
            type="email"
            autocomplete="email"
            placeholder="用于找回密码"
          />
          <p v-if="f.emailProblem.value" class="hint">{{ f.emailProblem.value }}</p>
        </div>
      </template>

      <template v-if="f.tab.value !== 'forgot'">
        <div class="field">
          <label for="auth-password">密码</label>
          <input
            id="auth-password"
            v-model="f.form.password"
            name="password"
            type="password"
            autocomplete="current-password"
            placeholder="至少 8 位"
          />
          <p v-if="f.passwordProblem.value" class="hint">{{ f.passwordProblem.value }}</p>
        </div>
      </template>

      <p v-if="f.failed.value" class="alert" role="alert">{{ f.failed.value }}</p>
      <p v-if="f.notice.value" class="ok" role="status">{{ f.notice.value }}</p>

      <div class="row">
        <button class="primary" type="submit" :disabled="!f.canSubmit.value || f.busy.value">
          <BrandIcon v-if="f.busy.value" name="load-spinner" decorative :size="15" />
          <span>{{ f.tab.value === 'login' ? '登录' : '发送重置链接' }}</span>
        </button>
      </div>
    </form>

    <div class="rule" aria-hidden="true" />

    <div class="alt">
      <button v-if="props.directEntry" class="ghost" type="button" @click="emit('direct')">
        直接进放映室
      </button>
      <button v-if="f.tab.value !== 'register'" class="ghost" type="button" @click="f.switchTab('register')">
        还没有账号？
      </button>
      <button v-else class="ghost" type="button" @click="f.switchTab('login')">已有账号？去登录</button>
    </div>

    <!-- 人机验证子窗体：页内模态（**不是** window.open —— 弹窗会被拦截）。
         它挂在 panel 最外层：position:fixed 使其成为独立的一层，不参与表单布局。
         **不套 <Transition>**：离场动画依赖 transitionend + 双 rAF，而后台/无头
         标签页会把 rAF 节流到十几秒 —— 那会让遮罩卡在 DOM 里继续挡住页面。
         入场动画由 .overlay / .dialog 自身的 animation 提供（一次性，不阻塞移除）。 -->
    <CaptchaDialog v-if="reg.captchaOpen.value" @pass="reg.onCaptchaPass()" @cancel="reg.onCaptchaCancel()" />
  </div>
</template>

<style scoped>
.panel {
  position: relative;
  padding: 22px;
  border: 1px solid var(--line);
  background: linear-gradient(180deg, rgba(180, 108, 255, 0.1), rgba(0, 0, 0, 0.45));
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.6);
}

/* 上下两条错位装饰线：RGB 分离感（纯装饰） */
.panel::before,
.panel::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  height: 3px;
  pointer-events: none;
}

.panel::before {
  top: 0;
  background: var(--accent);
  transform: translateX(-5px);
}

.panel::after {
  bottom: 0;
  background: var(--glitch-r);
  transform: translateX(5px);
}

.corner {
  position: absolute;
  width: 14px;
  height: 14px;
  border: 2px solid var(--accent);
  pointer-events: none;
}

.c-tl {
  top: -1px;
  left: -1px;
  border-right: 0;
  border-bottom: 0;
}

.c-tr {
  top: -1px;
  right: -1px;
  border-left: 0;
  border-bottom: 0;
}

.c-bl {
  bottom: -1px;
  left: -1px;
  border-right: 0;
  border-top: 0;
}

.c-br {
  bottom: -1px;
  right: -1px;
  border-left: 0;
  border-top: 0;
}

.tabs {
  display: flex;
  gap: 2px;
  margin-bottom: 16px;
}

.tabs button {
  flex: 1;
  padding: 8px 4px;
  font-size: 13px;
  color: var(--dim);
  border-bottom: 2px solid transparent;
  transition:
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease);
}

.tabs button:hover {
  color: var(--text);
}

.tabs button.on {
  color: var(--text);
  border-bottom-color: var(--accent);
}

input {
  margin-bottom: 4px;
}

/* 聚焦时分离描边（RGB 分离语言） */
input:focus {
  box-shadow:
    2px 0 0 var(--glitch-r),
    -2px 0 0 var(--accent);
}

/* 字段组：label + input + 提示，作为 grid 单元整体换列 */
.field {
  min-width: 0;
  margin-bottom: 4px;
}

/* ---------- 两页式注册 ---------- */

/* 步骤指示：两页式必须让人知道"我在第几步、还剩几步" */
.steps {
  margin: 0 0 14px;
  padding: 0;
  list-style: none;
  display: flex;
  gap: 14px;
  font-size: 12px;
  color: var(--dim);
}

.steps li {
  display: flex;
  align-items: center;
  gap: 6px;
}

.steps .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  border: 1px solid var(--line);
  background: transparent;
}

/* 当前步：实心 + 主色；已完成：对勾色。
   状态不靠颜色单独表达 —— 当前步还有字重与 aria 上的 on 类。 */
.steps li.on {
  color: var(--text);
  font-weight: 700;
}

.steps li.on .dot {
  background: var(--accent);
  border-color: var(--accent);
}

.steps li.done .dot {
  background: var(--ok);
  border-color: var(--ok);
}

.steps li.done {
  color: var(--ok);
}

.reg .row {
  flex-wrap: wrap;
}

/* ---------- 动效（只动 transform / opacity；reduced-motion 由 tokens.css 全局判死） ---------- */

/* 进度条：按步填充。它是这一屏唯一"常驻"的视觉动效，其余都是一次性的进出场。 */
.bar-track {
  position: relative;
  height: 2px;
  margin: 0 0 16px;
  background: var(--line);
  overflow: hidden;
}

.bar-fill {
  position: absolute;
  inset: 0;
  background: linear-gradient(90deg, var(--accent), var(--glitch-p));
  transform: scaleX(0.5);
  transform-origin: left center;
  transition: transform var(--dur-morph) var(--ease);
}

.bar-fill.full {
  transform: scaleX(1);
}

/* 当前步的圆点轻微呼吸：提示"停在这一步"。用 transform 而不是 box-shadow，避免逐帧重绘。 */
.steps li.on .dot {
  animation: dot-breathe 1800ms var(--ease) infinite;
}

@keyframes dot-breathe {
  0%,
  100% {
    transform: scale(1);
  }
  50% {
    transform: scale(1.45);
  }
}

/* 翻页过渡：新页**自带**入场动画（一次性，非循环）。
   不用 <Transition mode="out-in"> 的原因见模板里的注释：那种模式依赖离场完成，
   而离场由双 rAF 驱动，后台/无头标签页会被节流 → 旧页不离开、新页不出现、表单卡死。 */
.pane {
  animation: pane-in var(--dur-comp) var(--ease);
}

@keyframes pane-in {
  from {
    transform: translateX(16px);
    opacity: 0;
  }
  to {
    transform: none;
    opacity: 1;
  }
}

/* 淡入 / 上升：改成**元素自带的初始 animation**，而不是 Vue 的 <Transition>。
   理由见模板注释：Transition 的离场要等 transitionend + 双 rAF，
   而 rAF 在后台/无头标签页会被节流到十几秒，元素会卡住不消失。
   自带 animation 只在挂载时播一次，移除是同步的 —— 没有任何"等不到事件"的风险。 */
@keyframes fade-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

@keyframes rise-in {
  from {
    transform: translateY(10px);
    opacity: 0;
  }
  to {
    transform: none;
    opacity: 1;
  }
}

.code-block {
  animation: rise-in var(--dur-comp) var(--ease);
}

/* 错误条"抖一下再出现"：故障语言里最常见的反馈，且与子窗体里的点错抖动同源 */
.alert {
  animation: alert-in 280ms var(--ease);
}

@keyframes alert-in {
  0% {
    transform: translateX(-6px);
    opacity: 0;
  }
  40% {
    transform: translateX(3px);
    opacity: 1;
  }
  70% {
    transform: translateX(-2px);
  }
  100% {
    transform: none;
  }
}

/* 输入聚焦：轻微前移（与既有的 RGB 分离描边叠加，不替代它） */
input:not(:disabled) {
  transition:
    transform var(--dur-micro) var(--ease),
    box-shadow var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease);
}

input:not(:disabled):focus {
  transform: translateY(-1px);
}

/* 主按钮的悬停/按下微动在既有 `.primary` 规则里（见下方），此处不重复声明。 */

/* 本机开发的验证码回显：视觉上必须一眼看出它"不是给用户看的正式信息" */
.dev-hint {
  margin: 0;
  padding: 7px 10px;
  border: 1px dashed var(--line-strong);
  font-size: 12px;
  color: var(--glitch-p);
}

.dev-hint .mono {
  font-family: var(--font-mono);
  letter-spacing: 0.12em;
}

/* 注册两列排布：4 个字段 2×2，避免单列过长撑出滚动条 */
form.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  column-gap: 14px;
  align-items: start;
}

form.two-col .alert,
form.two-col .ok,
form.two-col .row {
  grid-column: 1 / -1;
}

.hint {
  margin: 2px 0 8px;
  font-size: 12px;
  color: var(--glitch-p);
}

.alert {
  margin: 10px 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}

.ok {
  margin: 10px 0;
  padding: 8px 10px;
  border: 1px solid var(--ok);
  background: rgba(61, 220, 151, 0.1);
  font-size: 12px;
  color: var(--ok);
  animation: fade-in var(--dur-comp) var(--ease);
}

.row {
  display: flex;
  gap: 10px;
  margin-top: 16px;
}

.primary {
  flex: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 11px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  transition:
    background var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

.primary:hover:not(:disabled) {
  background: var(--accent-hover);
  /* 抬起 1px：纯 transform，不触发布局；与"故障"语言一致的是位移而非阴影 */
  transform: translateY(-1px);
}

.primary:active:not(:disabled) {
  transform: scale(0.98);
}

.primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.primary :deep(.brand-icon) {
  animation: spin 900ms linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

/* 会抖的故障分割线（常驻） */
.rule {
  height: 1px;
  margin: 16px 0 12px;
  background: var(--line);
  animation: rule-glitch 3600ms steps(3, end) infinite;
}

@keyframes rule-glitch {
  0%,
  88% {
    transform: none;
    opacity: 1;
  }
  90% {
    transform: translateX(-4px) scaleY(2);
    opacity: 0.6;
  }
  93%,
  100% {
    transform: none;
    opacity: 1;
  }
}

.alt {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}

.ghost {
  flex: 1;
  min-width: 120px;
  padding: 10px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
  text-align: center;
  text-decoration: none;
  transition:
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

.ghost:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}

.ghost:active {
  transform: scale(0.98);
}
</style>
