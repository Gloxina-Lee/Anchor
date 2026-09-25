<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'

const stage = ref('loading')
const page = ref(location.pathname.startsWith('/admin/settings') ? 'settings' : 'links')
const theme = ref(localStorage.getItem('anchor-theme') === 'dark' ? 'dark' : 'light')
const csrf = ref('')
const username = ref('')
const credentials = reactive({ username: '', password: '' })
const links = ref([])
const settings = reactive({ minLength: 6, maxLength: 32, excludeSimilar: false, reuseCodes: true, rootBehavior: 'admin', rootRedirectUrl: '', rootHtml: '' })
const draft = reactive({ destination: '', code: '', note: '', startChoice: 'now', startCustom: '', expiryChoice: 'forever', expiryCustom: '' })
const selected = ref([])
const search = ref('')
const statusFilter = ref('all')
const dialog = ref(null)
const busy = ref(false)
const notice = ref(null)
const clock = ref(Date.now())
let ticker
let noticeTimer
let dialogReturnFocus

const filteredLinks = computed(() => links.value.filter(link => {
  const query = search.value.trim().toLowerCase()
  const matchesQuery = !query || link.code.toLowerCase().includes(query) || link.destination.toLowerCase().includes(query) || link.note?.toLowerCase().includes(query)
  return matchesQuery && (statusFilter.value === 'all' || statusOf(link) === statusFilter.value)
}))
const allSelected = computed(() => filteredLinks.value.length > 0 && filteredLinks.value.every(link => selected.value.includes(link.id)))
const counts = computed(() => {
  clock.value
  return {
    all: links.value.length,
    active: links.value.filter(link => statusOf(link) === 'active').length,
    scheduled: links.value.filter(link => statusOf(link) === 'scheduled').length,
    expired: links.value.filter(link => statusOf(link) === 'expired').length,
  }
})

onMounted(() => {
  applyTheme()
  window.addEventListener('popstate', syncPage)
  window.addEventListener('keydown', handleDialogKey)
  ticker = window.setInterval(() => { clock.value = Date.now() }, 30_000)
  refreshSession()
})
onUnmounted(() => {
  window.removeEventListener('popstate', syncPage)
  window.removeEventListener('keydown', handleDialogKey)
  window.clearInterval(ticker)
  window.clearTimeout(noticeTimer)
})

function syncPage() {
  page.value = location.pathname.startsWith('/admin/settings') ? 'settings' : 'links'
}
function navigate(next) {
  page.value = next
  history.pushState({}, '', next === 'settings' ? '/admin/settings' : '/admin/')
  selected.value = []
}
function applyTheme() {
  document.documentElement.dataset.theme = theme.value
}
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem('anchor-theme', theme.value)
  applyTheme()
}
function flash(text, type = 'success') {
  notice.value = { text, type }
  window.clearTimeout(noticeTimer)
  noticeTimer = window.setTimeout(() => { notice.value = null }, 4500)
}
async function request(path, method = 'GET', data) {
  const headers = { Accept: 'application/json' }
  if (data !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && csrf.value) headers['X-CSRF-Token'] = csrf.value
  const response = await fetch(path, { method, headers, credentials: 'same-origin', body: data === undefined ? undefined : JSON.stringify(data) })
  const payload = response.status === 204 ? null : await response.json().catch(() => null)
  if (!response.ok) {
    if (response.status === 401 && path !== '/auth/login') stage.value = 'login'
    throw new Error(payload?.error || `请求失败 (${response.status})`)
  }
  return payload
}
async function refreshSession() {
  try {
    const state = await request('/auth/status')
    if (!state.initialized) {
      stage.value = 'setup'
    } else if (!state.authenticated) {
      stage.value = 'login'
    } else {
      csrf.value = state.csrf
      username.value = state.username
      await loadData()
    }
  } catch (error) {
    stage.value = 'error'
    flash(error.message, 'error')
  }
}
async function loadData() {
  const [linkList, savedSettings] = await Promise.all([request('/api/links'), request('/api/settings')])
  clock.value = Date.now()
  links.value = linkList
  Object.assign(settings, savedSettings)
  stage.value = 'ready'
}
async function submitCredentials() {
  if (busy.value) return
  busy.value = true
  try {
    const action = stage.value === 'setup' ? 'setup' : 'login'
    const result = await request(`/auth/${action}`, 'POST', credentials)
    csrf.value = result.csrf
    credentials.password = ''
    await refreshSession()
    flash(action === 'setup' ? '初始化完成，欢迎使用 Anchor' : '欢迎回来')
  } catch (error) {
    flash(error.message, 'error')
  } finally {
    busy.value = false
  }
}
async function logout() {
  try {
    await request('/auth/logout', 'POST')
    csrf.value = ''
    links.value = []
    selected.value = []
    stage.value = 'login'
    navigate('links')
  } catch (error) {
    flash(error.message, 'error')
  }
}
function addCalendarMonth(source) {
  const date = new Date(source)
  const day = date.getDate()
  date.setDate(1)
  date.setMonth(date.getMonth() + 1)
  const last = new Date(date.getFullYear(), date.getMonth() + 1, 0).getDate()
  date.setDate(Math.min(day, last))
  return date
}
function addPeriod(source, choice) {
  if (choice === '1m') return addCalendarMonth(source)
  const hours = { '1h': 1, '1d': 24, '1w': 168 }[choice]
  return new Date(source.getTime() + hours * 3600_000)
}
function parseCustom(value, label) {
  const date = new Date(value)
  if (!value || Number.isNaN(date.getTime())) throw new Error(`请指定有效的${label}`)
  return date
}
function schedule() {
  const start = draft.startChoice === 'now' ? new Date() : draft.startChoice === 'custom'
    ? parseCustom(draft.startCustom, '生效时间') : addPeriod(new Date(), draft.startChoice)
  const end = draft.expiryChoice === 'forever' ? null : draft.expiryChoice === 'custom'
    ? parseCustom(draft.expiryCustom, '到期时间') : addPeriod(start, draft.expiryChoice)
  if (end && end <= start) throw new Error('到期时间必须晚于生效时间')
  return { startsAt: start.toISOString(), expiresAt: end?.toISOString() ?? null }
}
async function createLink() {
  if (busy.value) return
  busy.value = true
  try {
    const timing = schedule()
    const created = await request('/api/links', 'POST', {
      destination: draft.destination.trim(), code: draft.code.trim(), note: draft.note.trim(), ...timing,
    })
    draft.destination = ''
    draft.code = ''
    draft.note = ''
    draft.startChoice = 'now'
    draft.expiryChoice = 'forever'
    draft.startCustom = ''
    draft.expiryCustom = ''
    closeDialog()
    await loadData()
    flash(`已创建 /${created.code}`)
  } catch (error) {
    flash(error.message, 'error')
  } finally {
    busy.value = false
  }
}
function statusOf(link) {
  if (link.expiresAt && new Date(link.expiresAt).getTime() <= clock.value) return 'expired'
  if (new Date(link.startsAt).getTime() > clock.value) return 'scheduled'
  return 'active'
}
function statusLabel(status) {
  return { active: '生效中', scheduled: '待生效', expired: '已到期' }[status]
}
function formatDate(value) {
  return new Date(value).toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}
function localInput(value) {
  const date = new Date(value)
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}
function linkURL(code) {
  return `${location.origin}/${code}`
}
async function copyLink(code) {
  try {
    await navigator.clipboard.writeText(linkURL(code))
    flash('短链接已复制')
  } catch {
    flash('复制失败，请手动复制地址', 'error')
  }
}
function toggleAll(event) {
  const visible = filteredLinks.value.map(link => link.id)
  selected.value = event.target.checked
    ? [...new Set([...selected.value, ...visible])]
    : selected.value.filter(id => !visible.includes(id))
}
function toggleOne(id) {
  selected.value = selected.value.includes(id) ? selected.value.filter(value => value !== id) : [...selected.value, id]
}
function openDialog(type, ids = [], existingExpiry = null, existingNote = '') {
  dialogReturnFocus = document.activeElement
  dialog.value = {
    type, ids, expiryMode: existingExpiry ? 'custom' : 'forever',
    expiryValue: existingExpiry ? localInput(existingExpiry) : '',
    noteValue: existingNote,
  }
  nextTick(() => {
    const selector = type === 'create' ? '#create-destination' : type === 'note' ? '#edit-note' : '.modal .modal-actions button'
    document.querySelector(selector)?.focus()
  })
}
function closeDialog() {
  dialog.value = null
  nextTick(() => dialogReturnFocus?.focus?.())
}
function handleDialogKey(event) {
  if (!dialog.value) return
  if (event.key === 'Escape') {
    closeDialog()
    return
  }
  if (event.key !== 'Tab') return
  const controls = [...document.querySelectorAll('.modal button:not(:disabled), .modal input, .modal select, .modal textarea')]
  if (!controls.length) return
  const first = controls[0]
  const last = controls[controls.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
async function commitDialog() {
  if (!dialog.value || busy.value) return
  busy.value = true
  try {
    const { type, ids, expiryMode, expiryValue, noteValue } = dialog.value
    if (type === 'delete') {
      if (ids.length === 1) await request(`/api/links/${ids[0]}`, 'DELETE')
      else await request('/api/links/batch', 'POST', { action: 'delete', ids })
      flash(`已删除 ${ids.length} 条短链接`)
    } else if (type === 'note') {
      await request(`/api/links/${ids[0]}`, 'PATCH', { note: noteValue })
      flash('备注已保存')
    } else {
      const expiry = expiryMode === 'forever' ? null : parseCustom(expiryValue, '到期时间').toISOString()
      if (ids.length === 1) await request(`/api/links/${ids[0]}`, 'PATCH', { expiresAt: expiry })
      else await request('/api/links/batch', 'POST', { action: 'expiry', ids, expiresAt: expiry })
      flash(`已更新 ${ids.length} 条短链接`)
    }
    selected.value = []
    closeDialog()
    await loadData()
  } catch (error) {
    flash(error.message, 'error')
  } finally {
    busy.value = false
  }
}
async function saveSettings() {
  if (busy.value) return
  busy.value = true
  try {
    const saved = await request('/api/settings', 'PUT', {
      minLength: Number(settings.minLength), maxLength: Number(settings.maxLength),
      excludeSimilar: settings.excludeSimilar, reuseCodes: settings.reuseCodes,
      rootBehavior: settings.rootBehavior, rootRedirectUrl: settings.rootRedirectUrl, rootHtml: settings.rootHtml,
    })
    Object.assign(settings, saved)
    flash('设置已保存')
  } catch (error) {
    flash(error.message, 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="stage === 'loading'" class="boot-screen" role="status">
    <span class="boot-symbol">A</span><span class="spinner"></span><p>正在载入 Anchor…</p>
  </div>

  <div v-else-if="stage === 'error'" class="boot-screen" role="alert">
    <span class="boot-symbol">A</span><h1>暂时无法连接服务</h1><p>请确认服务已启动，然后重试。</p>
    <button class="button button-primary" type="button" @click="stage = 'loading'; refreshSession()">重试</button>
  </div>

  <div v-else-if="stage === 'setup' || stage === 'login'" class="auth-layout">
    <section class="auth-story">
      <div class="auth-brand"><span class="brand-mark">A</span><span>Anchor<span class="brand-dot">.</span></span></div>
      <div class="auth-story-copy">
        <span class="eyebrow">YOUR LINKS, IN ONE PLACE</span>
        <h1>让每一个链接，<br /><em>都有清晰的去处。</em></h1>
        <p>创建、安排与管理你的短链接。简单的入口，完整的掌控。</p>
      </div>
      <div class="auth-story-bottom"><span class="story-line"></span><span>PRIVATE LINK MANAGEMENT</span></div>
    </section>
    <main class="auth-form-wrap">
      <button class="theme-button auth-theme" type="button" :aria-label="theme === 'light' ? '切换到深色模式' : '切换到浅色模式'" @click="toggleTheme">{{ theme === 'light' ? '◐' : '◑' }}</button>
      <form class="auth-card" @submit.prevent="submitCredentials">
        <span class="eyebrow">{{ stage === 'setup' ? 'GET STARTED' : 'WELCOME BACK' }}</span>
        <h2>{{ stage === 'setup' ? '初始化 Anchor' : '欢迎回来' }}</h2>
        <p class="auth-intro">{{ stage === 'setup' ? '创建唯一的管理员账户，开始管理短链接。' : '登录后继续管理你的短链接。' }}</p>
        <label class="field"><span>用户名</span><input v-model="credentials.username" autocomplete="username" required :minlength="stage === 'setup' ? 3 : undefined" maxlength="64" placeholder="输入用户名" /></label>
        <label class="field"><span>密码</span><input v-model="credentials.password" type="password" :autocomplete="stage === 'setup' ? 'new-password' : 'current-password'" required :minlength="stage === 'setup' ? 8 : undefined" placeholder="输入密码" /></label>
        <p v-if="stage === 'setup'" class="field-hint">密码至少 8 个字符。初始化后此入口将关闭。</p>
        <button class="button button-primary auth-submit" type="submit" :disabled="busy">{{ busy ? '请稍候…' : stage === 'setup' ? '创建管理员账户' : '登录管理面板' }} <span aria-hidden="true">→</span></button>
      </form>
      <p class="auth-foot">Anchor · 私有短链接服务</p>
    </main>
  </div>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="sidebar-top">
        <div class="sidebar-brand"><span class="brand-mark">A</span><span>Anchor<span class="brand-dot">.</span></span></div>
        <p class="sidebar-caption">LINK CONTROL</p>
        <nav class="side-nav" aria-label="管理导航">
          <a href="/admin/" :class="{ active: page === 'links' }" :aria-current="page === 'links' ? 'page' : undefined" @click.prevent="navigate('links')"><span class="nav-glyph">↗</span> 短链接管理</a>
          <a href="/admin/settings" :class="{ active: page === 'settings' }" :aria-current="page === 'settings' ? 'page' : undefined" @click.prevent="navigate('settings')"><span class="nav-glyph">⚙</span> 设置</a>
        </nav>
      </div>
      <div class="sidebar-bottom"><span class="online-dot"></span> 服务已连接 <span class="sidebar-version">ANCHOR</span></div>
    </aside>

    <div class="main-wrap">
      <header class="topbar">
        <div class="breadcrumb">工作台 <span>/</span> <strong>{{ page === 'links' ? '短链接管理' : '设置' }}</strong></div>
        <div class="top-actions"><button class="theme-button" type="button" :aria-label="theme === 'light' ? '切换到深色模式' : '切换到浅色模式'" @click="toggleTheme">{{ theme === 'light' ? '◐' : '◑' }}</button><span class="top-divider"></span><span class="user-avatar">{{ username.slice(0, 1).toUpperCase() }}</span><span class="user-name">{{ username }}</span><button class="text-button" type="button" @click="logout">退出</button></div>
      </header>

      <main v-if="page === 'links'" class="page-content">
        <header class="page-heading"><div><span class="eyebrow">OVERVIEW / LINKS</span><h1>短链接管理<span class="heading-dot">.</span></h1><p>从这里创建、安排并整理每一条链接。</p></div><button class="button button-primary heading-action" type="button" @click="openDialog('create')"><span aria-hidden="true">＋</span> 创建短链接</button></header>
        <section class="stats-grid" aria-label="链接概览">
          <button class="stat-card" :class="{ selected: statusFilter === 'all' }" type="button" @click="statusFilter = 'all'"><span class="stat-label">全部链接</span><strong>{{ counts.all }}</strong><span class="stat-foot">所有已创建的短链接</span></button>
          <button class="stat-card" :class="{ selected: statusFilter === 'active' }" type="button" @click="statusFilter = 'active'"><span class="stat-label"><span class="status-pin active"></span> 生效中</span><strong>{{ counts.active }}</strong><span class="stat-foot">现在可以访问</span></button>
          <button class="stat-card" :class="{ selected: statusFilter === 'scheduled' }" type="button" @click="statusFilter = 'scheduled'"><span class="stat-label"><span class="status-pin scheduled"></span> 待生效</span><strong>{{ counts.scheduled }}</strong><span class="stat-foot">等待设定的时间</span></button>
          <button class="stat-card" :class="{ selected: statusFilter === 'expired' }" type="button" @click="statusFilter = 'expired'"><span class="stat-label"><span class="status-pin expired"></span> 已到期</span><strong>{{ counts.expired }}</strong><span class="stat-foot">目前无法访问</span></button>
        </section>

        <div class="workspace-grid">
          <section class="surface list-surface" aria-labelledby="list-title">
            <div class="section-head"><div><span class="section-kicker">YOUR COLLECTION</span><h2 id="list-title">链接列表 <span class="count-chip">{{ filteredLinks.length }}</span></h2></div></div>
            <div class="list-toolbar"><label class="search-field"><span aria-hidden="true">⌕</span><input v-model="search" type="search" aria-label="搜索短码、目标网址或备注" placeholder="搜索短码、目标网址或备注…" /></label><select v-model="statusFilter" aria-label="按状态筛选"><option value="all">全部状态</option><option value="active">生效中</option><option value="scheduled">待生效</option><option value="expired">已到期</option></select></div>
            <div v-if="selected.length" class="bulk-bar"><span>已选择 <strong>{{ selected.length }}</strong> 条</span><div><button type="button" class="button button-quiet" @click="openDialog('expiry', [...selected])">更改到期时间</button><button type="button" class="button button-danger" @click="openDialog('delete', [...selected])">删除</button></div></div>
            <div v-if="filteredLinks.length" class="link-list"><div class="list-select-all"><label><input type="checkbox" :checked="allSelected" @change="toggleAll" /> 选择当前列表</label><span>共 {{ filteredLinks.length }} 条</span></div>
              <article v-for="link in filteredLinks" :key="link.id" class="link-row">
                <input class="row-check" type="checkbox" :aria-label="`选择 ${link.code}`" :checked="selected.includes(link.id)" @change="toggleOne(link.id)" />
                <div class="link-content"><div class="link-line"><a class="short-url" :href="linkURL(link.code)" :title="linkURL(link.code)" target="_blank" rel="noopener noreferrer">{{ link.code }}</a><span class="status-pill" :class="statusOf(link)">{{ statusLabel(statusOf(link)) }}</span></div><p v-if="link.note" class="link-note">{{ link.note }}</p><a class="destination" :href="link.destination" target="_blank" rel="noopener noreferrer" :title="link.destination">↳ {{ link.destination }}</a><div class="link-meta"><span>创建于 {{ formatDate(link.createdAt) }}</span><span>生效 {{ formatDate(link.startsAt) }}</span><span>到期 {{ link.expiresAt ? formatDate(link.expiresAt) : '永久' }}</span></div></div>
                <div class="row-actions"><button type="button" aria-label="复制短链接" title="复制完整短链接" @click="copyLink(link.code)">⧉</button><button type="button" :aria-label="link.note ? '编辑备注' : '添加备注'" :title="link.note ? '编辑备注' : '添加备注'" @click="openDialog('note', [link.id], null, link.note)">✎</button><button type="button" aria-label="更改到期时间" title="更改到期时间" @click="openDialog('expiry', [link.id], link.expiresAt)">◷</button><button type="button" class="delete-action" aria-label="删除短链接" title="删除短链接" @click="openDialog('delete', [link.id])">×</button></div>
              </article>
            </div>
            <div v-else class="empty-state"><span class="empty-symbol">↗</span><h3>{{ search || statusFilter !== 'all' ? '没有匹配的链接' : '从第一条短链接开始' }}</h3><p>{{ search || statusFilter !== 'all' ? '试试调整搜索词或筛选条件。' : '点击上方的“创建短链接”按钮开始。' }}</p></div>
          </section>

        </div>
      </main>

      <main v-else class="page-content settings-page">
        <header class="page-heading"><div><span class="eyebrow">PREFERENCES / SETTINGS</span><h1>设置<span class="heading-dot">.</span></h1><p>调整根路径、短码生成与过期短码的处理方式。</p></div></header>
        <form class="settings-layout" @submit.prevent="saveSettings">
          <section class="surface settings-section">
            <div class="section-head"><div><span class="section-kicker">ROOT PATH</span><h2>根路径</h2></div></div>
            <p class="section-description">指定访问本站根路径 / 时的响应，不影响 /admin/ 管理页面和短链接。</p>
            <label class="field"><span>访问根路径时</span>
              <select v-model="settings.rootBehavior">
                <option value="admin">跳转至 /admin/</option>
                <option value="notFound">返回 404</option>
                <option value="redirect">跳转至其他 URL</option>
                <option value="html">返回指定的 HTML 页面</option>
              </select>
            </label>
            <div v-if="settings.rootBehavior === 'redirect'" class="root-detail">
              <label class="field"><span>跳转目标</span><input v-model="settings.rootRedirectUrl" type="text" placeholder="例如 example.org 或 https://example.org/" required /></label>
              <p class="field-hint">可输入完整的 HTTP/HTTPS 网址；只输入域名时自动使用 HTTPS。</p>
            </div>
            <div v-if="settings.rootBehavior === 'html'" class="root-detail">
              <label class="field"><span>HTML 页面内容</span><textarea v-model="settings.rootHtml" class="html-editor" spellcheck="false" placeholder="<!doctype html>&#10;<html lang=&#34;zh-CN&#34;>..." required></textarea></label>
              <p class="field-hint">填写完整的 HTML 页面，最多 256 KB。页面以独立来源运行，无法读取管理页面数据。</p>
            </div>
          </section>
          <section class="surface settings-section"><div class="section-head"><div><span class="section-kicker">SHORT CODES</span><h2>短码生成</h2></div></div><p class="section-description">随机短码从最小长度开始。发生冲突时会重试，并在必要时增加长度。</p><div class="length-grid"><label class="field"><span>最小长度</span><input v-model.number="settings.minLength" type="number" min="1" max="128" required /></label><label class="field"><span>最大长度</span><input v-model.number="settings.maxLength" type="number" min="1" max="128" required /></label></div><p class="field-hint">默认 6–32 位。手动短码最少 1 位，最多为这里设置的最大长度。</p><label class="switch-row"><span><strong>排除相近字符</strong><small>随机生成时避开 0/O、1/i/l 等易混淆字符。</small></span><input v-model="settings.excludeSimilar" type="checkbox" role="switch" /></label></section>
          <section class="surface settings-section"><div class="section-head"><div><span class="section-kicker">REUSE POLICY</span><h2>短码复用</h2></div></div><p class="section-description">决定到期或删除后的短码是否可以被新链接再次使用。</p><label class="switch-row"><span><strong>允许重新分配短码</strong><small>开启后，旧链接可能在未来指向新的目标网址。</small></span><input v-model="settings.reuseCodes" type="checkbox" role="switch" /></label></section>
          <div class="settings-actions"><button class="button button-primary" type="submit" :disabled="busy">{{ busy ? '正在保存…' : '保存设置' }}</button></div>
        </form>
      </main>
    </div>
  </div>

  <div v-if="dialog" class="modal-backdrop" @click.self="closeDialog">
    <section class="modal" :class="{ 'modal-create': dialog.type === 'create' }" role="dialog" aria-modal="true" :aria-label="{ create: '创建短链接', delete: '删除短链接', note: '编辑备注', expiry: '更改到期时间' }[dialog.type]">
      <template v-if="dialog.type === 'create'">
        <span class="section-kicker">NEW LINK</span>
        <h2>创建短链接</h2>
        <p>设置目标网址、备注与访问时间。</p>
        <form class="create-form" @submit.prevent="createLink">
          <label class="field"><span>目标网址 <b>*</b></span><input id="create-destination" v-model="draft.destination" type="url" required placeholder="https://example.com/article" /></label>
          <label class="field"><span>自定义短码 <small>可选</small></span><div class="code-input"><span>/</span><input v-model="draft.code" type="text" :maxlength="settings.maxLength" pattern="[A-Za-z0-9]+" placeholder="留空则自动生成" /></div></label>
          <div class="field-hint">支持字母与数字，最长 {{ settings.maxLength }} 位。</div>
          <label class="field"><span>备注 <small>可选</small></span><textarea v-model="draft.note" maxlength="500" rows="3" placeholder="记下这条链接的用途…"></textarea></label>
          <div class="form-divider"></div>
          <label class="field"><span>生效时间</span><select v-model="draft.startChoice"><option value="now">立即</option><option value="1h">1 小时后</option><option value="1d">1 天后</option><option value="1w">1 周后</option><option value="1m">1 月后</option><option value="custom">手动指定</option></select></label>
          <label v-if="draft.startChoice === 'custom'" class="field sub-field"><span>选择生效日期和时间</span><input v-model="draft.startCustom" type="datetime-local" required /></label>
          <label class="field"><span>到期时间</span><select v-model="draft.expiryChoice"><option value="forever">永久</option><option value="1h">生效后 1 小时</option><option value="1d">生效后 1 天</option><option value="1w">生效后 1 周</option><option value="1m">生效后 1 月</option><option value="custom">手动指定</option></select></label>
          <label v-if="draft.expiryChoice === 'custom'" class="field sub-field"><span>选择到期日期和时间</span><input v-model="draft.expiryCustom" type="datetime-local" required /></label>
          <div class="modal-actions"><button class="button button-secondary" type="button" @click="closeDialog">取消</button><button class="button button-primary" type="submit" :disabled="busy">{{ busy ? '正在创建…' : '创建短链接' }}</button></div>
        </form>
      </template>
      <template v-else>
        <span class="section-kicker">{{ dialog.type === 'delete' ? 'CONFIRM ACTION' : dialog.type === 'note' ? 'LINK NOTE' : 'UPDATE EXPIRY' }}</span>
        <h2>{{ dialog.type === 'delete' ? '删除短链接？' : dialog.type === 'note' ? '编辑备注' : '更改到期时间' }}</h2>
        <p v-if="dialog.type === 'delete'">将删除所选的 {{ dialog.ids.length }} 条短链接。访问这些地址会返回 404。</p>
        <template v-else-if="dialog.type === 'note'">
          <p>为这条短链接记录用途，最多 500 个字符。</p>
          <label class="field"><span>备注</span><textarea id="edit-note" v-model="dialog.noteValue" maxlength="500" rows="5" placeholder="例如：发给朋友的活动页面"></textarea></label>
        </template>
        <template v-else>
          <p>为所选的 {{ dialog.ids.length }} 条短链接设置新的到期时间。</p>
          <label class="field"><span>到期方式</span><select v-model="dialog.expiryMode"><option value="forever">永久</option><option value="custom">指定日期和时间</option></select></label>
          <label v-if="dialog.expiryMode === 'custom'" class="field"><span>到期日期和时间</span><input v-model="dialog.expiryValue" type="datetime-local" /></label>
        </template>
        <div class="modal-actions"><button class="button button-secondary" type="button" @click="closeDialog">取消</button><button class="button" :class="dialog.type === 'delete' ? 'button-danger' : 'button-primary'" type="button" :disabled="busy" @click="commitDialog">{{ busy ? '处理中…' : dialog.type === 'delete' ? '确认删除' : '保存更改' }}</button></div>
      </template>
    </section>
  </div>
  <div v-if="notice" class="toast" :class="notice.type" role="status">{{ notice.text }}</div>
</template>
