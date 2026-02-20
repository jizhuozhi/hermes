<template>
  <div class="app app-auth" v-if="isAuthPage">
    <router-view />
  </div>
  <div class="app" v-else>
    <nav class="sidebar">
      <div class="sidebar-top">
        <div class="logo">
          <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
          </svg>
          <span>Hermes Control Plane</span>
        </div>
        <div class="ns-selector">
          <label class="ns-label">Region</label>
          <div class="ns-row">
            <select v-model="currentNs" @change="onNsChange" class="ns-select">
              <option v-for="ns in regions" :key="ns" :value="ns">{{ ns }}</option>
            </select>
            <button class="ns-add-btn" @click="showCreateNs = true" title="Create region">+</button>
          </div>
          <div v-if="showCreateNs" class="ns-create">
            <input v-model="newNsName" class="ns-input" placeholder="region name" @keyup.enter="createNs" />
            <div class="ns-create-actions">
              <button class="ns-btn ns-btn-primary" @click="createNs" :disabled="!newNsName.trim()">Create</button>
              <button class="ns-btn ns-btn-cancel" @click="showCreateNs = false; newNsName = ''">Cancel</button>
            </div>
            <div v-if="nsError" class="ns-error">{{ nsError }}</div>
          </div>
        </div>
        <div class="nav-links">
          <router-link to="/domains" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
            </svg>
            Domains
          </router-link>
          <router-link to="/clusters" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="3"/><circle cx="4" cy="8" r="2"/><circle cx="20" cy="8" r="2"/><circle cx="4" cy="16" r="2"/><circle cx="20" cy="16" r="2"/><path d="M6 9l4 2M14 14l4 2M6 15l4-2M14 10l4-2"/>
            </svg>
            Clusters
          </router-link>
          <router-link to="/status" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M22 12h-4l-3 9L9 3l-3 9H2"/>
            </svg>
            Status
          </router-link>
          <router-link to="/audit" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><polyline points="10 9 9 9 8 9"/>
            </svg>
            Audit Log
          </router-link>
          <router-link to="/monitoring" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="2" y="3" width="20" height="14" rx="2" ry="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/>
            </svg>
            Monitoring
          </router-link>
          <router-link to="/credentials" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>
            </svg>
            Credentials
          </router-link>
          <router-link to="/members" class="nav-item" active-class="active">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>
            </svg>
            Members
          </router-link>
        </div>
      </div>
      <div v-if="user" class="user-info">
        <div class="user-avatar">{{ userInitial }}</div>
        <div class="user-detail">
          <div class="user-name">{{ user.name }}</div>
          <div class="user-email" v-if="user.email">{{ user.email }}</div>
        </div>
        <button class="logout-btn" @click="logout" title="Sign out">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/>
          </svg>
        </button>
      </div>
    </nav>
    <main class="content">
      <router-view :key="currentNs + '-' + nsKey" />
    </main>
  </div>
</template>

<script>
import api, { getRegion, setRegion, getUser, clearAuth } from './api.js'

export default {
  data() {
    return {
      regions: ['default'],
      currentNs: getRegion(),
      nsKey: 0,
      showCreateNs: false,
      newNsName: '',
      nsError: '',
      user: getUser(),
    }
  },
  computed: {
    isAuthPage() {
      return this.$route.path === '/login' || this.$route.path === '/auth/callback'
    },
    userInitial() {
      const name = this.user?.name || '?'
      return name.charAt(0).toUpperCase()
    }
  },
  watch: {
    '$route.path'() {
      // Refresh user when navigating back from auth.
      this.user = getUser()
    }
  },
  async created() {
    try {
      const res = await api.listRegions()
      const regionList = res.data.regions || []
      if (regionList.length) {
        this.regions = regionList
      }
      if (!this.regions.includes(this.currentNs)) {
        this.regions.push(this.currentNs)
      }
    } catch (e) {
      // fallback: keep 'default'
    }
  },
  methods: {
    onNsChange() {
      setRegion(this.currentNs)
      this.nsKey++
    },
    async createNs() {
      const name = this.newNsName.trim()
      if (!name) return
      this.nsError = ''
      try {
        await api.createRegion(name)
        this.regions.push(name)
        setRegion(name)
        this.currentNs = name
        this.nsKey++
        this.showCreateNs = false
        this.newNsName = ''
      } catch (e) {
        this.nsError = e.response?.data?.error || 'Failed to create region'
      }
    },
    logout() {
      clearAuth()
      this.$router.push('/login')
    }
  }
}
</script>

<style>
.app {
  display: flex;
  min-height: 100vh;
}
.app.app-auth {
  display: block;
}
.sidebar {
  width: 220px;
  background: #161b22;
  border-right: 1px solid #30363d;
  padding: 20px 0 0;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
}
.sidebar-top {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.logo {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 20px 24px;
  color: #58a6ff;
  font-size: 17px;
  font-weight: 600;
  border-bottom: 1px solid #30363d;
  margin-bottom: 12px;
}
.ns-selector {
  padding: 0 16px 12px;
  border-bottom: 1px solid #30363d;
  margin-bottom: 12px;
}
.user-info {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-top: 1px solid #30363d;
  background: #0d1117;
}
.user-avatar {
  width: 30px;
  height: 30px;
  border-radius: 50%;
  background: #1f6feb;
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}
.user-detail {
  flex: 1;
  min-width: 0;
}
.user-name {
  font-size: 13px;
  color: #e1e4e8;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.3;
}
.user-email {
  font-size: 11px;
  color: #8b949e;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.3;
}
.logout-btn {
  background: none;
  border: none;
  color: #8b949e;
  padding: 4px;
  cursor: pointer;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transition: all 0.15s;
}
.logout-btn:hover {
  color: #f85149;
  background: #f8514922;
}
.ns-label {
  display: block;
  font-size: 11px;
  color: #8b949e;
  margin-bottom: 4px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.ns-select {
  width: 100%;
  padding: 6px 8px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e1e4e8;
  font-size: 13px;
  cursor: pointer;
}
.ns-select:focus {
  outline: none;
  border-color: #58a6ff;
}
.ns-row {
  display: flex;
  gap: 6px;
  align-items: center;
}
.ns-row .ns-select {
  flex: 1;
  min-width: 0;
}
.ns-add-btn {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #21262d;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #58a6ff;
  font-size: 16px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s;
}
.ns-add-btn:hover {
  background: #30363d;
  border-color: #58a6ff;
}
.ns-create {
  margin-top: 8px;
}
.ns-input {
  width: 100%;
  padding: 6px 8px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e1e4e8;
  font-size: 13px;
  box-sizing: border-box;
}
.ns-input:focus {
  outline: none;
  border-color: #58a6ff;
}
.ns-create-actions {
  display: flex;
  gap: 6px;
  margin-top: 6px;
}
.ns-btn {
  padding: 4px 12px;
  border: 1px solid #30363d;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.ns-btn-primary {
  background: #238636;
  border-color: #238636;
  color: #fff;
}
.ns-btn-primary:hover:not(:disabled) {
  background: #2ea043;
}
.ns-btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.ns-btn-cancel {
  background: transparent;
  color: #8b949e;
}
.ns-btn-cancel:hover {
  color: #e1e4e8;
}
.ns-error {
  margin-top: 4px;
  font-size: 12px;
  color: #f85149;
}
.nav-links {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 0 8px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  color: #8b949e;
  text-decoration: none;
  border-radius: 6px;
  font-size: 14px;
  transition: all 0.15s;
}
.nav-item:hover {
  background: #1c2128;
  color: #e1e4e8;
}
.nav-item.active {
  background: #1f6feb22;
  color: #58a6ff;
}
.content {
  flex: 1;
  padding: 32px;
  overflow-y: auto;
}
</style>
