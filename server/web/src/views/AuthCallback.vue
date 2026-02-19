<template>
  <div class="callback-page">
    <div class="callback-card">
      <div v-if="!error" class="callback-loading">Completing sign-in...</div>
      <div v-else class="callback-error">
        <p>{{ error }}</p>
        <button class="callback-btn" @click="$router.push('/login')">Back to Login</button>
      </div>
    </div>
  </div>
</template>

<script>
import axios from 'axios'
import { setToken, setRefreshToken, setUser, parseJwtPayload } from '../api.js'

export default {
  data() {
    return { error: '' }
  },
  async created() {
    const code = new URLSearchParams(window.location.search).get('code')
    if (!code) {
      this.error = 'No authorization code received.'
      return
    }
    try {
      const res = await axios.get('/api/auth/token', {
        params: { code }
      })
      const { access_token, refresh_token } = res.data
      if (!access_token) {
        this.error = 'No access token in response.'
        return
      }
      setToken(access_token)
      if (refresh_token) setRefreshToken(refresh_token)

      // Extract user info from JWT payload.
      const payload = parseJwtPayload(access_token)
      if (payload) {
        setUser({
          sub: payload.sub,
          name: payload.preferred_username || payload.name || payload.email || payload.sub,
          email: payload.email || '',
        })
      }

      this.$router.replace('/')
    } catch (e) {
      this.error = e.response?.data?.error || 'Authentication failed.'
    }
  }
}
</script>

<style scoped>
.callback-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background: #0f1117;
}
.callback-card {
  text-align: center;
  padding: 48px 40px;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 12px;
}
.callback-loading {
  color: #8b949e;
  font-size: 16px;
}
.callback-error p {
  color: #f85149;
  font-size: 14px;
  margin-bottom: 16px;
}
.callback-btn {
  padding: 8px 20px;
  background: #21262d;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e1e4e8;
  cursor: pointer;
}
.callback-btn:hover {
  background: #30363d;
}
</style>
