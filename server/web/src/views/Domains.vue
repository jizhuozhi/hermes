<template>
  <div>
    <div class="page-header">
      <h1>Domains</h1>
      <button class="btn btn-primary" @click="$router.push('/domains/new')">+ New Domain</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading" class="loading">Loading...</div>

    <!-- Domain list -->
    <table v-if="domains.length" class="table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Hosts</th>
          <th>Routes</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="d in domains" :key="d.name" :class="{ 'default-row': d.name === '_default' }">
          <td class="mono">
            {{ d.name }}
            <span v-if="d.name === '_default'" class="badge badge-special">default</span>
          </td>
          <td>
            <span v-for="h in d.hosts" :key="h" class="badge badge-host">{{ h }}</span>
          </td>
          <td>
            <span class="badge badge-blue">{{ (d.routes || []).length }} routes</span>
          </td>
          <td class="actions">
            <button class="btn btn-small" @click="$router.push(`/domains/${d.name}/edit`)">Edit</button>
            <button class="btn btn-small btn-history" @click="$router.push(`/domains/${d.name}/history`)">History</button>
            <button class="btn btn-small btn-danger" @click="confirmDelete(d.name)"
              :disabled="d.name === '_default'">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="!loading && !domains.length && !error" class="empty">
      No domains configured. Click "New Domain" to create one.
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return { domains: [], loading: true, error: null }
  },
  async created() {
    await this.load()
  },
  methods: {
    async load() {
      this.loading = true
      this.error = null
      try {
        const res = await api.listDomains()
        const domains = res.data.domains || []
        // Sort: _default domain first, then alphabetical
        domains.sort((a, b) => {
          if (a.name === '_default') return -1
          if (b.name === '_default') return 1
          return a.name.localeCompare(b.name)
        })
        this.domains = domains
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    async confirmDelete(name) {
      if (name === '_default') return
      if (!confirm(`Delete domain "${name}" and all its routes?`)) return
      try {
        await api.deleteDomain(name)
        await this.load()
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    }
  }
}
</script>

<style scoped>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
h1 { font-size: 24px; font-weight: 600; }

.table { width: 100%; border-collapse: collapse; }
.table th, .table td { padding: 12px 16px; text-align: left; border-bottom: 1px solid #21262d; }
.table th { color: #8b949e; font-size: 12px; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 500; }
.table tbody tr:hover { background: #161b22; }
.default-row { background: #1a1f2e; }
.default-row:hover { background: #1c2233 !important; }
.mono { font-family: 'SF Mono', Monaco, monospace; font-size: 13px; }

.badge { display: inline-block; padding: 2px 8px; border-radius: 12px; font-size: 12px; font-weight: 500; }
.badge-blue { background: #1f6feb33; color: #58a6ff; }
.badge-host { background: #23863622; color: #3fb950; border: 1px solid #3fb95033; margin-right: 4px; }
.badge-special { background: #d2992222; color: #e3b341; border: 1px solid #d2992244; font-size: 11px; padding: 2px 8px; border-radius: 12px; font-weight: 600; margin-left: 6px; }

.actions { display: flex; gap: 8px; }
.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; transition: all 0.15s; }
.btn:hover { background: #30363d; border-color: #8b949e; }
.btn:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-primary { background: #238636; border-color: #2ea043; color: #fff; }
.btn-primary:hover { background: #2ea043; }
.btn-small { padding: 4px 12px; font-size: 12px; }
.btn-history { color: #d29922; border-color: #d2992244; }
.btn-history:hover { background: #d2992222; border-color: #d29922; }
.btn-danger { color: #f85149; }
.btn-danger:hover { background: #f8514922; border-color: #f85149; }
.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }
.loading { color: #8b949e; padding: 40px; text-align: center; }
.empty { color: #8b949e; padding: 60px; text-align: center; font-size: 15px; }
</style>
