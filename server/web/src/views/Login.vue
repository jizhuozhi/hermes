<template>
  <div class="login-page">
    <div class="login-card">
      <svg viewBox="0 0 24 24" width="48" height="48" fill="none" stroke="currentColor" stroke-width="2" class="login-icon">
        <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
      </svg>
      <h1>Hermes Control Plane</h1>

      <!-- Force password change mode -->
      <template v-if="mustChangePassword">
        <p class="login-desc">You must change your password before continuing.</p>
        <form class="login-form" @submit.prevent="submitChangePassword">
          <div class="form-group">
            <label for="old-password">Current Password</label>
            <input id="old-password" v-model="oldPassword" type="password" placeholder="Current password" autocomplete="current-password" />
          </div>
          <div class="form-group">
            <label for="new-password">New Password</label>
            <input id="new-password" v-model="newPassword" type="password" placeholder="New password (min 6 chars)" autocomplete="new-password" />
          </div>
          <div class="form-group">
            <label for="confirm-password">Confirm New Password</label>
            <input id="confirm-password" v-model="confirmPassword" type="password" placeholder="Confirm new password" autocomplete="new-password" />
          </div>
          <button type="submit" class="login-btn" :disabled="loading || !oldPassword || !newPassword || !confirmPassword">
            {{ loading ? 'Changing...' : 'Change Password' }}
          </button>
        </form>
      </template>

      <!-- Normal login mode -->
      <template v-else>
        <p class="login-desc">Sign in to manage your API gateway configuration.</p>

        <!-- SSO (OIDC) mode -->
        <button v-if="authMode === 'oidc' && !loading" class="login-btn" @click="loginSSO">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
            <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
          </svg>
          Sign in with SSO
        </button>

        <!-- Builtin (email/password) mode -->
        <form v-if="authMode === 'builtin'" class="login-form" @submit.prevent="loginBuiltin">
          <div class="form-group">
            <label for="email">Email</label>
            <input id="email" v-model="email" type="email" placeholder="admin@hermes.local" autocomplete="username" />
          </div>
          <div class="form-group">
            <label for="password">Password</label>
            <input id="password" v-model="password" type="password" placeholder="Password" autocomplete="current-password" />
          </div>
          <button type="submit" class="login-btn" :disabled="loading || !email || !password">
            <svg v-if="!loading" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/><polyline points="10 17 15 12 10 7"/><line x1="15" y1="12" x2="3" y2="12"/>
            </svg>
            {{ loading ? 'Signing in...' : 'Sign in' }}
          </button>
        </form>

        <div v-if="loading && authMode === 'oidc'" class="login-loading">Redirecting...</div>
      </template>

      <div v-if="error" class="login-error">{{ error }}</div>
    </div>
  </div>
</template>

<script>
import { getAuthConfig, builtinLogin, changePassword, getMustChangePassword, clearMustChangePassword } from '../api.js'

export default {
  data() {
    return {
      loading: false,
      error: '',
      authMode: null, // 'oidc', 'builtin', or null
      email: '',
      password: '',
      // Force password change state
      mustChangePassword: false,
      oldPassword: '',
      newPassword: '',
      confirmPassword: '',
    }
  },
  async created() {
    // Check if returning from a login that requires password change.
    if (getMustChangePassword()) {
      this.mustChangePassword = true
      this.authMode = 'builtin'
    }
    try {
      const cfg = await getAuthConfig()
      if (cfg.enabled) {
        this.authMode = cfg.mode || 'oidc'
      }
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
    async loginBuiltin() {
      this.loading = true
      this.error = ''
      try {
        const result = await builtinLogin(this.email, this.password)
        if (result.must_change_password) {
          this.mustChangePassword = true
          this.oldPassword = this.password
          this.password = ''
        } else {
          this.$router.replace('/')
        }
      } catch (e) {
        this.error = e.response?.data?.error || 'Sign in failed'
      } finally {
        this.loading = false
      }
    },
    async submitChangePassword() {
      this.error = ''
      if (this.newPassword !== this.confirmPassword) {
        this.error = 'New passwords do not match'
        return
      }
      if (this.newPassword.length < 6) {
        this.error = 'Password must be at least 6 characters'
        return
      }
      this.loading = true
      try {
        await changePassword(this.oldPassword, this.newPassword)
        clearMustChangePassword()
        this.$router.replace('/')
      } catch (e) {
        this.error = e.response?.data?.error || 'Password change failed'
      } finally {
        this.loading = false
      }
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
.login-form {
  text-align: left;
}
.form-group {
  margin-bottom: 16px;
}
.form-group label {
  display: block;
  font-size: 13px;
  color: #8b949e;
  margin-bottom: 6px;
}
.form-group input {
  width: 100%;
  padding: 10px 12px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e1e4e8;
  font-size: 14px;
  box-sizing: border-box;
  transition: border-color 0.15s;
}
.form-group input:focus {
  outline: none;
  border-color: #58a6ff;
}
.form-group input::placeholder {
  color: #484f58;
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
  margin-top: 4px;
}
.login-btn:hover:not(:disabled) {
  background: #2ea043;
}
.login-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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
  text-align: center;
}
</style>
