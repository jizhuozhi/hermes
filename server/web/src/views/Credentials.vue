<template>
  <div class="credentials-page">
    <h1>API Credentials</h1>

    <section class="section">
      <div class="section-header">
        <h2>AK/SK Pairs</h2>
        <button class="btn btn-sm btn-primary" @click="showCreateDialog = true">+ Create Credential</button>
      </div>
      <p class="section-desc">Manage AK/SK pairs for controller HMAC-SHA256 authentication. Authorization is namespace-scoped (controllers select namespace via X-Hermes-Namespace header).</p>

      <div v-if="error" class="alert alert-error">{{ error }}</div>
      <div v-if="loading" class="loading">Loading credentials...</div>

      <div v-if="!loading && !credentials.length && !error" class="empty">
        <p>No API credentials configured. Authentication is currently disabled.</p>
        <p class="hint">Create a credential and configure the AK/SK on your controller(s).</p>
      </div>

      <table v-if="credentials.length" class="data-table">
        <thead>
          <tr>
            <th>Access Key</th>
            <th>Description</th>
            <th>Scopes</th>
            <th>Status</th>
            <th>Created</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in credentials" :key="c.id">
            <td><code class="ak-code">{{ c.access_key }}</code></td>
            <td>{{ c.description || '—' }}</td>
            <td>
              <span v-if="c.scopes && c.scopes.length === allScopes.length" class="badge badge-ok">All</span>
              <span v-else-if="!c.scopes || !c.scopes.length" class="badge badge-warn">None</span>
              <span v-else class="scope-tags">
                <span v-for="s in c.scopes" :key="s" class="scope-tag">{{ s }}</span>
              </span>
            </td>
            <td>
              <span class="badge" :class="c.enabled ? 'badge-ok' : 'badge-warn'">
                {{ c.enabled ? 'Enabled' : 'Disabled' }}
              </span>
            </td>
            <td>{{ formatTime(c.created_at) }}</td>
            <td class="actions">
              <button class="btn btn-xs" @click="toggleEnabled(c)">
                {{ c.enabled ? 'Disable' : 'Enable' }}
              </button>
              <button class="btn btn-xs" @click="editCredential(c)">Edit</button>
              <button class="btn btn-xs btn-danger" @click="deleteCredential(c)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Create Credential Dialog -->
    <div v-if="showCreateDialog" class="dialog-overlay" @click.self="closeCreateDialog">
      <div class="dialog">
        <h2>Create API Credential</h2>
        <div class="form-group">
          <label>Description</label>
          <input v-model="createDesc" type="text" placeholder="e.g. production-controller" class="input" />
        </div>
        <div class="form-group">
          <label>Scopes</label>
          <div class="scope-select-actions">
            <button class="btn btn-xs" @click="selectAllCreateScopes">Select All</button>
            <button class="btn btn-xs" @click="clearCreateScopes">Clear</button>
          </div>
          <div class="scope-grid">
            <label v-for="group in scopeGroups" :key="group.label" class="scope-group">
              <span class="scope-group-label">{{ group.label }}</span>
              <label v-for="s in group.scopes" :key="s" class="scope-checkbox">
                <input type="checkbox" :value="s" v-model="createScopes" />
                <span>{{ s }}</span>
              </label>
            </label>
          </div>
        </div>
        <div v-if="createError" class="alert alert-error" style="margin-top:8px">{{ createError }}</div>
        <div class="dialog-actions">
          <button class="btn" @click="closeCreateDialog">Cancel</button>
          <button class="btn btn-primary" @click="doCreate" :disabled="creating">
            {{ creating ? 'Creating...' : 'Create' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Show Secret Dialog (shown once after creation) -->
    <div v-if="newCredential" class="dialog-overlay" @click.self="closeSecretDialog">
      <div class="dialog">
        <h2>Credential Created</h2>
        <div class="alert alert-warn" style="margin-bottom:16px">
          Save the Secret Key now — it will not be shown again.
        </div>
        <div class="form-group">
          <label>Access Key</label>
          <div class="secret-display">
            <code>{{ newCredential.access_key }}</code>
            <button class="btn btn-xs" @click="copy(newCredential.access_key)">Copy</button>
          </div>
        </div>
        <div class="form-group">
          <label>Secret Key</label>
          <div class="secret-display">
            <code>{{ newCredential.secret_key }}</code>
            <button class="btn btn-xs" @click="copy(newCredential.secret_key)">Copy</button>
          </div>
        </div>
        <div class="form-group">
          <label>Controller config.yaml</label>
          <pre class="config-preview">auth:
  access_key: "{{ newCredential.access_key }}"
  secret_key: "{{ newCredential.secret_key }}"</pre>
          <button class="btn btn-xs" @click="copyConfig">Copy Config</button>
        </div>
        <div class="dialog-actions">
          <button class="btn btn-primary" @click="closeSecretDialog">I've saved the keys</button>
        </div>
      </div>
    </div>

    <!-- Edit Credential Dialog -->
    <div v-if="editingCredential" class="dialog-overlay" @click.self="closeEditDialog">
      <div class="dialog">
        <h2>Edit Credential</h2>
        <div class="form-group">
          <label>Access Key</label>
          <code class="ak-code">{{ editingCredential.access_key }}</code>
        </div>
        <div class="form-group">
          <label>Description</label>
          <input v-model="editDesc" type="text" class="input" />
        </div>
        <div class="form-group">
          <label>Scopes</label>
          <div class="scope-select-actions">
            <button class="btn btn-xs" @click="selectAllEditScopes">Select All</button>
            <button class="btn btn-xs" @click="clearEditScopes">Clear</button>
          </div>
          <div class="scope-grid">
            <label v-for="group in scopeGroups" :key="group.label" class="scope-group">
              <span class="scope-group-label">{{ group.label }}</span>
              <label v-for="s in group.scopes" :key="s" class="scope-checkbox">
                <input type="checkbox" :value="s" v-model="editScopes" />
                <span>{{ s }}</span>
              </label>
            </label>
          </div>
        </div>
        <div v-if="editError" class="alert alert-error" style="margin-top:8px">{{ editError }}</div>
        <div class="dialog-actions">
          <button class="btn" @click="closeEditDialog">Cancel</button>
          <button class="btn btn-primary" @click="doEdit" :disabled="editing">
            {{ editing ? 'Saving...' : 'Save' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import api from '../api.js'

function buildScopeGroups(scopes) {
  const groups = {}
  for (const s of scopes) {
    const [prefix] = s.split(':')
    if (!groups[prefix]) groups[prefix] = { label: prefix.charAt(0).toUpperCase() + prefix.slice(1), scopes: [] }
    groups[prefix].scopes.push(s)
  }
  return Object.values(groups)
}

export default {
  data() {
    return {
      credentials: [],
      loading: true,
      error: null,
      allScopes: [],
      scopeGroups: [],
      // create
      showCreateDialog: false,
      createDesc: '',
      createScopes: [],
      createError: null,
      creating: false,
      // secret display
      newCredential: null,
      // edit
      editingCredential: null,
      editDesc: '',
      editScopes: [],
      editError: null,
      editing: false,
    }
  },
  async created() {
    await Promise.all([this.loadCredentials(), this.loadScopes()])
  },
  methods: {
    async loadScopes() {
      try {
        const res = await api.listScopes()
        this.allScopes = res.data.scopes || []
        this.scopeGroups = buildScopeGroups(this.allScopes)
      } catch {
        // Fallback: leave empty, user can still type scopes manually or retry.
      }
    },
    async loadCredentials() {
      this.loading = true
      this.error = null
      try {
        const res = await api.listCredentials()
        this.credentials = res.data.credentials || []
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      } finally {
        this.loading = false
      }
    },
    closeCreateDialog() {
      this.showCreateDialog = false
      this.createDesc = ''
      this.createScopes = []
      this.createError = null
    },
    async doCreate() {
      this.creating = true
      this.createError = null
      try {
        const res = await api.createCredential({
          description: this.createDesc,
          scopes: this.createScopes,
        })
        this.newCredential = res.data
        this.closeCreateDialog()
        await this.loadCredentials()
      } catch (e) {
        this.createError = e.response?.data?.error || e.message
      } finally {
        this.creating = false
      }
    },
    closeSecretDialog() {
      this.newCredential = null
    },
    editCredential(c) {
      this.editingCredential = c
      this.editDesc = c.description
      this.editScopes = c.scopes ? [...c.scopes] : []
      this.editError = null
    },
    closeEditDialog() {
      this.editingCredential = null
      this.editDesc = ''
      this.editScopes = []
      this.editError = null
    },
    async doEdit() {
      this.editing = true
      this.editError = null
      try {
        await api.updateCredential(this.editingCredential.id, {
          description: this.editDesc,
          enabled: this.editingCredential.enabled,
          scopes: this.editScopes,
        })
        this.closeEditDialog()
        await this.loadCredentials()
      } catch (e) {
        this.editError = e.response?.data?.error || e.message
      } finally {
        this.editing = false
      }
    },
    async toggleEnabled(c) {
      try {
        await api.updateCredential(c.id, {
          description: c.description,
          enabled: !c.enabled,
          scopes: c.scopes || [],
        })
        await this.loadCredentials()
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    },
    async deleteCredential(c) {
      if (!confirm(`Delete credential "${c.access_key}"? Controllers using this key will be denied access.`)) return
      try {
        await api.deleteCredential(c.id)
        await this.loadCredentials()
      } catch (e) {
        this.error = e.response?.data?.error || e.message
      }
    },
    copy(text) {
      navigator.clipboard.writeText(text)
    },
    copyConfig() {
      const cfg = `auth:\n  access_key: "${this.newCredential.access_key}"\n  secret_key: "${this.newCredential.secret_key}"`
      navigator.clipboard.writeText(cfg)
    },
    selectAllCreateScopes() {
      this.createScopes = [...this.allScopes]
    },
    clearCreateScopes() {
      this.createScopes = []
    },
    selectAllEditScopes() {
      this.editScopes = [...this.allScopes]
    },
    clearEditScopes() {
      this.editScopes = []
    },
    formatTime(t) {
      if (!t) return '—'
      return new Date(t).toLocaleString()
    }
  }
}
</script>

<style scoped>
.credentials-page { max-width: 1200px; }
h1 { font-size: 20px; font-weight: 600; margin-bottom: 24px; }
h2 { font-size: 16px; font-weight: 600; }

.section { margin-bottom: 32px; }
.section-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.section-desc { color: #8b949e; font-size: 13px; margin-bottom: 16px; }

.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th { text-align: left; padding: 10px 12px; border-bottom: 1px solid #30363d; color: #8b949e; font-weight: 500; font-size: 12px; }
.data-table td { padding: 10px 12px; border-bottom: 1px solid #21262d; }
.data-table tr:hover td { background: #161b22; }

.ak-code { background: #21262d; padding: 2px 6px; border-radius: 3px; font-size: 12px; font-family: monospace; }

.badge { padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 500; }
.badge-ok { background: #23863622; color: #3fb950; }
.badge-warn { background: #d29922; color: #0d1117; }

.actions { display: flex; gap: 4px; }

.secret-display { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
.secret-display code { background: #0d1117; padding: 6px 10px; border-radius: 4px; font-size: 13px; font-family: monospace; word-break: break-all; flex: 1; }

.config-preview { background: #0d1117; padding: 12px; border-radius: 6px; font-size: 12px; font-family: monospace; white-space: pre; overflow-x: auto; margin-top: 4px; color: #e1e4e8; }

.empty { color: #8b949e; padding: 40px; text-align: center; font-size: 14px; }
.empty .hint { margin-top: 8px; font-size: 12px; }
.hint { color: #8b949e; font-size: 12px; margin-top: 4px; }

.btn { padding: 8px 16px; border: 1px solid #30363d; border-radius: 6px; background: #21262d; color: #e1e4e8; cursor: pointer; font-size: 14px; }
.btn:hover { background: #30363d; }
.btn-sm { padding: 5px 12px; font-size: 12px; }
.btn-xs { padding: 3px 8px; font-size: 11px; }
.btn-primary { background: #238636; border-color: #238636; }
.btn-primary:hover { background: #2ea043; }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }
.btn-danger { color: #f85149; border-color: #f85149; background: transparent; }
.btn-danger:hover { background: #f8514922; }

.alert { padding: 12px 16px; border-radius: 6px; margin-bottom: 12px; font-size: 13px; }
.alert-error { background: #f8514922; border: 1px solid #f85149; color: #f85149; }
.alert-warn { background: #d2992222; border: 1px solid #d29922; color: #d29922; }
.loading { color: #8b949e; padding: 40px; text-align: center; }

.dialog-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
.dialog { background: #161b22; border: 1px solid #30363d; border-radius: 12px; padding: 24px; width: 620px; max-width: 90vw; max-height: 90vh; overflow-y: auto; }
.dialog h2 { margin-bottom: 16px; }
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 13px; color: #8b949e; margin-bottom: 4px; }
.input { width: 100%; padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #e1e4e8; font-size: 14px; box-sizing: border-box; }
.input:focus { outline: none; border-color: #58a6ff; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }

.scope-select-actions { display: flex; gap: 6px; margin-bottom: 8px; }
.scope-grid { display: flex; flex-direction: column; gap: 10px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; padding: 12px; max-height: 280px; overflow-y: auto; }
.scope-group { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; }
.scope-group-label { font-size: 11px; font-weight: 600; color: #8b949e; text-transform: uppercase; min-width: 80px; }
.scope-checkbox { display: flex; align-items: center; gap: 4px; font-size: 12px; color: #e1e4e8; cursor: pointer; }
.scope-checkbox input[type="checkbox"] { accent-color: #238636; cursor: pointer; }

.scope-tags { display: flex; flex-wrap: wrap; gap: 3px; }
.scope-tag { background: #21262d; border: 1px solid #30363d; padding: 1px 6px; border-radius: 3px; font-size: 11px; color: #8b949e; font-family: monospace; }
</style>
