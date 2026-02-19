<template>
  <div>
    <div class="page-header">
      <div class="header-left">
        <button class="btn btn-back" @click="goBack">← Back</button>
        <h1>
          <span class="kind-badge" :class="kind">{{ kind }}</span>
          {{ resourceName }}
          <span class="subtitle">Change History</span>
        </h1>
      </div>
      <div class="header-actions">
        <label v-if="history.length >= 2" class="diff-toggle">
          <input type="checkbox" v-model="diffMode" />
          Diff Mode
        </label>
        <button class="btn" @click="load" :disabled="loading">Refresh</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="message" class="alert alert-success">{{ message }}</div>
    <div v-if="loading" class="loading">Loading...</div>

    <!-- Diff mode: select two versions -->
    <div v-if="diffMode && history.length >= 2" class="diff-section">
      <div class="diff-controls">
        <div class="diff-select">
          <label>Old Version:</label>
          <select v-model="diffOld">
            <option v-for="e in history" :key="'old-'+e.version" :value="e.version">
              v{{ e.version }} — {{ e.action }} {{ e.operator ? '(' + e.operator + ')' : '' }} — {{ formatTime(e.timestamp) }}
            </option>
          </select>
        </div>
        <span class="diff-arrow">→</span>
        <div class="diff-select">
          <label>New Version:</label>
          <select v-model="diffNew">
            <option v-for="e in history" :key="'new-'+e.version" :value="e.version">
              v{{ e.version }} — {{ e.action }} {{ e.operator ? '(' + e.operator + ')' : '' }} — {{ formatTime(e.timestamp) }}
            </option>
          </select>
        </div>
      </div>
      <div v-if="diffOld && diffNew && diffOld !== diffNew" class="diff-result">
        <div class="diff-header">
          <span>v{{ diffOld }} → v{{ diffNew }}</span>
        </div>
        <div class="diff-body">
          <div v-for="(line, i) in diffLines" :key="i"
               :class="['diff-line', line.type === '+' ? 'diff-add' : line.type === '-' ? 'diff-del' : 'diff-ctx']">
            <span class="diff-prefix">{{ line.type === '+' ? '+' : line.type === '-' ? '-' : ' ' }}</span>
            <span>{{ line.text }}</span>
          </div>
          <div v-if="!diffLines.length" class="diff-empty">No differences found.</div>
        </div>
      </div>
      <div v-else-if="diffOld === diffNew && diffOld" class="diff-result">
        <div class="diff-empty">Same version selected. Choose two different versions to compare.</div>
      </div>
    </div>

    <!-- Timeline (always visible) -->
    <div v-if="history.length" class="timeline">
      <div v-for="(entry, i) in history" :key="entry.version" class="timeline-item">
        <div class="timeline-dot" :class="{ 'dot-latest': i === 0, 'dot-delete': entry.action === 'delete' }"></div>
        <div class="timeline-card" :class="{ expanded: expanded === i }">
          <div class="timeline-header" @click="expanded = expanded === i ? null : i">
            <span class="version">v{{ entry.version }}</span>
            <span class="action-badge" :class="'action-' + entry.action">{{ entry.action }}</span>
            <span v-if="entry.operator" class="operator-badge">{{ entry.operator }}</span>
            <span class="timestamp">{{ formatTime(entry.timestamp) }}</span>
            <span v-if="i === 0" class="badge badge-current">current</span>
            <button
              v-if="i !== 0 && entry.action !== 'delete'"
              class="btn btn-rollback"
              @click.stop="confirmRollback(entry)"
              :disabled="rolling"
            >
              Rollback to this version
            </button>
          </div>
          <div v-if="expanded === i" class="timeline-body">
            <pre v-if="entry.domain">{{ JSON.stringify(entry.domain, null, 2) }}</pre>
            <pre v-else-if="entry.cluster">{{ JSON.stringify(entry.cluster, null, 2) }}</pre>
            <p v-else class="text-muted">(deleted — no content)</p>
          </div>
        </div>
      </div>
    </div>

    <div v-if="!loading && !history.length && !error" class="empty">
      No history available for this {{ kind }}.
    </div>

    <!-- Rollback confirmation modal -->
    <div v-if="rollbackTarget" class="modal-overlay" @click.self="rollbackTarget = null">
      <div class="modal">
        <h2>Confirm Rollback</h2>
        <p>
          Restore <strong>{{ resourceName }}</strong> to
          <strong>version {{ rollbackTarget.version }}</strong>?
        </p>
        <p class="modal-detail">
          {{ rollbackTarget.action }} · {{ formatTime(rollbackTarget.timestamp) }}
        </p>
        <p class="modal-warning">
          Only this single {{ kind }} will be affected. All other {{ kind }}s remain unchanged.
        </p>
        <div class="modal-actions">
          <button class="btn" @click="rollbackTarget = null">Cancel</button>
          <button class="btn btn-danger" @click="doRollback" :disabled="rolling">
            {{ rolling ? 'Rolling back...' : 'Rollback' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  props: {
    kind: { type: String, default: '' },
  },
  data() {
    return {
      resourceName: '',
      history: [],
      loading: true,
      error: null,
      message: null,
      expanded: null,
      rollbackTarget: null,
      rolling: false,
      diffMode: false,
      diffOld: null,
      diffNew: null,
    }
  },
  computed: {
    diffLines() {
      if (!this.diffOld || !this.diffNew || this.diffOld === this.diffNew) return []
      const oldEntry = this.history.find(e => e.version === this.diffOld)
      const newEntry = this.history.find(e => e.version === this.diffNew)
      if (!oldEntry || !newEntry) return []

      const oldObj = oldEntry.domain || oldEntry.cluster || null
      const newObj = newEntry.domain || newEntry.cluster || null
      const oldText = oldObj ? JSON.stringify(oldObj, null, 2) : '(deleted)'
      const newText = newObj ? JSON.stringify(newObj, null, 2) : '(deleted)'

      return this.computeDiff(oldText.split('\n'), newText.split('\n'))
    }
  },
  watch: {
    diffMode(val) {
      if (val && this.history.length >= 2) {
        this.diffOld = this.history[1].version
        this.diffNew = this.history[0].version
      }
    }
  },
  async created() {
    this.resourceName = this.$route.params.name
    await this.load()
  },
  methods: {
    goBack() {
      if (this.kind === 'domain') {
        this.$router.push('/domains')
      } else {
        this.$router.push('/clusters')
      }
    },
    async load() {
      this.loading = true
      this.error = null
      try {
        let res
        if (this.kind === 'domain') {
          res = await api.listDomainHistory(this.resourceName)
        } else {
          res = await api.listClusterHistory(this.resourceName)
        }
        this.history = res.data.history || []
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    confirmRollback(entry) {
      this.rollbackTarget = entry
    },
    async doRollback() {
      if (!this.rollbackTarget) return
      this.rolling = true
      this.error = null
      this.message = null
      try {
        let res
        if (this.kind === 'domain') {
          res = await api.rollbackDomain(this.resourceName, this.rollbackTarget.version)
        } else {
          res = await api.rollbackCluster(this.resourceName, this.rollbackTarget.version)
        }
        this.message = `${this.resourceName} rolled back to v${res.data.rolled_back_to}, created as v${res.data.new_version}`
        this.rollbackTarget = null
        await this.load()
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.rolling = false
      }
    },
    formatTime(ts) {
      return new Date(ts).toLocaleString()
    },
    computeDiff(oldLines, newLines) {
      const result = []
      const n = oldLines.length, m = newLines.length
      // Simple LCS-based diff
      const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
      for (let i = n - 1; i >= 0; i--) {
        for (let j = m - 1; j >= 0; j--) {
          if (oldLines[i] === newLines[j]) {
            dp[i][j] = dp[i + 1][j + 1] + 1
          } else {
            dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1])
          }
        }
      }
      let i = 0, j = 0
      while (i < n || j < m) {
        if (i < n && j < m && oldLines[i] === newLines[j]) {
          result.push({ type: ' ', text: oldLines[i] })
          i++; j++
        } else if (j < m && (i >= n || dp[i][j + 1] >= dp[i + 1][j])) {
          result.push({ type: '+', text: newLines[j] })
          j++
        } else {
          result.push({ type: '-', text: oldLines[i] })
          i++
        }
      }
      return result
    }
  }
}
</script>

<style scoped>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.header-left { display: flex; align-items: center; gap: 16px; }
.header-actions { display: flex; align-items: center; gap: 16px; }
h1 { font-size: 20px; font-weight: 600; display: flex; align-items: center; gap: 10px; }
.subtitle { color: #8b949e; font-weight: 400; font-size: 16px; }
.kind-badge { padding: 3px 10px; border-radius: 4px; font-size: 11px; font-weight: 600; text-transform: uppercase; }
.kind-badge.domain { background: #23863433; color: #3fb950; border: 1px solid #3fb95055; }
.kind-badge.cluster { background: #23853433; color: #3fb950; border: 1px solid #3fb95055; }

.btn-back { padding: 6px 12px; font-size: 13px; }

.diff-toggle { display: flex; align-items: center; gap: 6px; color: #c9d1d9; font-size: 13px; cursor: pointer; }
.diff-toggle input { cursor: pointer; }

.action-badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 600; }
.action-create { background: #23853422; color: #3fb950; }
.action-update { background: #1f6feb22; color: #58a6ff; }
.action-delete { background: #f8514922; color: #f85149; }
.action-rollback { background: #d2992222; color: #d29922; }
.action-import { background: #8b949e22; color: #8b949e; }

.operator-badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 500; background: #8957e522; color: #d2a8ff; border: 1px solid #8957e533; }

/* Diff section */
.diff-section { margin-bottom: 24px; }
.diff-controls { display: flex; align-items: flex-end; gap: 12px; margin-bottom: 16px; background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 16px; }
.diff-select { display: flex; flex-direction: column; gap: 4px; flex: 1; }
.diff-select label { font-size: 12px; color: #8b949e; font-weight: 600; }
.diff-select select { padding: 8px 12px; border: 1px solid #30363d; border-radius: 6px; background: #0d1117; color: #e1e4e8; font-size: 13px; }
.diff-arrow { color: #58a6ff; font-size: 20px; font-weight: 700; padding-bottom: 4px; }

.diff-result { background: #161b22; border: 1px solid #30363d; border-radius: 8px; overflow: hidden; }
.diff-header { padding: 10px 16px; background: #21262d; border-bottom: 1px solid #30363d; font-size: 13px; font-weight: 600; color: #c9d1d9; }
.diff-body { font-family: 'SF Mono', Monaco, monospace; font-size: 12px; line-height: 1.6; overflow-x: auto; max-height: 500px; overflow-y: auto; }
.diff-line { padding: 0 16px; white-space: pre; }
.diff-prefix { display: inline-block; width: 16px; user-select: none; }
.diff-add { background: #23853418; color: #3fb950; }
.diff-del { background: #f8514918; color: #f85149; }
.diff-ctx { color: #8b949e; }
.diff-empty { padding: 24px; text-align: center; color: #8b949e; }

.timeline { position: relative; padding-left: 32px; }
.timeline::before { content: ""; position: absolute; left: 10px; top: 0; bottom: 0; width: 2px; background: #30363d; }
.timeline-item { position: relative; margin-bottom: 16px; }
.timeline-dot { position: absolute; left: -26px; top: 16px; width: 10px; height: 10px; border-radius: 50%; background: #58a6ff; border: 2px solid #0f1117; }
.timeline-dot.dot-latest { background: #3fb950; }
.timeline-dot.dot-delete { background: #f85149; }

.timeline-card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; transition: border-color 0.15s; }
.timeline-card:hover { border-color: #58a6ff; }
.timeline-card.expanded { border-color: #58a6ff; }

.timeline-header { display: flex; align-items: center; gap: 12px; padding: 14px 16px; cursor: pointer; flex-wrap: wrap; }
.version { font-weight: 600; font-family: 'SF Mono', Monaco, monospace; font-size: 14px; color: #58a6ff; }
.timestamp { color: #8b949e; font-size: 13px; }

.badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 600; }
.badge-current { background: #23853433; color: #3fb950; border: 1px solid #3fb95055; margin-left: auto; }

.btn-rollback { margin-left: auto; padding: 4px 12px; font-size: 12px; background: #21262d; border: 1px solid #f8514944; color: #f85149; border-radius: 6px; cursor: pointer; }
.btn-rollback:hover { background: #f8514922; border-color: #f85149; }
.btn-rollback:disabled { opacity: 0.5; cursor: not-allowed; }

.timeline-body { padding: 0 16px 16px; border-top: 1px solid #21262d; }
.timeline-body pre { background: #0d1117; padding: 12px; border-radius: 6px; font-size: 12px; line-height: 1.5; overflow-x: auto; max-height: 400px; color: #e1e4e8; }

.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn:disabled { opacity: 0.5; }

.btn-danger { background: #da3633; border-color: #da3633; color: #fff; }
.btn-danger:hover { background: #f85149; }

.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }
.alert-success { background: #23853422; border: 1px solid #3fb950; color: #3fb950; }

.loading { color: #8b949e; padding: 40px; text-align: center; }
.empty { color: #8b949e; padding: 60px; text-align: center; font-size: 15px; }
.text-muted { color: #8b949e; font-style: italic; }

.modal-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 1000; }
.modal { background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; max-width: 460px; width: 90%; }
.modal h2 { font-size: 18px; font-weight: 600; margin-bottom: 12px; }
.modal p { color: #c9d1d9; font-size: 14px; margin-bottom: 8px; }
.modal-detail { color: #8b949e; font-size: 13px; }
.modal-warning { color: #d29922; font-size: 13px; background: #d2992222; padding: 8px 12px; border-radius: 6px; border: 1px solid #d2992244; }
.modal-actions { display: flex; justify-content: flex-end; gap: 12px; margin-top: 20px; }
</style>
