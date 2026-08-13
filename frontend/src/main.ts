import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import { createPinia } from 'pinia'
import { createApp } from 'vue'

import App from './App.vue'
import { registerSessionExpiredHandler } from './api/http'
import router from './router'
import { useAuthStore } from './stores/auth'

// Element Plus 全量样式(开发期省心;后续可按需引入优化体积)
import 'element-plus/dist/index.css'
import './styles/main.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
app.use(ElementPlus, { locale: zhCn })

// 会话过期回调:http.ts 401 刷新失败时触发(替代 location.href 硬跳)
// → store 清凭证 + 路由跳登录(带 redirect 回跳)
registerSessionExpiredHandler(() => {
  const auth = useAuthStore(pinia)
  auth.expireSession()
  if (router.currentRoute.value.name !== 'login') {
    router.push({
      name: 'login',
      query: { redirect: router.currentRoute.value.fullPath },
    })
  }
})

app.mount('#app')
