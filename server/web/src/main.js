import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Domains from './views/Domains.vue'
import DomainEdit from './views/DomainEdit.vue'
import Clusters from './views/Clusters.vue'
import ClusterEdit from './views/ClusterEdit.vue'
import Status from './views/Status.vue'
import History from './views/History.vue'
import AuditLog from './views/AuditLog.vue'
import Grafana from './views/Grafana.vue'
import Credentials from './views/Credentials.vue'
import Members from './views/Members.vue'
import Login from './views/Login.vue'
import AuthCallback from './views/AuthCallback.vue'
import { getAuthConfig, getToken, isTokenExpired, refreshAccessToken } from './api.js'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/domains' },
    { path: '/login', component: Login, meta: { public: true } },
    { path: '/auth/callback', component: AuthCallback, meta: { public: true } },
    { path: '/domains', component: Domains },
    { path: '/domains/new', component: DomainEdit },
    { path: '/domains/:name/edit', component: DomainEdit },
    { path: '/domains/:name/history', component: History, props: () => ({ kind: 'domain' }) },
    { path: '/clusters', component: Clusters },
    { path: '/clusters/new', component: ClusterEdit },
    { path: '/clusters/:name/edit', component: ClusterEdit },
    { path: '/clusters/:name/history', component: History, props: () => ({ kind: 'cluster' }) },
    { path: '/status', component: Status },
    { path: '/audit', component: AuditLog },
    { path: '/monitoring', component: Grafana },
    { path: '/credentials', component: Credentials },
    { path: '/members', component: Members },
  ]
})

// Navigation guard: redirect to /login if OIDC is enabled and no valid token.
let _authConfigCache = null
router.beforeEach(async (to) => {
  if (to.meta.public) return true

  // Fetch auth config once.
  if (!_authConfigCache) {
    try {
      _authConfigCache = await getAuthConfig()
    } catch {
      // If we can't reach the server, let the request through.
      return true
    }
  }

  if (!_authConfigCache.enabled) return true

  // Check if we have a valid token.
  const token = getToken()
  if (!token) return '/login'

  if (isTokenExpired()) {
    const ok = await refreshAccessToken()
    if (!ok) return '/login'
  }

  return true
})

createApp(App).use(router).mount('#app')
