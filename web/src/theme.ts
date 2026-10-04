import { ref } from 'vue'

// 当前主题：CSS 变量文件由 Go 后端静态服务（/themes/{id}/variables.css）。
// themeState 是响应式的，让 Naive UI 的暗色组件主题跟随切换。
export const themeState = ref(localStorage.getItem('theme') || 'default')

export function applyTheme(id: string) {
  const safe = /^[a-zA-Z0-9_-]+$/.test(id) ? id : 'default'
  const link = document.getElementById('theme-css') as HTMLLinkElement | null
  if (link) link.href = `/themes/${safe}/variables.css`
  localStorage.setItem('theme', safe)
  themeState.value = safe
}
export function currentTheme() { return themeState.value }
