import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './style.css'
import { siteName } from './branding'
import { applyTheme, currentTheme } from './theme'

applyTheme(currentTheme())
document.title = siteName
createApp(App).use(createPinia()).use(router).mount('#app')
