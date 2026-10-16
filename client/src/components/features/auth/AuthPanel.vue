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
import BrandIcon from '../../ui/BrandIcon.vue'
import { useAuthForm, type AuthTab } from './useAuthForm'

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

    <form :class="{ 'two-col': f.tab.value === 'register' }" @submit.prevent="f.submit()">
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

      <template v-if="f.tab.value === 'register'">
        <div class="field">
          <label for="auth-display">昵称</label>
          <input
            id="auth-display"
            v-model="f.form.displayName"
            name="displayName"
            autocomplete="nickname"
            placeholder="留空则用账号名"
          />
        </div>
      </template>

      <template v-if="f.tab.value === 'register' || f.tab.value === 'forgot'">
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
            :autocomplete="f.tab.value === 'register' ? 'new-password' : 'current-password'"
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
          <span>{{ f.tab.value === 'login' ? '登录' : f.tab.value === 'register' ? '注册' : '发送重置链接' }}</span>
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
