<template>
  <div>
    <div class="page-header">
      <h1>System Status</h1>
      <button class="btn" @click="load" :disabled="loading">Refresh</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading && !status" class="loading">Loading...</div>

    <!-- Global Summary -->
    <div v-if="status" class="summary">
      <div class="stat-card">
        <div class="stat-value revision-highlight">{{ globalRevision }}</div>
        <div class="stat-label">Global Revision</div>
      </div>
      <div class="stat-card">
        <div class="stat-value" :class="controllerStatusColor">{{ controllerStatus }}</div>
        <div class="stat-label">Controller</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{{ status.total }}</div>
        <div class="stat-label">Gateway Instances</div>
      </div>
      <div class="stat-card">
        <div class="stat-value text-green">{{ runningCount }}</div>
        <div class="stat-label">Running</div>
      </div>
    </div>

    <!-- Controller Section -->
    <div v-if="status?.controller" class="section">
      <h2 class="section-title">Controller</h2>
      <div class="instance-card">
        <div class="instance-header">
          <span class="dot" :class="statusDotClass(status.controller.status)"></span>
          <strong class="mono">{{ status.controller.id }}</strong>
          <span class="status-badge" :class="'badge-' + (status.controller.status || 'unknown')">{{ status.controller.status || 'unknown' }}</span>
          <span class="leader-badge" :class="status.controller.is_leader ? 'badge-leader' : 'badge-follower'">{{ status.controller.is_leader ? 'Leader' : 'Follower' }}</span>
        </div>
        <div class="instance-details">
          <div class="detail-row">
            <span class="detail-label">Started At</span>
            <span class="detail-value mono">{{ formatTime(status.controller.started_at) || '-' }}</span>
          </div>
          <div class="detail-row">
            <span class="detail-label">Last Heartbeat</span>
            <span class="detail-value mono">{{ formatTime(status.controller.last_heartbeat_at) || '-' }}</span>
          </div>
          <div class="detail-row">
            <span class="detail-label">Config Revision</span>
            <span class="detail-value">
              <span class="revision-badge">{{ status.controller.config_revision || 0 }}</span>
            </span>
          </div>
        </div>
      </div>
    </div>

    <div v-else-if="status && !status.controller" class="section">
      <h2 class="section-title">Controller</h2>
      <div class="empty">No controller status reported.</div>
    </div>

    <!-- Gateway Instances Section -->
    <div v-if="status" class="section">
      <h2 class="section-title">Gateway Instances</h2>

      <div v-if="status?.instances?.length" class="instances">
        <div v-for="inst in status.instances" :key="inst.id" class="instance-card">
          <div class="instance-header">
            <span class="dot" :class="statusDotClass(inst.status)"></span>
            <strong class="mono">{{ inst.id }}</strong>
            <span class="status-badge" :class="'badge-' + (inst.status || 'unknown')">{{ inst.status || 'unknown' }}</span>
          </div>
          <div class="instance-details">
            <div class="detail-row">
              <span class="detail-label">Started At</span>
              <span class="detail-value mono">{{ formatTime(inst.started_at) || '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">Registered At</span>
              <span class="detail-value mono">{{ formatTime(inst.registered_at) || '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">Last Keepalive</span>
              <span class="detail-value mono">{{ formatTime(inst.last_keepalive_at) || '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">Config Revision</span>
              <span class="detail-value">
                <span class="revision-badge">{{ inst.config_revision || 0 }}</span>
              </span>
            </div>
          </div>
        </div>
      </div>

      <div v-else class="empty">
        No gateway instances registered.
      </div>
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return { status: null, loading: true, error: null, timer: null }
  },
  computed: {
    runningCount() {
      if (!this.status?.instances) return 0
      return this.status.instances.filter(i => i.status === 'running').length
    },
    globalRevision() {
      return this.status?.controller?.config_revision || 0
    },
    controllerStatus() {
      return this.status?.controller?.status || 'offline'
    },
    controllerStatusColor() {
      const s = this.controllerStatus
      if (s === 'running') return 'text-green'
      if (s === 'shutting_down') return 'text-red'
      return 'text-muted'
    }
  },
  async created() {
    await this.load()
    this.timer = setInterval(() => this.load(), 5000)
  },
  beforeUnmount() {
    if (this.timer) clearInterval(this.timer)
  },
  methods: {
    async load() {
      this.error = null
      try {
        const res = await api.getStatus()
        this.status = res.data
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    formatTime(t) {
      if (!t) return ''
      try {
        const d = new Date(t)
        if (isNaN(d.getTime())) return t
        return d.toLocaleString()
      } catch {
        return t
      }
    },
    statusDotClass(status) {
      if (status === 'running') return 'dot-green'
      if (status === 'starting') return 'dot-yellow'
      if (status === 'shutting_down') return 'dot-red'
      return 'dot-gray'
    }
  }
}
</script>

<style scoped>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
h1 { font-size: 24px; font-weight: 600; }

.summary { display: flex; gap: 16px; margin-bottom: 28px; flex-wrap: wrap; }
.stat-card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 20px 32px; text-align: center; min-width: 120px; }
.stat-value { font-size: 32px; font-weight: 700; }
.stat-label { font-size: 12px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; margin-top: 4px; }
.text-green { color: #3fb950; }
.text-red { color: #f85149; }
.text-muted { color: #8b949e; font-size: 14px; }
.revision-highlight { color: #58a6ff; }

.section { margin-bottom: 28px; }
.section-title { font-size: 16px; font-weight: 600; color: #e1e4e8; margin-bottom: 12px; padding-bottom: 8px; border-bottom: 1px solid #21262d; }

.instances { display: flex; flex-direction: column; gap: 12px; }
.instance-card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 16px; }
.instance-header { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; padding-bottom: 12px; border-bottom: 1px solid #21262d; }
.instance-details { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 8px; }
.detail-row { display: flex; justify-content: space-between; align-items: center; padding: 6px 10px; background: #0d1117; border-radius: 6px; }
.detail-label { color: #8b949e; font-size: 12px; text-transform: uppercase; letter-spacing: 0.03em; }
.detail-value { font-size: 13px; color: #e1e4e8; }

.dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
.dot-green { background: #3fb950; }
.dot-yellow { background: #e3b341; }
.dot-red { background: #f85149; }
.dot-gray { background: #484f58; }

.status-badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
.badge-running { background: #3fb95022; color: #3fb950; }
.badge-starting { background: #e3b34122; color: #e3b341; }
.badge-shutting_down { background: #f8514922; color: #f85149; }
.badge-unknown { background: #484f5822; color: #484f58; }
.badge-offline { background: #484f5822; color: #484f58; }

.leader-badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
.badge-leader { background: #da8b4522; color: #da8b45; }
.badge-follower { background: #8b949e22; color: #8b949e; }

.revision-badge { display: inline-block; padding: 2px 10px; background: #1f6feb22; color: #58a6ff; border-radius: 10px; font-size: 12px; font-weight: 600; font-family: 'SF Mono', Monaco, monospace; }

.mono { font-family: 'SF Mono', Monaco, monospace; }
.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn:disabled { opacity: 0.5; }
.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }
.loading { color: #8b949e; padding: 40px; text-align: center; }
.empty { color: #8b949e; padding: 40px; text-align: center; font-size: 14px; }
</style>
