<template>
  <div>
    <div class="page-header">
      <h1>Clusters</h1>
      <button class="btn btn-primary" @click="$router.push('/clusters/new')">+ New Cluster</button>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="loading" class="loading">Loading...</div>

    <table v-if="clusters.length" class="table">
      <thead>
        <tr>
          <th>Name</th>
          <th>LB Type</th>
          <th>Upstream</th>
          <th>Timeout</th>
          <th>Health Check</th>
          <th>Features</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in clusters" :key="c.name">
          <td class="mono">{{ c.name }}</td>
          <td><span class="badge badge-purple">{{ c.type }}</span></td>
          <td>
            <span v-if="c.discovery_type" class="badge badge-blue">{{ c.discovery_type }}</span>
            <span v-if="c.service_name" class="text-muted"> {{ c.service_name }}</span>
            <div v-if="c.discovery_args && c.discovery_args.metadata_match" class="meta-filters">
              <span v-for="(vals, key) in c.discovery_args.metadata_match" :key="key" class="meta-filter">
                {{ key }}={{ vals.join('|') }}
              </span>
            </div>
            <span v-if="!c.discovery_type" class="badge badge-gray">static</span>
            <span v-if="!c.discovery_type" class="text-muted"> {{ c.nodes?.length || 0 }} nodes</span>
          </td>
          <td class="text-muted">{{ c.timeout?.connect }}s / {{ c.timeout?.read }}s</td>
          <td>
            <span v-if="c.health_check && c.health_check.active" class="badge badge-green">Active</span>
            <span v-if="c.health_check && c.health_check.passive" class="badge badge-green">Passive</span>
            <span v-if="!c.health_check" class="text-muted">-</span>
          </td>
          <td>
            <span v-if="c.retry" class="badge badge-yellow">Retry</span>
            <span v-if="c.circuit_breaker" class="badge badge-orange">CB</span>
            <span v-if="!c.retry && !c.circuit_breaker" class="text-muted">-</span>
          </td>
          <td class="actions">
            <button class="btn btn-small" @click="$router.push(`/clusters/${c.name}/edit`)">Edit</button>
            <button class="btn btn-small btn-history" @click="$router.push(`/clusters/${c.name}/history`)">History</button>
            <button class="btn btn-small btn-danger" @click="confirmDelete(c.name)">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="!loading && !clusters.length && !error" class="empty">
      No clusters configured. Click "New Cluster" to create one.
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return { clusters: [], loading: true, error: null }
  },
  async created() {
    await this.load()
  },
  methods: {
    async load() {
      this.loading = true
      this.error = null
      try {
        const res = await api.listClusters()
        this.clusters = res.data.clusters || []
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    async confirmDelete(name) {
      if (!confirm(`Delete cluster "${name}"? Make sure no routes reference it.`)) return
      try {
        await api.deleteCluster(name)
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
.mono { font-family: 'SF Mono', Monaco, monospace; font-size: 13px; }
.badge { display: inline-block; padding: 2px 8px; border-radius: 12px; font-size: 12px; font-weight: 500; }
.badge-blue { background: #1f6feb33; color: #58a6ff; }
.badge-gray { background: #30363d; color: #8b949e; }
.badge-purple { background: #8957e533; color: #bc8cff; }
.badge-green { background: #23863633; color: #3fb950; }
.badge-yellow { background: #d2992233; color: #d29922; }
.badge-orange { background: #db611333; color: #db6113; }
.text-muted { color: #8b949e; font-size: 12px; }
.meta-filters { margin-top: 4px; display: flex; flex-wrap: wrap; gap: 4px; }
.meta-filter { display: inline-block; padding: 1px 6px; background: #1f6feb22; border: 1px solid #1f6feb55; border-radius: 3px; font-size: 11px; color: #58a6ff; font-family: 'SF Mono', Monaco, monospace; }
.actions { display: flex; gap: 8px; }
.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; transition: all 0.15s; }
.btn:hover { background: #30363d; border-color: #8b949e; }
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
