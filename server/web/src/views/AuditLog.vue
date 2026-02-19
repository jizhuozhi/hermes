<template>
  <div>
    <div class="page-header">
      <h1>Audit Log</h1>
      <button class="btn" @click="load" :disabled="loading">Refresh</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading && !entries.length" class="loading">Loading...</div>

    <div v-if="entries.length" class="audit-table-wrapper">
      <table class="audit-table">
        <thead>
          <tr>
            <th>Revision</th>
            <th>Kind</th>
            <th>Name</th>
            <th>Action</th>
            <th>Operator</th>
            <th>Time</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.revision" class="audit-row">
            <td class="cell-revision">{{ e.revision }}</td>
            <td><span class="kind-badge" :class="e.kind">{{ e.kind }}</span></td>
            <td class="cell-name">
              <router-link v-if="e.action !== 'delete'" :to="`/${e.kind}s/${e.name}/history`" class="name-link">
                {{ e.name }}
              </router-link>
              <span v-else>{{ e.name }}</span>
            </td>
            <td><span class="action-badge" :class="'action-' + e.action">{{ e.action }}</span></td>
            <td>
              <span v-if="e.operator" class="operator-badge">{{ e.operator }}</span>
              <span v-else class="text-muted">—</span>
            </td>
            <td class="cell-time">{{ formatTime(e.timestamp) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="entries.length" class="pagination">
      <button class="btn btn-sm" :disabled="offset === 0" @click="prevPage">← Prev</button>
      <span class="page-info">{{ offset + 1 }}–{{ Math.min(offset + limit, total) }} of {{ total }}</span>
      <button class="btn btn-sm" :disabled="offset + limit >= total" @click="nextPage">Next →</button>
    </div>

    <div v-if="!loading && !entries.length && !error" class="empty">
      No audit log entries found.
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return {
      entries: [],
      total: 0,
      limit: 50,
      offset: 0,
      loading: true,
      error: null,
    }
  },
  async created() {
    await this.load()
  },
  methods: {
    async load() {
      this.loading = true
      this.error = null
      try {
        const res = await api.listAuditLog(this.limit, this.offset)
        this.entries = res.data.entries || []
        this.total = res.data.total || 0
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    prevPage() {
      this.offset = Math.max(0, this.offset - this.limit)
      this.load()
    },
    nextPage() {
      this.offset += this.limit
      this.load()
    },
    formatTime(ts) {
      return new Date(ts).toLocaleString()
    }
  }
}
</script>

<style scoped>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
h1 { font-size: 20px; font-weight: 600; }

.audit-table-wrapper { overflow-x: auto; }
.audit-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.audit-table th { text-align: left; padding: 10px 14px; border-bottom: 2px solid #30363d; color: #8b949e; font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; }
.audit-table td { padding: 10px 14px; border-bottom: 1px solid #21262d; }
.audit-row:hover { background: #161b22; }

.cell-revision { font-family: 'SF Mono', Monaco, monospace; font-weight: 600; color: #58a6ff; }
.cell-name { font-weight: 500; }
.cell-time { color: #8b949e; white-space: nowrap; }

.name-link { color: #58a6ff; text-decoration: none; }
.name-link:hover { text-decoration: underline; }

.kind-badge { padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 600; text-transform: uppercase; }
.kind-badge.domain { background: #23863433; color: #3fb950; }
.kind-badge.cluster { background: #1f6feb22; color: #58a6ff; }

.action-badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 600; }
.action-create { background: #23853422; color: #3fb950; }
.action-update { background: #1f6feb22; color: #58a6ff; }
.action-delete { background: #f8514922; color: #f85149; }
.action-rollback { background: #d2992222; color: #d29922; }
.action-import { background: #8b949e22; color: #8b949e; }

.operator-badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 500; background: #8957e522; color: #d2a8ff; border: 1px solid #8957e533; }

.pagination { display: flex; align-items: center; justify-content: center; gap: 16px; margin-top: 20px; padding: 12px; }
.page-info { color: #8b949e; font-size: 13px; }
.btn-sm { padding: 5px 12px; font-size: 12px; }

.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }

.loading { color: #8b949e; padding: 40px; text-align: center; }
.empty { color: #8b949e; padding: 60px; text-align: center; font-size: 15px; }
.text-muted { color: #8b949e; }
</style>
