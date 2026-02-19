import axios from 'axios'

const api = axios.create({ baseURL: '/api/v1' })

// ─── Namespace ───────────────────────────────────────────────────────

let currentNamespace = localStorage.getItem('hermes_namespace') || 'default'

export function setNamespace(ns) {
  currentNamespace = ns
  localStorage.setItem('hermes_namespace', ns)
}

export function getNamespace() {
  return currentNamespace
}

// ─── Auth ─────────────────────────────────────────────────────────────

let _authConfig = null

export async function getAuthConfig() {
  if (_authConfig) return _authConfig
  const res = await axios.get('/api/auth/config')
  _authConfig = res.data
  return _authConfig
}

// Reset cached auth config (used when auth mode may have changed).
export function resetAuthConfig() {
  _authConfig = null
}

// Builtin login: POST /api/auth/login with email + password.
export async function builtinLogin(email, password) {
  const res = await axios.post('/api/auth/login', { email, password })
  const { access_token, must_change_password } = res.data
  if (!access_token) throw new Error('No access token in response')
  setToken(access_token)

  const payload = parseJwtPayload(access_token)
  if (payload) {
    setUser({
      sub: payload.sub,
      name: payload.preferred_username || payload.name || payload.email || payload.sub,
      email: payload.email || '',
    })
  }

  if (must_change_password) {
    localStorage.setItem('hermes_must_change_password', '1')
  }

  return { must_change_password: !!must_change_password }
}

// Change password: POST /api/auth/change-password.
export async function changePassword(oldPassword, newPassword) {
  const token = getToken()
  const res = await axios.post('/api/auth/change-password', {
    old_password: oldPassword,
    new_password: newPassword,
  }, {
    headers: { Authorization: 'Bearer ' + token },
  })
  localStorage.removeItem('hermes_must_change_password')
  return res.data
}

export function getMustChangePassword() {
  return localStorage.getItem('hermes_must_change_password') === '1'
}

export function clearMustChangePassword() {
  localStorage.removeItem('hermes_must_change_password')
}

export function getToken() {
  return localStorage.getItem('hermes_token')
}

export function setToken(token) {
  localStorage.setItem('hermes_token', token)
}

export function getRefreshToken() {
  return localStorage.getItem('hermes_refresh_token')
}

export function setRefreshToken(token) {
  localStorage.setItem('hermes_refresh_token', token)
}

export function clearAuth() {
  localStorage.removeItem('hermes_token')
  localStorage.removeItem('hermes_refresh_token')
  localStorage.removeItem('hermes_user')
}

export function getUser() {
  try {
    const raw = localStorage.getItem('hermes_user')
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function setUser(user) {
  localStorage.setItem('hermes_user', JSON.stringify(user))
}

// Parse JWT payload (without verification — that's the backend's job).
export function parseJwtPayload(token) {
  try {
    const parts = token.split('.')
    if (parts.length < 2) return null
    const payload = atob(parts[1].replace(/-/g, '+').replace(/_/g, '/'))
    return JSON.parse(payload)
  } catch {
    return null
  }
}

// Check if the current token is expired (with 30s grace).
export function isTokenExpired() {
  const token = getToken()
  if (!token) return true
  const payload = parseJwtPayload(token)
  if (!payload || !payload.exp) return true
  return Date.now() / 1000 > payload.exp - 30
}

// Refresh the access token using the refresh token.
let _refreshPromise = null
export async function refreshAccessToken() {
  // Deduplicate concurrent refresh calls.
  if (_refreshPromise) return _refreshPromise
  _refreshPromise = _doRefresh()
  try {
    return await _refreshPromise
  } finally {
    _refreshPromise = null
  }
}

async function _doRefresh() {
  const refreshToken = getRefreshToken()
  if (!refreshToken) {
    clearAuth()
    return false
  }
  try {
    const res = await axios.post('/api/auth/refresh', { refresh_token: refreshToken })
    setToken(res.data.access_token)
    if (res.data.refresh_token) {
      setRefreshToken(res.data.refresh_token)
    }
    return true
  } catch {
    clearAuth()
    return false
  }
}

// ─── Interceptors ────────────────────────────────────────────────────

api.interceptors.request.use(async (config) => {
  if (currentNamespace) {
    config.headers['X-Hermes-Namespace'] = currentNamespace
  }
  const token = getToken()
  if (token) {
    // Try to refresh if expired.
    if (isTokenExpired()) {
      const ok = await refreshAccessToken()
      if (ok) {
        config.headers['Authorization'] = 'Bearer ' + getToken()
      }
    } else {
      config.headers['Authorization'] = 'Bearer ' + token
    }
  }
  return config
})

api.interceptors.response.use(
  (response) => response,
  async (error) => {
    const config = error.config
    if (error.response?.status === 401 && getToken() && !config._retried) {
      config._retried = true
      // Token might have just expired; try refresh once (OIDC only).
      const ok = await refreshAccessToken()
      if (ok) {
        config.headers['Authorization'] = 'Bearer ' + getToken()
        return api.request(config)
      }
      // Refresh failed — redirect to login.
      clearAuth()
      window.location.href = '/login'
    }
    return Promise.reject(error)
  }
)

// ─── API Methods ─────────────────────────────────────────────────────

export default {
  // Config
  getConfig: () => api.get('/config'),
  putConfig: (cfg) => api.put('/config', cfg),
  validateConfig: (cfg) => api.post('/config/validate', cfg),

  // Domains
  listDomains: () => api.get('/domains'),
  getDomain: (name) => api.get(`/domains/${name}`),
  createDomain: (domain) => api.post('/domains', domain),
  updateDomain: (name, domain) => api.put(`/domains/${name}`, domain),
  deleteDomain: (name) => api.delete(`/domains/${name}`),

  // Per-domain History
  listDomainHistory: (name) => api.get(`/domains/${name}/history`),
  getDomainVersion: (name, version) => api.get(`/domains/${name}/history/${version}`),
  rollbackDomain: (name, version) => api.post(`/domains/${name}/rollback/${version}`),

  // Clusters
  listClusters: () => api.get('/clusters'),
  getCluster: (name) => api.get(`/clusters/${name}`),
  createCluster: (cluster) => api.post('/clusters', cluster),
  updateCluster: (name, cluster) => api.put(`/clusters/${name}`, cluster),
  deleteCluster: (name) => api.delete(`/clusters/${name}`),

  // Per-cluster History
  listClusterHistory: (name) => api.get(`/clusters/${name}/history`),
  getClusterVersion: (name, version) => api.get(`/clusters/${name}/history/${version}`),
  rollbackCluster: (name, version) => api.post(`/clusters/${name}/rollback/${version}`),

  // Status
  getStatus: () => api.get('/status'),
  getInstances: () => api.get('/status/instances'),
  getController: () => api.get('/status/controller'),

  // Audit log
  listAuditLog: (limit = 50, offset = 0) => api.get(`/audit?limit=${limit}&offset=${offset}`),

  // Grafana
  getGrafanaDashboards: () => api.get('/grafana/dashboards'),
  createGrafanaDashboard: (d) => api.post('/grafana/dashboards', d),
  updateGrafanaDashboard: (d) => api.put('/grafana/dashboards', d),
  deleteGrafanaDashboard: (id) => api.delete(`/grafana/dashboards/${id}`),

  // API Credentials
  listCredentials: () => api.get('/credentials'),
  createCredential: (d) => api.post('/credentials', d),
  updateCredential: (id, d) => api.put(`/credentials/${id}`, d),
  deleteCredential: (id) => api.delete(`/credentials/${id}`),

  // Scopes
  listScopes: () => api.get('/scopes'),

  // Namespaces
  listNamespaces: () => api.get('/namespaces'),
  createNamespace: (name) => api.post('/namespaces', { name }),

  // Namespace Members
  listMembers: () => api.get('/members'),
  addMember: (userSub, role) => api.post('/members', { user_sub: userSub, role }),
  removeMember: (sub) => api.delete(`/members/${sub}`),

  // Group Bindings (OIDC group → namespace role)
  listGroupBindings: () => api.get('/group-bindings'),
  setGroupBinding: (group, role) => api.post('/group-bindings', { group, role }),
  removeGroupBinding: (group) => api.delete(`/group-bindings/${encodeURIComponent(group)}`),

  // Users
  listUsers: () => api.get('/users'),
  createBuiltinUser: (email, password, name, isAdmin) => api.post('/users', { email, password, name, is_admin: isAdmin }),
  updateUser: (sub, data) => api.put(`/users/${sub}`, data),
  deleteUser: (sub) => api.delete(`/users/${sub}`),
  setAdmin: (sub, isAdmin) => api.put(`/users/${sub}/admin`, { is_admin: isAdmin }),
  forcePasswordChange: (sub, must) => api.put(`/users/${sub}/force-password-change`, { must_change_password: must }),
  resetUserPassword: (sub, newPassword) => api.put(`/users/${sub}/reset-password`, { new_password: newPassword }),

  // WhoAmI
  whoami: () => api.get('/whoami'),
}
