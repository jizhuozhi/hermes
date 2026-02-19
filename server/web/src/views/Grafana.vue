<template>
  <div class="grafana-page">
    <div class="page-header">
      <h1>Monitoring</h1>
      <div class="header-actions">
        <button class="btn btn-sm btn-primary" @click="showAddDialog = true">+ Add Dashboard</button>
        <button v-if="currentUrl" class="btn btn-sm" @click="openExternal">Open in Grafana ↗</button>
        <button class="btn btn-sm" @click="reload">Refresh</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading" class="loading">Loading dashboards...</div>

    <div v-if="!loading && !dashboards.length && !error" class="empty">
      <p>No Grafana dashboards configured.</p>
      <p class="hint">Click <strong>+ Add Dashboard</strong> to add a Grafana dashboard URL.</p>
    </div>

    <div v-if="dashboards.length" class="dashboard-layout">
      <div class="tab-bar">
        <button
          v-for="(d, i) in dashboards" :key="d.id"
          class="tab" :class="{ active: activeTab === i }"
          @click="activeTab = i"
        >
          {{ d.name }}
          <span class="tab-actions" @click.stop>
            <button class="tab-btn" title="Edit" @click="editDashboard(d)">✎</button>
            <button class="tab-btn tab-btn-del" title="Delete" @click="deleteDashboard(d)">×</button>
          </span>
        </button>
      </div>
      <div class="iframe-wrapper">
        <div v-if="iframeLoading" class="iframe-loading">Loading dashboard...</div>
        <iframe
          v-if="currentUrl"
          :src="currentUrl"
          class="grafana-iframe"
          frameborder="0"
          allowfullscreen
          referrerpolicy="no-referrer-when-downgrade"
          @load="iframeLoading = false"
        ></iframe>
      </div>
      <div class="embed-note">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/>
        </svg>
        Grafana must have <code>allow_embedding = true</code> set. For AWS Managed Grafana, enable it in workspace configuration.
      </div>
    </div>

    <!-- Add/Edit Dialog -->
    <div v-if="showAddDialog || editingDashboard" class="dialog-overlay" @click.self="closeDialog">
      <div class="dialog">
        <h2>{{ editingDashboard ? 'Edit Dashboard' : 'Add Dashboard' }}</h2>
        <div class="form-group">
          <label>Name</label>
          <input v-model="formName" type="text" placeholder="e.g. Gateway Overview" class="input" />
        </div>
        <div class="form-group">
          <label>URL</label>
          <input v-model="formUrl" type="text" placeholder="https://your-grafana/d/xxx?orgId=1" class="input" />
          <span class="form-hint">Tip: append <code>&kiosk</code> to hide Grafana header/sidebar</span>
        </div>
        <div v-if="formError" class="alert alert-error" style="margin-top:8px">{{ formError }}</div>
        <div class="dialog-actions">
          <button class="btn" @click="closeDialog">Cancel</button>
          <button class="btn btn-primary" @click="saveDashboard" :disabled="saving">
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return {
      dashboards: [],
      activeTab: 0,
      loading: true,
      error: null,
      iframeLoading: true,
      // dialog state
      showAddDialog: false,
      editingDashboard: null,
      formName: '',
      formUrl: '',
      formError: null,
      saving: false,
    }
  },
  computed: {
    currentUrl() {
      const d = this.dashboards[this.activeTab]
      return d ? d.url : ''
    }
  },
  watch: {
    activeTab() {
      this.iframeLoading = true
    }
  },
  async created() {
    await this.loadDashboards()
  },
  methods: {
    async loadDashboards() {
      this.loading = true
      this.error = null
      try {
        const res = await api.getGrafanaDashboards()
        this.dashboards = res.data.dashboards || []
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    reload() {
      this.iframeLoading = true
      const iframe = this.$el.querySelector('.grafana-iframe')
      if (iframe) {
        iframe.src = iframe.src
      }
    },
    openExternal() {
      if (this.currentUrl) {
        window.open(this.currentUrl, '_blank')
      }
    },
    editDashboard(d) {
      this.editingDashboard = d
      this.formName = d.name
      this.formUrl = d.url
      this.formError = null
    },
    closeDialog() {
      this.showAddDialog = false
      this.editingDashboard = null
      this.formName = ''
      this.formUrl = ''
      this.formError = null
    },
    async saveDashboard() {
      if (!this.formName.trim() || !this.formUrl.trim()) {
        this.formError = 'Name and URL are required'
        return
      }
      this.saving = true
      this.formError = null
      try {
        const payload = { name: this.formName.trim(), url: this.formUrl.trim() }
        if (this.editingDashboard) {
          payload.id = this.editingDashboard.id
          await api.updateGrafanaDashboard(payload)
        } else {
          await api.createGrafanaDashboard(payload)
        }
        this.closeDialog()
        await this.loadDashboards()
      } catch (e) {
        this.formError = e.response?.data?.error || e.message
      } finally {
        this.saving = false
      }
    },
    async deleteDashboard(d) {
      if (!confirm(`Delete dashboard "${d.name}"?`)) return
      try {
        await api.deleteGrafanaDashboard(d.id)
        await this.loadDashboards()
        if (this.activeTab >= this.dashboards.length) {
          this.activeTab = Math.max(0, this.dashboards.length - 1)
        }
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    }
  }
}
</script>

<style scoped>
.grafana-page { display: flex; flex-direction: column; height: calc(100vh - 64px); }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-shrink: 0; }
.header-actions { display: flex; gap: 8px; }
h1 { font-size: 20px; font-weight: 600; }

.tab-bar { display: flex; gap: 2px; background: #161b22; border: 1px solid #30363d; border-bottom: none; border-radius: 8px 8px 0 0; padding: 4px 4px 0; flex-shrink: 0; overflow-x: auto; }
.tab { display: flex; align-items: center; gap: 6px; padding: 8px 12px; border: none; background: transparent; color: #8b949e; font-size: 13px; font-weight: 500; cursor: pointer; border-radius: 6px 6px 0 0; transition: all 0.15s; white-space: nowrap; }
.tab:hover { color: #e1e4e8; background: #21262d; }
.tab.active { color: #58a6ff; background: #0d1117; border-bottom: 2px solid #58a6ff; }
.tab-actions { display: none; margin-left: 4px; }
.tab:hover .tab-actions { display: flex; gap: 2px; }
.tab-btn { background: none; border: none; color: #8b949e; cursor: pointer; font-size: 14px; padding: 0 2px; line-height: 1; }
.tab-btn:hover { color: #e1e4e8; }
.tab-btn-del:hover { color: #f85149; }

.dashboard-layout { display: flex; flex-direction: column; flex: 1; min-height: 0; }
.iframe-wrapper { flex: 1; position: relative; border: 1px solid #30363d; border-radius: 0 0 8px 8px; overflow: hidden; background: #0d1117; }
.grafana-iframe { width: 100%; height: 100%; border: none; }
.iframe-loading { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: #8b949e; font-size: 14px; z-index: 1; }

.embed-note { display: flex; align-items: center; gap: 6px; margin-top: 8px; color: #8b949e; font-size: 12px; flex-shrink: 0; }
.embed-note code { background: #21262d; padding: 1px 5px; border-radius: 3px; font-size: 11px; }

.empty { color: #8b949e; padding: 60px; text-align: center; font-size: 15px; }
.empty .hint { margin-top: 12px; font-size: 13px; }

.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn-sm { padding: 5px 12px; font-size: 12px; }
.btn-primary { background: #238636; border-color: #238636; }
.btn-primary:hover { background: #2ea043; }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }

.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; flex-shrink: 0; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }
.loading { color: #8b949e; padding: 40px; text-align: center; }

/* Dialog */
.dialog-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
.dialog { background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; width: 480px; max-width: 90vw; }
.dialog h2 { font-size: 16px; font-weight: 600; margin-bottom: 16px; }
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 13px; color: #8b949e; margin-bottom: 4px; }
.input { width: 100%; padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 14px; box-sizing: border-box; }
.input:focus { outline: none; border-color: #58a6ff; }
.form-hint { font-size: 11px; color: #6e7681; margin-top: 4px; display: block; }
.form-hint code { background: #21262d; padding: 1px 4px; border-radius: 3px; font-size: 10px; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }
</style>
