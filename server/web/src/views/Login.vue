<template>
  <div class="login-page">
    <div class="login-card">
      <svg viewBox="0 0 24 24" width="48" height="48" fill="none" stroke="currentColor" stroke-width="2" class="login-icon">
        <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
      </svg>
      <h1>Hermes Control Plane</h1>
      <p class="login-desc">Sign in to manage your API gateway configuration.</p>

      <button v-if="ssoEnabled && !loading" class="login-btn" @click="loginSSO">
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
          <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
        </svg>
        Sign in with SSO
      </button>

      <div v-if="loading" class="login-loading">Redirecting...</div>
      <div v-if="error" class="login-error">{{ error }}</div>
    </div>
  </div>
</template>

<script>
import { getAuthConfig } from '../api.js'

export default {
  data() {
    return {
      loading: false,
      error: '',
      ssoEnabled: false,
    }
  },
  async created() {
    try {
      const cfg = await getAuthConfig()
      this.ssoEnabled = cfg.enabled || false
    } catch {
      // If we can't reach the server, show nothing special.
    }
  },
  methods: {
    async loginSSO() {
      this.loading = true
      this.error = ''
      window.location.href = '/api/auth/login'
    },
  }
}
</script>

<style scoped>
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background: #0f1117;
}
.login-card {
  text-align: center;
  padding: 48px 40px;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 12px;
  max-width: 400px;
  width: 100%;
}
.login-icon {
  color: #58a6ff;
  margin-bottom: 16px;
}
.login-card h1 {
  font-size: 22px;
  color: #e1e4e8;
  margin-bottom: 8px;
}
.login-desc {
  color: #8b949e;
  font-size: 14px;
  margin-bottom: 28px;
}
.login-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 10px 24px;
  background: #238636;
  border: none;
  border-radius: 6px;
  color: #fff;
  font-size: 15px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s;
  width: 100%;
}
.login-btn:hover {
  background: #2ea043;
}
.login-loading {
  color: #8b949e;
  font-size: 14px;
  margin-top: 16px;
}
.login-error {
  margin-top: 16px;
  padding: 8px 12px;
  background: #f8514922;
  border: 1px solid #f85149;
  border-radius: 6px;
  color: #f85149;
  font-size: 13px;
}
</style>
