<template>
  <div>
    <div class="page-header">
      <h1>{{ isEdit ? `Edit Domain: ${domainName}` : 'New Domain' }}</h1>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="validationErrors.length" class="alert alert-error">
      <div v-for="e in validationErrors" :key="e.field">{{ e.field }}: {{ e.message }}</div>
    </div>

    <form @submit.prevent="save" class="form">
      <!-- Basic info -->
      <div class="form-section">
        <h2>Basic</h2>
        <div class="form-grid">
          <div class="field">
            <label>Name</label>
            <input v-model="domain.name" :disabled="isEdit" required placeholder="my-domain" />
          </div>
        </div>
      </div>

      <!-- Hosts -->
      <div class="form-section">
        <h2>Hosts</h2>
        <p v-if="isDefaultDomain" class="section-hint">
          The <code>_default</code> domain uses <code>_</code> as its catch-all host (like nginx <code>server_name _</code>). This cannot be changed.
        </p>
        <p v-else class="section-hint">Domain matching hosts. Use <code>*.example.com</code> for wildcard matching.</p>
        <div class="host-list">
          <div v-for="(h, i) in domain.hosts" :key="i" class="host-row">
            <input v-model="domain.hosts[i]" required placeholder="api.example.com or *.staging.example.com"
              :disabled="isDefaultDomain" />
            <button type="button" class="btn btn-small btn-danger" @click="domain.hosts.splice(i, 1)"
              :disabled="domain.hosts.length <= 1 || isDefaultDomain">&times;</button>
          </div>
          <button v-if="!isDefaultDomain" type="button" class="btn btn-small" @click="domain.hosts.push('')">+ Add Host</button>
        </div>
      </div>

      <!-- Routes -->
      <div class="form-section">
        <div class="section-header">
          <h2>Routes</h2>
          <button type="button" class="btn btn-small btn-primary" @click="addRoute">+ Add Route</button>
        </div>
        <p class="section-hint">Routes owned by this domain. They only match requests whose Host header matches one of the hosts above.</p>

        <div v-if="!domain.routes.length" class="empty-routes">
          No routes yet. Click "Add Route" to create one.
        </div>

        <div v-for="(route, ri) in domain.routes" :key="ri" class="route-card" :class="{ collapsed: collapsedRoutes[ri] }">
          <div class="route-header" @click="toggleRoute(ri)">
            <svg class="chevron" :class="{ open: !collapsedRoutes[ri] }" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.5">
              <path d="M9 18l6-6-6-6"/>
            </svg>
            <span class="route-name">{{ route.name || '(unnamed)' }}</span>
            <span class="route-uri mono">{{ route.uri || '/' }}</span>
            <span class="dot" :class="route.status === 1 ? 'dot-green' : 'dot-red'"></span>
            <button type="button" class="btn btn-small btn-danger" @click.stop="domain.routes.splice(ri, 1)">&times;</button>
          </div>

          <div v-if="!collapsedRoutes[ri]" class="route-body">
            <div class="form-grid">
              <div class="field">
                <label>Name</label>
                <input v-model="route.name" required placeholder="route-name" />
              </div>
              <div class="field">
                <label>URI</label>
                <input v-model="route.uri" required placeholder="/v1/api/*" />
              </div>
              <div class="field">
                <label>Priority</label>
                <input v-model.number="route.priority" type="number" />
              </div>
              <div class="field">
                <label>Status</label>
                <select v-model.number="route.status">
                  <option :value="1">Enabled</option>
                  <option :value="0">Disabled</option>
                </select>
              </div>
              <div class="field field-wide">
                <label>Methods</label>
                <div class="method-checkboxes">
                  <label v-for="m in allMethods" :key="m" class="method-check" :class="{ active: route._methods && route._methods.includes(m) }">
                    <input type="checkbox" :value="m" v-model="route._methods" />
                    {{ m }}
                  </label>
                </div>
              </div>
            </div>

            <!-- Header matchers -->
            <div class="sub-section">
              <h3>Header Matchers</h3>
              <div v-for="(hm, hi) in route.headers" :key="hi" class="header-row">
                <input v-model="hm.name" placeholder="Header-Name" />
                <select v-model="hm.match_type">
                  <option value="exact">exact</option>
                  <option value="prefix">prefix</option>
                  <option value="regex">regex</option>
                  <option value="present">present</option>
                </select>
                <input v-model="hm.value" placeholder="value" :disabled="hm.match_type === 'present'" />
                <label class="inline-check"><input type="checkbox" v-model="hm.invert" /> Invert</label>
                <button type="button" class="btn btn-small btn-danger" @click="route.headers.splice(hi, 1)">&times;</button>
              </div>
              <button type="button" class="btn btn-small" @click="route.headers.push({name:'', value:'', match_type:'exact', invert:false})">+ Add Header</button>
            </div>

            <!-- Clusters -->
            <div class="sub-section">
              <h3>Clusters (Traffic Split)</h3>
              <div v-for="(wc, ci) in route.clusters" :key="ci" class="cluster-row">
                <select v-model="wc.name" required>
                  <option value="" disabled>Select cluster...</option>
                  <option v-for="c in availableClusters" :key="c.name" :value="c.name">
                    {{ c.name }} ({{ c.type }})
                  </option>
                </select>
                <input v-model.number="wc.weight" type="number" min="0" placeholder="weight" style="width:80px" />
                <button type="button" class="btn btn-small btn-danger" @click="route.clusters.splice(ci, 1)"
                  :disabled="route.clusters.length <= 1">&times;</button>
              </div>
              <button type="button" class="btn btn-small" @click="route.clusters.push({name:'', weight:100})">+ Add Cluster</button>
            </div>

            <!-- Cluster Override Header -->
            <div class="sub-section">
              <h3>Cluster Override Header <span class="hint-inline">(optional, for per-environment testing)</span></h3>
              <div class="form-grid">
                <div class="field">
                  <label>Header Name</label>
                  <input v-model="route.cluster_override_header" placeholder="e.g. X-Cluster-Override" />
                  <span class="hint-text">When set, requests carrying this header can force a specific cluster by name. Response will include <code>X-Hermes-Cluster</code> and <code>X-Hermes-Cluster-Override: true</code></span>
                </div>
              </div>
            </div>

            <!-- Header Transforms -->
            <div class="sub-section">
              <h3>Header Transforms <span class="hint-inline">(traffic coloring &amp; response injection)</span></h3>
              <div class="transform-group">
                <h4>Request Headers <span class="hint-inline">— injected into upstream requests (e.g. <code>X-Env: canary</code>)</span></h4>
                <div v-for="(t, ti) in route.request_header_transforms" :key="'req'+ti" class="transform-row">
                  <select v-model="t.action">
                    <option value="set">set</option>
                    <option value="add">add</option>
                    <option value="remove">remove</option>
                  </select>
                  <input v-model="t.name" placeholder="Header-Name" />
                  <input v-model="t.value" placeholder="value" :disabled="t.action === 'remove'" />
                  <button type="button" class="btn btn-small btn-danger" @click="route.request_header_transforms.splice(ti, 1)">&times;</button>
                </div>
                <button type="button" class="btn btn-small" @click="route.request_header_transforms.push({name:'', value:'', action:'set'})">+ Add Request Transform</button>
              </div>
              <div class="transform-group">
                <h4>Response Headers <span class="hint-inline">— injected into downstream responses</span></h4>
                <div v-for="(t, ti) in route.response_header_transforms" :key="'res'+ti" class="transform-row">
                  <select v-model="t.action">
                    <option value="set">set</option>
                    <option value="add">add</option>
                    <option value="remove">remove</option>
                  </select>
                  <input v-model="t.name" placeholder="Header-Name" />
                  <input v-model="t.value" placeholder="value" :disabled="t.action === 'remove'" />
                  <button type="button" class="btn btn-small btn-danger" @click="route.response_header_transforms.splice(ti, 1)">&times;</button>
                </div>
                <button type="button" class="btn btn-small" @click="route.response_header_transforms.push({name:'', value:'', action:'set'})">+ Add Response Transform</button>
              </div>
            </div>

            <!-- Limits & Compression -->
            <div class="sub-section">
              <h3>Limits</h3>
              <div class="form-grid">
                <div class="field">
                  <label>Max Body Size (bytes)</label>
                  <input v-model.number="route.max_body_bytes" type="number" min="0" placeholder="unlimited" />
                  <span class="hint-text">Maximum request body size in bytes. Leave empty for unlimited.</span>
                </div>
                <div class="field">
                  <label>Response Compression</label>
                  <label class="toggle"><input type="checkbox" v-model="route.enable_compression" /><span></span></label>
                  <span class="hint-text">Enable streaming gzip/brotli compression for responses on this route.</span>
                </div>
              </div>
            </div>

            <!-- Rate limit toggle -->
            <div class="sub-section">
              <h3>Rate Limit <label class="toggle"><input type="checkbox" v-model="route._hasRateLimit" @change="toggleRateLimit(route)" /><span></span></label></h3>
              <div v-if="route._hasRateLimit" class="form-grid">
                <div class="field">
                  <label>Mode</label>
                  <select v-model="route.rate_limit.mode">
                    <option value="req">Token Bucket (req/s)</option>
                    <option value="count">Sliding Window (count)</option>
                  </select>
                </div>
                <div class="field" v-if="route.rate_limit.mode === 'req'">
                  <label>Rate (req/s)</label>
                  <input v-model.number="route.rate_limit.rate" type="number" />
                </div>
                <div class="field" v-if="route.rate_limit.mode === 'req'">
                  <label>Burst</label>
                  <input v-model.number="route.rate_limit.burst" type="number" />
                </div>
                <div class="field" v-if="route.rate_limit.mode === 'count'">
                  <label>Count</label>
                  <input v-model.number="route.rate_limit.count" type="number" />
                </div>
                <div class="field" v-if="route.rate_limit.mode === 'count'">
                  <label>Time Window (s)</label>
                  <input v-model.number="route.rate_limit.time_window" type="number" />
                </div>
                <div class="field">
                  <label>Key</label>
                  <select v-model="route.rate_limit.key">
                    <option value="route">route (recommended)</option>
                    <option value="host_uri">host_uri</option>
                    <option value="remote_addr">remote_addr</option>
                    <option value="uri">uri</option>
                  </select>
                </div>
                <div class="field">
                  <label>Rejected Code</label>
                  <input v-model.number="route.rate_limit.rejected_code" type="number" />
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="form-actions">
        <button type="button" class="btn" @click="$router.push('/domains')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="saving">
          {{ saving ? 'Saving...' : (isEdit ? 'Update' : 'Create') }}
        </button>
      </div>
    </form>
  </div>
</template>

<script>
import api from '../api.js'

const ALL_METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH', 'HEAD', 'OPTIONS', 'CONNECT', 'TRACE']

const defaultRoute = () => ({
  id: '', name: '', uri: '', methods: [], headers: [], priority: 0, status: 1,
  clusters: [{ name: '', weight: 100 }],
  rate_limit: null,
  cluster_override_header: null,
  request_header_transforms: [],
  response_header_transforms: [],
  max_body_bytes: null,
  enable_compression: false,
  _methods: [...ALL_METHODS],
  _hasRateLimit: false,
})

export default {
  data() {
    return {
      domain: { name: '', hosts: [''], routes: [] },
      availableClusters: [],
      allMethods: ALL_METHODS,
      isEdit: false,
      isDefaultDomain: false,
      domainName: '',
      error: null,
      validationErrors: [],
      saving: false,
      collapsedRoutes: {},
    }
  },
  async created() {
    await this.loadClusters()
    const name = this.$route.params.name
    if (name) {
      this.isEdit = true
      this.domainName = name
      this.isDefaultDomain = name === '_default'
      try {
        const res = await api.getDomain(name)
        this.domain = res.data
        if (!this.domain.hosts || this.domain.hosts.length === 0) {
          this.domain.hosts = ['']
        }
        if (!this.domain.routes) {
          this.domain.routes = []
        }
        for (const r of this.domain.routes) {
          r._methods = (r.methods || []).length > 0 ? [...r.methods] : [...ALL_METHODS]
          r._hasRateLimit = !!r.rate_limit
          if (!r.headers) r.headers = []
          if (!r.clusters || r.clusters.length === 0) r.clusters = [{ name: '', weight: 100 }]
          if (!r.request_header_transforms) r.request_header_transforms = []
          if (!r.response_header_transforms) r.response_header_transforms = []
        }
        // Collapse all routes by default in edit mode
        for (let i = 0; i < this.domain.routes.length; i++) {
          this.collapsedRoutes[i] = true
        }
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    }
  },
  methods: {
    async loadClusters() {
      try {
        const res = await api.listClusters()
        this.availableClusters = res.data.clusters || []
      } catch (e) {
        this.error = 'Failed to load clusters: ' + (e.response?.data?.error || e.message)
      }
    },
    addRoute() {
      this.domain.routes.push(defaultRoute())
      // Auto-expand new route
      this.$nextTick(() => {
        this.collapsedRoutes[this.domain.routes.length - 1] = false
      })
    },
    toggleRoute(idx) {
      this.collapsedRoutes = { ...this.collapsedRoutes, [idx]: !this.collapsedRoutes[idx] }
    },
    toggleRateLimit(route) {
      if (route._hasRateLimit && !route.rate_limit) {
        route.rate_limit = { mode: 'req', rate: 1000, burst: 100, key: 'host_uri', rejected_code: 429 }
      } else if (!route._hasRateLimit) {
        route.rate_limit = null
      }
    },
    async save() {
      this.saving = true
      this.error = null
      this.validationErrors = []

      // Clean up transient fields
      const payload = JSON.parse(JSON.stringify(this.domain))
      if (this.isDefaultDomain) {
        payload.hosts = ['_']
      } else {
        payload.hosts = payload.hosts.filter(h => h.trim())
      }
      for (const r of payload.routes) {
        r.methods = (r._methods || []).filter(Boolean)
        r.clusters = (r.clusters || []).filter(c => c.name)
        if (!r._hasRateLimit) r.rate_limit = null
        if (!r.cluster_override_header || !r.cluster_override_header.trim()) r.cluster_override_header = null
        if (r.max_body_bytes === '' || r.max_body_bytes === null || r.max_body_bytes === undefined) r.max_body_bytes = null
        r.request_header_transforms = (r.request_header_transforms || []).filter(t => t.name)
        r.response_header_transforms = (r.response_header_transforms || []).filter(t => t.name)
        delete r._methods
        delete r._hasRateLimit
      }

      if (payload.hosts.length === 0) {
        this.validationErrors = [{ field: 'hosts', message: 'At least one host is required' }]
        this.saving = false
        return
      }

      try {
        if (this.isEdit) {
          await api.updateDomain(this.domainName, payload)
        } else {
          await api.createDomain(payload)
        }
        this.$router.push('/domains')
      } catch (e) {
        const data = e.response?.data
        if (data?.errors) {
          this.validationErrors = data.errors
        } else {
          this.error = data?.error || e.message
        }
      } finally {
        this.saving = false
      }
    }
  }
}
</script>

<style scoped>
.page-header { margin-bottom: 24px; }
h1 { font-size: 24px; font-weight: 600; }
h2 { font-size: 16px; font-weight: 600; margin-bottom: 16px; color: #e1e4e8; display: flex; align-items: center; gap: 12px; }
h3 { font-size: 14px; font-weight: 500; color: #8b949e; margin: 12px 0 8px; display: flex; align-items: center; gap: 10px; }
.section-hint { font-size: 13px; color: #8b949e; margin: -8px 0 16px; }
.section-header { display: flex; justify-content: space-between; align-items: center; }

.form-section { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 20px; margin-bottom: 16px; }
.form-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 16px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.field label { font-size: 12px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; }
.field input, .field select { padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 14px; }
.field input:focus, .field select:focus { outline: none; border-color: #58a6ff; box-shadow: 0 0 0 3px #1f6feb33; }

code { background: #21262d; padding: 1px 6px; border-radius: 4px; font-size: 12px; color: #79c0ff; }

/* Hosts */
.host-list { display: flex; flex-direction: column; gap: 8px; }
.host-row { display: flex; gap: 8px; align-items: center; }
.host-row input { flex: 1; padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 14px; font-family: 'SF Mono', Monaco, monospace; }
.host-row input:focus { outline: none; border-color: #58a6ff; box-shadow: 0 0 0 3px #1f6feb33; }

/* Routes accordion */
.route-card { border: 1px solid #30363d; border-radius: 8px; margin-bottom: 8px; overflow: hidden; transition: border-color 0.15s; }
.route-card:hover { border-color: #484f58; }
.route-header { display: flex; align-items: center; gap: 10px; padding: 12px 16px; cursor: pointer; background: #0d1117; }
.route-header:hover { background: #151b23; }
.chevron { color: #8b949e; transition: transform 0.2s; flex-shrink: 0; }
.chevron.open { transform: rotate(90deg); }
.route-name { font-weight: 600; font-size: 14px; color: #e1e4e8; }
.route-uri { color: #8b949e; font-size: 13px; }
.mono { font-family: 'SF Mono', Monaco, monospace; }
.route-body { padding: 16px; border-top: 1px solid #21262d; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-left: auto; flex-shrink: 0; }
.dot-green { background: #3fb950; }
.dot-red { background: #f85149; }

.empty-routes { color: #8b949e; padding: 24px; text-align: center; font-size: 14px; border: 1px dashed #30363d; border-radius: 8px; }

/* Header matchers */
.sub-section { margin-top: 16px; padding-top: 12px; border-top: 1px solid #21262d; }
.header-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; flex-wrap: wrap; }
.header-row input, .header-row select { padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }
.header-row input:first-child { width: 180px; }
.header-row select { width: 100px; }
.header-row input:nth-of-type(2) { flex: 1; min-width: 120px; }
.inline-check { display: flex; align-items: center; gap: 4px; font-size: 12px; color: #8b949e; cursor: pointer; white-space: nowrap; }

/* Method checkboxes */
.field-wide { grid-column: 1 / -1; }
.method-checkboxes { display: flex; flex-wrap: wrap; gap: 6px; }
.method-check { display: inline-flex; align-items: center; gap: 4px; padding: 4px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; font-size: 12px; font-weight: 500; color: #8b949e; cursor: pointer; transition: all 0.15s; user-select: none; }
.method-check input { display: none; }
.method-check.active { background: #1f6feb22; border-color: #1f6feb; color: #58a6ff; }
.method-check:hover { border-color: #58a6ff; }

/* Cluster rows */
.cluster-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.cluster-row select { flex: 1; padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }
.cluster-row input { padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }

.form-actions { display: flex; justify-content: flex-end; gap: 12px; margin-top: 24px; }
.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; text-decoration: none; }
.btn:hover { background: #30363d; }
.btn-primary { background: #238636; border-color: #2ea043; color: #fff; }
.btn-primary:hover { background: #2ea043; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-small { padding: 4px 12px; font-size: 12px; }
.btn-danger { color: #f85149; }
.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; font-size: 13px; }

.toggle { position: relative; display: inline-block; width: 36px; height: 20px; }
.toggle input { opacity: 0; width: 0; height: 0; }
.toggle span { position: absolute; cursor: pointer; inset: 0; background: #30363d; border-radius: 20px; transition: 0.2s; }
.toggle span::before { content: ""; position: absolute; height: 14px; width: 14px; left: 3px; bottom: 3px; background: #e1e4e8; border-radius: 50%; transition: 0.2s; }
.toggle input:checked + span { background: #238636; }
.toggle input:checked + span::before { transform: translateX(16px); }
.hint-inline { font-size: 11px; color: #6e7681; font-weight: 400; }
.hint-text { font-size: 11px; color: #6e7681; margin-top: 4px; }

/* Header transforms */
.transform-group { margin-bottom: 12px; }
.transform-group h4 { font-size: 12px; font-weight: 500; color: #8b949e; margin: 8px 0 6px; display: flex; align-items: center; gap: 6px; }
.transform-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.transform-row select { width: 90px; padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }
.transform-row input { flex: 1; padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }
.transform-row input:first-of-type { max-width: 200px; }
</style>
