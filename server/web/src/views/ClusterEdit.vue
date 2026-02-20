<template>
  <div>
    <div class="page-header">
      <h1>{{ isEdit ? `Edit Cluster: ${clusterName}` : 'New Cluster' }}</h1>
      <div class="mode-tabs">
        <button type="button" class="mode-tab" :class="{ active: mode === 'form' }" @click="switchMode('form')">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/></svg>
          Form
        </button>
        <button type="button" class="mode-tab" :class="{ active: mode === 'json' }" @click="switchMode('json')">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>
          JSON
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>
    <div v-if="validationErrors.length" class="alert alert-error">
      <div v-for="e in validationErrors" :key="e.field">{{ e.field }}: {{ e.message }}</div>
    </div>

    <!-- JSON Mode -->
    <div v-if="mode === 'json'" class="json-mode">
      <div class="json-toolbar">
        <button type="button" class="btn btn-small" @click="formatJson">Format</button>
        <button type="button" class="btn btn-small" @click="copyJson">Copy</button>
        <span v-if="jsonMsg" :class="['json-msg', jsonMsg.type]">{{ jsonMsg.text }}</span>
      </div>
      <div class="json-editor-wrap">
        <div class="json-gutter" ref="jsonGutter">
          <div v-for="n in jsonLineCount" :key="n" class="json-line-num">{{ n }}</div>
        </div>
        <textarea
          ref="jsonEditor"
          v-model="jsonText"
          class="json-textarea"
          spellcheck="false"
          @scroll="syncJsonScroll"
          @keydown="onJsonKeydown"
        ></textarea>
      </div>
      <div class="json-status">
        <span class="json-hint">Edit the cluster JSON directly. Click Save to apply.</span>
        <span class="json-lines">{{ jsonLineCount }} lines</span>
      </div>
      <div class="form-actions">
        <button type="button" class="btn" @click="$router.push('/clusters')">Cancel</button>
        <button type="button" class="btn btn-primary" @click="saveJson" :disabled="saving">
          {{ saving ? 'Saving...' : (isEdit ? 'Update' : 'Create') }}
        </button>
      </div>
    </div>

    <!-- Form Mode -->
    <form v-if="mode === 'form'" @submit.prevent="save" class="form">
      <div class="form-section">
        <h2>Basic</h2>
        <div class="form-grid">
          <div class="field">
            <label>Name</label>
            <input v-model="cluster.name" :disabled="isEdit" required placeholder="my-cluster" />
          </div>
          <div class="field">
            <label>LB Type</label>
            <select v-model="cluster.type">
              <option value="roundrobin">roundrobin</option>
              <option value="random">random</option>
              <option value="least_request">least_request</option>
              <option value="peak_ewma">peak_ewma</option>
            </select>
          </div>
          <div class="field">
            <label>Scheme</label>
            <select v-model="cluster.scheme">
              <option value="http">http</option>
              <option value="https">https</option>
            </select>
          </div>
          <div class="field" v-if="cluster.scheme === 'https'">
            <label>TLS Verify</label>
            <div class="toggle-row">
              <label class="toggle">
                <input type="checkbox" v-model="cluster.tls_verify" />
                <span class="toggle-slider"></span>
              </label>
              <span class="toggle-label">{{ cluster.tls_verify ? 'Enabled — verify upstream certificates' : 'Disabled — skip certificate verification (default)' }}</span>
            </div>
          </div>
          <div class="field">
            <label>Pass Host</label>
            <select v-model="cluster.pass_host">
              <option value="pass">pass</option>
              <option value="node">node</option>
              <option value="rewrite">rewrite</option>
            </select>
          </div>
          <div class="field" v-if="cluster.pass_host === 'rewrite'">
            <label>Upstream Host</label>
            <input v-model="cluster.upstream_host" placeholder="upstream.example.com" />
          </div>
        </div>
      </div>

      <div class="form-section">
        <h2>Timeout</h2>
        <div class="form-grid">
          <div class="field">
            <label>Connect (s)</label>
            <input v-model.number="cluster.timeout.connect" type="number" step="0.1" />
          </div>
          <div class="field">
            <label>Send (s)</label>
            <input v-model.number="cluster.timeout.send" type="number" step="0.1" />
          </div>
          <div class="field">
            <label>Read (s)</label>
            <input v-model.number="cluster.timeout.read" type="number" step="0.1" />
          </div>
        </div>
      </div>

      <div class="form-section">
        <h2>Upstream Source</h2>
        <div class="form-grid">
          <div class="field">
            <label>Discovery Type</label>
            <select v-model="discoveryType">
              <option value="">None (static nodes)</option>
              <option value="consul">consul</option>
            </select>
          </div>
          <div class="field" v-if="discoveryType">
            <label>Service Name</label>
            <input v-model="serviceName" placeholder="my-service" />
          </div>
        </div>

        <div v-if="discoveryType" class="metadata-section">
          <h3>Metadata Match <span class="hint">(all keys must match — AND; values per key are alternatives — OR)</span></h3>
          <div v-for="(rule, i) in metadataRules" :key="i" class="meta-rule">
            <div class="meta-key">
              <input v-model="rule.key" placeholder="key (e.g. region)" @input="syncMetadata" />
            </div>
            <div class="meta-values">
              <span v-for="(val, j) in rule.values" :key="j" class="meta-tag">
                {{ val }}
                <button type="button" class="tag-remove" @click="removeMetaValue(i, j)">×</button>
              </span>
              <input
                class="meta-value-input"
                :placeholder="rule.values.length ? 'add another...' : 'value (press Enter to add)'"
                @keydown.enter.prevent="addMetaValue(i, $event)"
              />
            </div>
            <button type="button" class="btn btn-small btn-danger" @click="removeMetaRule(i)">×</button>
          </div>
          <button type="button" class="btn btn-small" @click="addMetaRule">+ Add Filter</button>
        </div>

        <div v-if="!discoveryType">
          <h3>Static Nodes</h3>
          <div v-for="(node, i) in cluster.nodes" :key="i" class="node-row">
            <input v-model="node.host" placeholder="host" />
            <input v-model.number="node.port" type="number" placeholder="port" />
            <input v-model.number="node.weight" type="number" placeholder="weight" />
            <button type="button" class="btn btn-small btn-danger" @click="cluster.nodes.splice(i, 1)">×</button>
          </div>
          <button type="button" class="btn btn-small" @click="cluster.nodes.push({host:'',port:80,weight:100})">+ Add Node</button>
        </div>
      </div>

      <!-- Health Check -->
      <div class="form-section">
        <h2>
          Health Check
          <label class="toggle" style="margin-left: auto;">
            <input type="checkbox" v-model="enableHealthCheck" />
            <span class="toggle-slider"></span>
          </label>
        </h2>
        <template v-if="enableHealthCheck">
          <!-- Active Health Check -->
          <div class="sub-section">
            <h3>
              Active Health Check
              <label class="toggle" style="margin-left: 12px;">
                <input type="checkbox" v-model="enableActiveHC" />
                <span class="toggle-slider"></span>
              </label>
            </h3>
            <div v-if="enableActiveHC" class="form-grid">
              <div class="field">
                <label>Interval (s)</label>
                <input v-model.number="activeHC.interval" type="number" min="1" />
              </div>
              <div class="field">
                <label>Path</label>
                <input v-model="activeHC.path" placeholder="/health" />
              </div>
              <div class="field">
                <label>Timeout (s)</label>
                <input v-model.number="activeHC.timeout" type="number" min="1" />
              </div>
              <div class="field">
                <label>Port (optional)</label>
                <input v-model.number="activeHCPort" type="number" min="1" max="65535" placeholder="same as node" />
                <span class="hint">Override port for health probes (leave empty to use node port)</span>
              </div>
              <div class="field">
                <label>Healthy Threshold</label>
                <input v-model.number="activeHC.healthy_threshold" type="number" min="1" />
              </div>
              <div class="field">
                <label>Unhealthy Threshold</label>
                <input v-model.number="activeHC.unhealthy_threshold" type="number" min="1" />
              </div>
              <div class="field">
                <label>Concurrency</label>
                <input v-model.number="activeHC.concurrency" type="number" min="0" placeholder="64" />
              </div>
              <div class="field field-wide">
                <label>Healthy Statuses</label>
                <input v-model="activeHCHealthyStatuses" placeholder="200" />
                <span class="hint">Comma-separated HTTP status codes, e.g. 200,204</span>
              </div>
            </div>
          </div>
        </template>
      </div>

      <!-- Retry -->
      <div class="form-section">
        <h2>
          Retry
          <label class="toggle" style="margin-left: auto;">
            <input type="checkbox" v-model="enableRetry" />
            <span class="toggle-slider"></span>
          </label>
        </h2>
        <div v-if="enableRetry" class="form-grid">
          <div class="field">
            <label>Max Retries</label>
            <input v-model.number="retryConfig.count" type="number" min="1" />
          </div>
          <div class="field field-wide">
            <label>Retry On Statuses</label>
            <input v-model="retryOnStatuses" placeholder="502,503,504" />
            <span class="hint">Comma-separated HTTP status codes</span>
          </div>
          <div class="field">
            <label>Retry On Connect Failure</label>
            <div class="toggle-row">
              <label class="toggle">
                <input type="checkbox" v-model="retryConfig.retry_on_connect_failure" />
                <span class="toggle-slider"></span>
              </label>
              <span class="toggle-label">{{ retryConfig.retry_on_connect_failure ? 'Yes' : 'No' }}</span>
            </div>
          </div>
          <div class="field">
            <label>Retry On Timeout</label>
            <div class="toggle-row">
              <label class="toggle">
                <input type="checkbox" v-model="retryConfig.retry_on_timeout" />
                <span class="toggle-slider"></span>
              </label>
              <span class="toggle-label">{{ retryConfig.retry_on_timeout ? 'Yes' : 'No' }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Circuit Breaker -->
      <div class="form-section">
        <h2>
          Circuit Breaker
          <label class="toggle" style="margin-left: auto;">
            <input type="checkbox" v-model="enableCircuitBreaker" />
            <span class="toggle-slider"></span>
          </label>
        </h2>
        <div v-if="enableCircuitBreaker" class="form-grid">
          <div class="field">
            <label>Failure Threshold</label>
            <input v-model.number="circuitBreakerConfig.failure_threshold" type="number" min="1" />
          </div>
          <div class="field">
            <label>Success Threshold</label>
            <input v-model.number="circuitBreakerConfig.success_threshold" type="number" min="1" />
          </div>
          <div class="field">
            <label>Open Duration (s)</label>
            <input v-model.number="circuitBreakerConfig.open_duration_secs" type="number" min="1" />
          </div>
        </div>
      </div>

      <div class="form-section">
        <h2>Keepalive Pool</h2>
        <div class="form-grid">
          <div class="field">
            <label>Idle Timeout (s)</label>
            <input v-model.number="cluster.keepalive_pool.idle_timeout" type="number" />
          </div>
          <div class="field">
            <label>Max Requests</label>
            <input v-model.number="cluster.keepalive_pool.requests" type="number" />
          </div>
          <div class="field">
            <label>Pool Size</label>
            <input v-model.number="cluster.keepalive_pool.size" type="number" />
          </div>
        </div>
      </div>

      <div class="form-actions">
        <button type="button" class="btn" @click="$router.push('/clusters')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="saving">
          {{ saving ? 'Saving...' : (isEdit ? 'Update' : 'Create') }}
        </button>
      </div>
    </form>
  </div>
</template>

<script>
import api from '../api.js'

const defaultCluster = () => ({
  name: '', type: 'roundrobin', scheme: 'http', pass_host: 'pass', upstream_host: '',
  tls_verify: false,
  timeout: { connect: 6, send: 6, read: 6 },
  nodes: [], discovery_type: null, service_name: null, discovery_args: null,
  keepalive_pool: { idle_timeout: 60, requests: 1000, size: 320 },
  health_check: null, retry: null, circuit_breaker: null,
})

export default {
  data() {
    return {
      mode: 'form',
      cluster: defaultCluster(),
      isEdit: false,
      clusterName: '',
      resourceVersion: 0,
      error: null,
      validationErrors: [],
      saving: false,
      discoveryType: '',
      serviceName: '',
      metadataRules: [],
      enableHealthCheck: false,
      enableActiveHC: false,
      activeHC: { interval: 10, path: '/health', timeout: 3, healthy_threshold: 3, unhealthy_threshold: 3, concurrency: 64, healthy_statuses: [200] },
      activeHCPort: null,
      activeHCHealthyStatuses: '200',
      enableRetry: false,
      retryConfig: { count: 2, retry_on_statuses: [502, 503, 504], retry_on_connect_failure: true, retry_on_timeout: true },
      retryOnStatuses: '502,503,504',
      enableCircuitBreaker: false,
      circuitBreakerConfig: { failure_threshold: 5, success_threshold: 2, open_duration_secs: 30 },
      // JSON mode
      jsonText: '',
      jsonMsg: null,
    }
  },
  computed: {
    jsonLineCount() {
      return this.jsonText ? this.jsonText.split('\n').length : 1
    }
  },
  watch: {
    discoveryType(v) {
      if (v) {
        this.cluster.discovery_type = v
        this.cluster.service_name = this.serviceName || null
      } else {
        this.cluster.discovery_type = null
        this.cluster.service_name = null
        this.cluster.discovery_args = null
        this.metadataRules = []
      }
    },
    serviceName(v) {
      this.cluster.service_name = v || null
    }
  },
  async created() {
    const name = this.$route.params.name
    if (name) {
      this.isEdit = true
      this.clusterName = name
      try {
        const res = await api.getCluster(name)
        this.cluster = res.data.cluster
        this.resourceVersion = res.data.resource_version || 0
        this.discoveryType = this.cluster.discovery_type || ''
        this.serviceName = this.cluster.service_name || ''
        // Hydrate metadataRules from discovery_args.metadata_match
        const mm = this.cluster.discovery_args?.metadata_match
        if (mm) {
          this.metadataRules = Object.entries(mm).map(([key, values]) => ({ key, values: [...values] }))
        }
        // Hydrate health check
        if (this.cluster.health_check) {
          this.enableHealthCheck = true
          if (this.cluster.health_check.active) {
            this.enableActiveHC = true
            this.activeHC = { ...this.activeHC, ...this.cluster.health_check.active }
            this.activeHCHealthyStatuses = (this.activeHC.healthy_statuses || []).join(',')
            this.activeHCPort = this.cluster.health_check.active.port || null
          }
        }
        // Hydrate retry
        if (this.cluster.retry) {
          this.enableRetry = true
          this.retryConfig = { ...this.retryConfig, ...this.cluster.retry }
          this.retryOnStatuses = (this.retryConfig.retry_on_statuses || []).join(',')
        }
        // Hydrate circuit breaker
        if (this.cluster.circuit_breaker) {
          this.enableCircuitBreaker = true
          this.circuitBreakerConfig = { ...this.circuitBreakerConfig, ...this.cluster.circuit_breaker }
        }
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    }
    if (this.$route.query.mode === 'json') {
      this.switchMode('json')
    }
  },
  methods: {
    // --- Mode switching ---
    switchMode(target) {
      if (target === this.mode) return
      if (target === 'json') {
        const payload = JSON.parse(JSON.stringify(this.cluster))
        this.jsonText = JSON.stringify(payload, null, 2)
        this.jsonMsg = null
      } else {
        try {
          const parsed = JSON.parse(this.jsonText)
          this.cluster = parsed
          this.hydrateCluster()
          this.jsonMsg = null
        } catch (e) {
          this.error = 'Invalid JSON — fix it before switching to Form mode: ' + e.message
          return
        }
      }
      this.mode = target
    },

    hydrateCluster() {
      this.discoveryType = this.cluster.discovery_type || ''
      this.serviceName = this.cluster.service_name || ''
      const mm = this.cluster.discovery_args?.metadata_match
      this.metadataRules = mm ? Object.entries(mm).map(([key, values]) => ({ key, values: [...values] })) : []
      if (this.cluster.health_check) {
        this.enableHealthCheck = true
        if (this.cluster.health_check.active) {
          this.enableActiveHC = true
          this.activeHC = { ...this.activeHC, ...this.cluster.health_check.active }
          this.activeHCHealthyStatuses = (this.activeHC.healthy_statuses || []).join(',')
          this.activeHCPort = this.cluster.health_check.active.port || null
        }
      }
      if (this.cluster.retry) {
        this.enableRetry = true
        this.retryConfig = { ...this.retryConfig, ...this.cluster.retry }
        this.retryOnStatuses = (this.retryConfig.retry_on_statuses || []).join(',')
      }
      if (this.cluster.circuit_breaker) {
        this.enableCircuitBreaker = true
        this.circuitBreakerConfig = { ...this.circuitBreakerConfig, ...this.cluster.circuit_breaker }
      }
    },

    // --- JSON mode helpers ---
    formatJson() {
      try {
        const obj = JSON.parse(this.jsonText)
        this.jsonText = JSON.stringify(obj, null, 2)
        this.jsonMsg = { type: 'success', text: 'Formatted.' }
      } catch (e) {
        this.jsonMsg = { type: 'error', text: 'Invalid JSON: ' + e.message }
      }
    },
    async copyJson() {
      try {
        await navigator.clipboard.writeText(this.jsonText)
        this.jsonMsg = { type: 'success', text: 'Copied.' }
      } catch {
        this.jsonMsg = { type: 'error', text: 'Copy failed.' }
      }
    },
    syncJsonScroll() {
      if (this.$refs.jsonGutter && this.$refs.jsonEditor) {
        this.$refs.jsonGutter.scrollTop = this.$refs.jsonEditor.scrollTop
      }
    },
    onJsonKeydown(e) {
      if (e.key === 'Tab') {
        e.preventDefault()
        const ta = e.target
        const start = ta.selectionStart
        const end = ta.selectionEnd
        this.jsonText = this.jsonText.substring(0, start) + '  ' + this.jsonText.substring(end)
        this.$nextTick(() => { ta.selectionStart = ta.selectionEnd = start + 2 })
      }
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        this.saveJson()
      }
    },
    async saveJson() {
      this.saving = true
      this.error = null
      this.validationErrors = []
      let parsed
      try {
        parsed = JSON.parse(this.jsonText)
      } catch (e) {
        this.error = 'Invalid JSON: ' + e.message
        this.saving = false
        return
      }
      try {
        if (this.isEdit) {
          parsed.resource_version = this.resourceVersion
          const res = await api.updateCluster(this.clusterName, parsed)
          this.resourceVersion = res.data.resource_version || this.resourceVersion
        } else {
          await api.createCluster(parsed)
        }
        this.$router.push('/clusters')
      } catch (e) {
        const data = e.response?.data
        if (e.response?.status === 409) {
          this.error = data?.error || 'Conflict: this cluster has been modified by another user. Please refresh and try again.'
        } else if (data?.errors) {
          this.validationErrors = data.errors
        } else {
          this.error = data?.error || e.message
        }
      } finally {
        this.saving = false
      }
    },

    // --- Original form methods ---
    syncMetadata() {
      const match = {}
      for (const rule of this.metadataRules) {
        if (rule.key && rule.values.length > 0) {
          match[rule.key] = [...rule.values]
        }
      }
      if (Object.keys(match).length > 0) {
        this.cluster.discovery_args = { metadata_match: match }
      } else {
        this.cluster.discovery_args = null
      }
    },
    addMetaRule() {
      this.metadataRules.push({ key: '', values: [] })
    },
    removeMetaRule(i) {
      this.metadataRules.splice(i, 1)
      this.syncMetadata()
    },
    addMetaValue(ruleIdx, event) {
      const input = event.target
      const val = input.value.trim()
      if (!val) return
      const rule = this.metadataRules[ruleIdx]
      if (!rule.values.includes(val)) {
        rule.values.push(val)
      }
      input.value = ''
      this.syncMetadata()
    },
    removeMetaValue(ruleIdx, valIdx) {
      this.metadataRules[ruleIdx].values.splice(valIdx, 1)
      this.syncMetadata()
    },
    async save() {
      this.saving = true
      this.error = null
      this.validationErrors = []

      if (!this.discoveryType) {
        this.cluster.discovery_type = null
        this.cluster.service_name = null
        this.cluster.discovery_args = null
      } else {
        this.syncMetadata()
      }

      // Serialize health check
      if (this.enableHealthCheck) {
        const hc = {}
        if (this.enableActiveHC) {
          this.activeHC.healthy_statuses = this.parseStatuses(this.activeHCHealthyStatuses)
          hc.active = { ...this.activeHC }
          hc.active.port = this.activeHCPort || undefined
        }
        this.cluster.health_check = hc
      } else {
        this.cluster.health_check = null
      }

      // Serialize retry
      if (this.enableRetry) {
        this.retryConfig.retry_on_statuses = this.parseStatuses(this.retryOnStatuses)
        this.cluster.retry = { ...this.retryConfig }
      } else {
        this.cluster.retry = null
      }

      // Serialize circuit breaker
      if (this.enableCircuitBreaker) {
        this.cluster.circuit_breaker = { ...this.circuitBreakerConfig }
      } else {
        this.cluster.circuit_breaker = null
      }

      try {
        if (this.isEdit) {
          this.cluster.resource_version = this.resourceVersion
          const res = await api.updateCluster(this.clusterName, this.cluster)
          this.resourceVersion = res.data.resource_version || this.resourceVersion
        } else {
          await api.createCluster(this.cluster)
        }
        this.$router.push('/clusters')
      } catch (e) {
        const data = e.response?.data
        if (e.response?.status === 409) {
          this.error = data?.error || 'Conflict: this cluster has been modified by another user. Please refresh and try again.'
        } else if (data?.errors) {
          this.validationErrors = data.errors
        } else {
          this.error = data?.error || e.message
        }
      } finally {
        this.saving = false
      }
    },
    parseStatuses(str) {
      return (str || '').split(',').map(s => parseInt(s.trim(), 10)).filter(n => !isNaN(n) && n > 0)
    }
  }
}
</script>

<style scoped>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.mode-tabs { display: flex; gap: 0; border: 1px solid #30363d; border-radius: 6px; overflow: hidden; }
.mode-tab { display: flex; align-items: center; gap: 6px; padding: 6px 14px; font-size: 13px; font-weight: 500; background: #21262d; color: #8b949e; border: none; cursor: pointer; transition: all 0.15s; }
.mode-tab:first-child { border-right: 1px solid #30363d; }
.mode-tab.active { background: #1f6feb33; color: #58a6ff; }
.mode-tab:hover:not(.active) { background: #30363d; color: #e1e4e8; }

.page-header h1 { font-size: 24px; font-weight: 600; }
h2 { font-size: 16px; font-weight: 600; margin-bottom: 16px; color: #e1e4e8; display: flex; align-items: center; gap: 12px; }
h3 { font-size: 14px; font-weight: 500; color: #8b949e; margin: 16px 0 8px; }
.form-section { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 20px; margin-bottom: 16px; }
.form-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 16px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.field label { font-size: 12px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; }
.field input, .field select { padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 14px; }
.field input:focus, .field select:focus { outline: none; border-color: #58a6ff; box-shadow: 0 0 0 3px #1f6feb33; }
.node-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.node-row input { padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; width: 180px; }
.node-row input:nth-child(2), .node-row input:nth-child(3) { width: 80px; }
.form-actions { display: flex; justify-content: flex-end; gap: 12px; margin-top: 24px; }
.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn-primary { background: #238636; border-color: #2ea043; color: #fff; }
.btn-primary:hover { background: #2ea043; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-small { padding: 4px 12px; font-size: 12px; }
.btn-danger { color: #f85149; }
.metadata-section { margin-top: 16px; }
.metadata-section .hint { font-size: 11px; color: #6e7681; font-weight: 400; }
.meta-rule { display: flex; gap: 8px; margin-bottom: 10px; align-items: flex-start; }
.meta-key { flex: 0 0 180px; }
.meta-key input { width: 100%; padding: 6px 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 13px; }
.meta-values { flex: 1; display: flex; flex-wrap: wrap; gap: 6px; align-items: center; padding: 4px 8px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; min-height: 34px; }
.meta-tag { display: inline-flex; align-items: center; gap: 4px; padding: 2px 8px; background: #1f6feb33; border: 1px solid #1f6feb; border-radius: 4px; color: #58a6ff; font-size: 12px; }
.tag-remove { background: none; border: none; color: #58a6ff; cursor: pointer; font-size: 14px; padding: 0 2px; line-height: 1; }
.tag-remove:hover { color: #f85149; }
.meta-value-input { background: transparent; border: none; color: #e1e4e8; font-size: 13px; outline: none; flex: 1; min-width: 120px; padding: 2px 0; }
.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 16px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; font-size: 13px; }
.toggle-row { display: flex; align-items: center; gap: 10px; min-height: 34px; }
.toggle { position: relative; display: inline-block; width: 36px; height: 20px; flex-shrink: 0; }
.toggle input { opacity: 0; width: 0; height: 0; }
.toggle-slider { position: absolute; inset: 0; background: #30363d; border-radius: 20px; cursor: pointer; transition: background 0.2s; }
.toggle-slider::before { content: ''; position: absolute; width: 14px; height: 14px; left: 3px; top: 3px; background: #e1e4e8; border-radius: 50%; transition: transform 0.2s; }
.toggle input:checked + .toggle-slider { background: #238636; }
.toggle input:checked + .toggle-slider::before { transform: translateX(16px); }
.toggle-label { font-size: 12px; color: #8b949e; }
.sub-section { margin-top: 16px; padding: 12px 16px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; }
.sub-section h3 { display: flex; align-items: center; margin: 0 0 12px 0; }
.field-wide { grid-column: 1 / -1; }
.field-wide .hint { font-size: 11px; color: #6e7681; margin-top: 4px; }

/* JSON editor */
.json-mode { margin-top: 0; }
.json-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.json-msg { font-size: 12px; margin-left: 8px; }
.json-msg.success { color: #3fb950; }
.json-msg.error { color: #f85149; }
.json-editor-wrap { position: relative; border: 1px solid #30363d; border-radius: 8px 8px 0 0; overflow: hidden; background: #0d1117; height: clamp(400px, 60vh, calc(100vh - 300px)); }
.json-gutter { position: absolute; left: 0; top: 0; bottom: 0; width: 48px; background: #161b22; border-right: 1px solid #21262d; padding: 12px 0; overflow: hidden; user-select: none; z-index: 1; pointer-events: none; }
.json-line-num { height: 20px; line-height: 20px; font-size: 12px; font-family: 'SF Mono', Consolas, monospace; color: #484f58; text-align: right; padding-right: 10px; }
.json-textarea { display: block; width: 100%; height: 100%; padding: 12px 16px 12px 64px; background: transparent; color: #e1e4e8; border: none; outline: none; resize: none; font-size: 13px; font-family: 'SF Mono', Consolas, monospace; line-height: 20px; tab-size: 2; white-space: pre; overflow: auto; box-sizing: border-box; }
.json-textarea::selection { background: #1f6feb44; }
.json-status { display: flex; justify-content: space-between; padding: 6px 12px; font-size: 12px; color: #8b949e; border: 1px solid #30363d; border-top: none; border-radius: 0 0 8px 8px; background: #161b22; }
.json-hint { color: #6e7681; }
.json-lines { color: #484f58; }
</style>
