<template>
  <div class="members-page">
    <div class="page-header">
      <h1>Members</h1>
      <p class="page-desc">Manage who can access this namespace and their roles.</p>
    </div>

    <!-- Current user info -->
    <div v-if="myRole" class="my-role-badge">
      Your role: <strong>{{ myRole }}</strong>
      <span v-if="roleSource" class="role-source">({{ roleSourceLabel }})</span>
    </div>

    <!-- Add member -->
    <div v-if="canManage" class="add-member-section">
      <h3>Add Member</h3>
      <div class="add-member-form">
        <select v-model="newMemberSub" class="form-select">
          <option value="">Select a user...</option>
          <option v-for="u in availableUsers" :key="u.sub" :value="u.sub">
            {{ u.username || u.email }} ({{ u.name }})
          </option>
        </select>
        <select v-model="newMemberRole" class="form-select role-select">
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
          <option value="owner">Owner</option>
        </select>
        <button class="btn btn-primary" @click="addMember" :disabled="!newMemberSub">Add</button>
      </div>
      <div v-if="addError" class="error-msg">{{ addError }}</div>
    </div>

    <!-- Members list -->
    <div class="members-list">
      <div v-if="loading" class="loading">Loading...</div>
      <table v-else-if="members.length" class="data-table">
        <thead>
          <tr>
            <th>User</th>
            <th>Email</th>
            <th>Role</th>
            <th v-if="canManage">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in members" :key="m.user_sub">
            <td>
              <div class="user-cell">
                <span class="member-avatar">{{ (m.username || m.name || '?')[0].toUpperCase() }}</span>
                <span>{{ m.username || m.name }}</span>
              </div>
            </td>
            <td class="dim">{{ m.email }}</td>
            <td>
              <select v-if="canManage && m.user_sub !== mySub" v-model="m.role" @change="updateRole(m)" class="role-inline">
                <option value="viewer">Viewer</option>
                <option value="editor">Editor</option>
                <option value="owner">Owner</option>
              </select>
              <span v-else class="role-badge" :class="'role-' + m.role">{{ m.role }}</span>
            </td>
            <td v-if="canManage">
              <button v-if="m.user_sub !== mySub" class="btn btn-danger-sm" @click="removeMember(m)">Remove</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty-state">No members in this namespace yet.</div>
    </div>

    <!-- Group Bindings section -->
    <div class="group-bindings-section">
      <h2>Group Bindings</h2>
      <p class="page-desc">
        Map OIDC groups to namespace roles. Users who belong to a bound group automatically inherit the configured role.
      </p>

      <!-- Add group binding (only for admin/owner) -->
      <div v-if="canManage" class="add-member-section">
        <h3>Add Group Binding</h3>
        <div class="add-member-form">
          <input v-model="newGroupName" class="form-input" placeholder="OIDC group name (e.g. /engineering)" @keyup.enter="addGroupBinding" />
          <select v-model="newGroupRole" class="form-select role-select">
            <option value="viewer">Viewer</option>
            <option value="editor">Editor</option>
            <option value="owner">Owner</option>
          </select>
          <button class="btn btn-primary" @click="addGroupBinding" :disabled="!newGroupName.trim()">Bind</button>
        </div>
        <div v-if="groupError" class="error-msg">{{ groupError }}</div>

        <!-- Known groups hint -->
        <div v-if="knownGroups.length" class="known-groups">
          <span class="known-groups-label">Your OIDC groups:</span>
          <span v-for="g in knownGroups" :key="g" class="group-chip" @click="newGroupName = g">{{ g }}</span>
        </div>
      </div>

      <!-- Bindings table -->
      <table v-if="groupBindings.length" class="data-table">
        <thead>
          <tr>
            <th>Group</th>
            <th>Role</th>
            <th v-if="canManage">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in groupBindings" :key="b.id">
            <td>
              <div class="user-cell">
                <span class="group-avatar">G</span>
                <span>{{ b.group }}</span>
              </div>
            </td>
            <td>
              <select v-if="canManage" v-model="b.role" @change="updateGroupBinding(b)" class="role-inline">
                <option value="viewer">Viewer</option>
                <option value="editor">Editor</option>
                <option value="owner">Owner</option>
              </select>
              <span v-else class="role-badge" :class="'role-' + b.role">{{ b.role }}</span>
            </td>
            <td v-if="canManage">
              <button class="btn btn-danger-sm" @click="removeGroupBinding(b)">Remove</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty-state">No group bindings configured for this namespace.</div>
    </div>

    <!-- Admin section: All Users -->
    <div v-if="isAdmin" class="admin-section">
      <h2>All Users (Admin)</h2>
      <p class="page-desc">Users are automatically synced on OIDC login. Toggle the Admin checkbox to promote a user to super admin.</p>
      <table v-if="allUsers.length" class="data-table">
        <thead>
          <tr>
            <th>Username</th>
            <th>Email</th>
            <th>Name</th>
            <th>Admin</th>
            <th>Last Seen</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in allUsers" :key="u.sub">
            <td>{{ u.username }}</td>
            <td class="dim">{{ u.email }}</td>
            <td>{{ u.name }}</td>
            <td>
              <label class="admin-toggle">
                <input type="checkbox" :checked="u.is_admin" @change="toggleAdmin(u)" :disabled="u.sub === mySub">
                <span>{{ u.is_admin ? 'Yes' : 'No' }}</span>
              </label>
            </td>
            <td class="dim">{{ formatTime(u.last_seen) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script>
import api from '../api.js'

export default {
  data() {
    return {
      members: [],
      allUsers: [],
      groupBindings: [],
      whoami: null,
      loading: true,
      newMemberSub: '',
      newMemberRole: 'viewer',
      addError: '',
      newGroupName: '',
      newGroupRole: 'viewer',
      groupError: '',
    }
  },
  computed: {
    mySub() {
      return this.whoami?.sub || ''
    },
    myRole() {
      return this.whoami?.role || ''
    },
    roleSource() {
      return this.whoami?.role_source || ''
    },
    roleSourceLabel() {
      const labels = { admin: 'super admin', direct: 'direct member', group: 'via group' }
      return labels[this.roleSource] || this.roleSource
    },
    isAdmin() {
      return this.whoami?.is_admin || false
    },
    canManage() {
      return this.isAdmin || this.myRole === 'owner'
    },
    availableUsers() {
      const memberSubs = new Set(this.members.map(m => m.user_sub))
      return this.allUsers.filter(u => !memberSubs.has(u.sub))
    },
    knownGroups() {
      // Current user's groups from JWT claims (via whoami).
      const myGroups = this.whoami?.groups || []
      const boundGroups = new Set(this.groupBindings.map(b => b.group))
      return myGroups.filter(g => !boundGroups.has(g)).sort()
    }
  },
  async created() {
    await this.loadData()
  },
  methods: {
    async loadData() {
      this.loading = true
      try {
        const [membersRes, whoamiRes, bindingsRes] = await Promise.all([
          api.listMembers(),
          api.whoami(),
          api.listGroupBindings(),
        ])
        this.members = membersRes.data.members || []
        this.whoami = whoamiRes.data
        this.groupBindings = bindingsRes.data.bindings || []

        // Load all users if admin or owner (needed for the add member dropdown).
        if (this.isAdmin || this.myRole === 'owner') {
          const usersRes = await api.listUsers()
          this.allUsers = usersRes.data.users || []
        }
      } catch (e) {
        // silently handle
      }
      this.loading = false
    },
    async addMember() {
      this.addError = ''
      try {
        await api.addMember(this.newMemberSub, this.newMemberRole)
        this.newMemberSub = ''
        this.newMemberRole = 'viewer'
        await this.loadData()
      } catch (e) {
        this.addError = e.response?.data?.error || 'Failed to add member'
      }
    },
    async updateRole(m) {
      try {
        await api.addMember(m.user_sub, m.role)
      } catch (e) {
        await this.loadData()
      }
    },
    async removeMember(m) {
      if (!confirm(`Remove ${m.username || m.user_sub} from this namespace?`)) return
      try {
        await api.removeMember(m.user_sub)
        await this.loadData()
      } catch (e) {
        // silently handle
      }
    },
    async addGroupBinding() {
      this.groupError = ''
      const group = this.newGroupName.trim()
      if (!group) return
      try {
        await api.setGroupBinding(group, this.newGroupRole)
        this.newGroupName = ''
        this.newGroupRole = 'viewer'
        await this.loadData()
      } catch (e) {
        this.groupError = e.response?.data?.error || 'Failed to add group binding'
      }
    },
    async updateGroupBinding(b) {
      try {
        await api.setGroupBinding(b.group, b.role)
      } catch (e) {
        await this.loadData()
      }
    },
    async removeGroupBinding(b) {
      if (!confirm(`Remove group binding "${b.group}"?`)) return
      try {
        await api.removeGroupBinding(b.group)
        await this.loadData()
      } catch (e) {
        // silently handle
      }
    },
    async toggleAdmin(u) {
      try {
        await api.setAdmin(u.sub, !u.is_admin)
        u.is_admin = !u.is_admin
      } catch (e) {
        // revert
      }
    },
    formatTime(t) {
      if (!t) return '-'
      return new Date(t).toLocaleString()
    }
  }
}
</script>

<style scoped>
.members-page { max-width: 900px; }
.page-header h1 { font-size: 24px; color: #e1e4e8; margin-bottom: 4px; }
.page-desc { color: #8b949e; font-size: 14px; margin-bottom: 20px; }
.my-role-badge {
  display: inline-block;
  padding: 6px 12px;
  background: #1f6feb22;
  border: 1px solid #1f6feb44;
  border-radius: 6px;
  color: #58a6ff;
  font-size: 13px;
  margin-bottom: 20px;
}
.my-role-badge strong { text-transform: capitalize; }
.role-source { font-size: 12px; color: #8b949e; margin-left: 4px; }

.add-member-section {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 16px 20px;
  margin-bottom: 24px;
}
.add-member-section h3 { font-size: 15px; color: #e1e4e8; margin-bottom: 12px; }
.add-member-form {
  display: flex;
  gap: 8px;
  align-items: center;
}
.form-select, .form-input {
  padding: 8px 10px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e1e4e8;
  font-size: 13px;
}
.form-input { flex: 1; min-width: 0; }
.form-select:focus, .form-input:focus { outline: none; border-color: #58a6ff; }
.role-select { width: 120px; }

.data-table {
  width: 100%;
  border-collapse: collapse;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  overflow: hidden;
}
.data-table th {
  text-align: left;
  padding: 10px 16px;
  font-size: 12px;
  color: #8b949e;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  border-bottom: 1px solid #30363d;
  background: #0d1117;
}
.data-table td {
  padding: 10px 16px;
  font-size: 14px;
  color: #e1e4e8;
  border-bottom: 1px solid #21262d;
}
.data-table tr:last-child td { border-bottom: none; }

.user-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.member-avatar, .group-avatar {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}
.member-avatar { background: #1f6feb; }
.group-avatar { background: #8957e5; }

.role-badge {
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  text-transform: capitalize;
}
.role-owner { background: #da361922; color: #f0883e; }
.role-editor { background: #1f6feb22; color: #58a6ff; }
.role-viewer { background: #23863622; color: #3fb950; }

.role-inline {
  padding: 4px 8px;
  background: #0d1117;
  border: 1px solid #30363d;
  border-radius: 4px;
  color: #e1e4e8;
  font-size: 13px;
}

.btn {
  padding: 8px 16px;
  border: none;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s;
}
.btn-primary { background: #238636; color: #fff; }
.btn-primary:hover:not(:disabled) { background: #2ea043; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-danger-sm {
  padding: 4px 10px;
  background: transparent;
  border: 1px solid #f8514944;
  border-radius: 4px;
  color: #f85149;
  font-size: 12px;
  cursor: pointer;
}
.btn-danger-sm:hover { background: #f8514922; }

.dim { color: #8b949e; }
.error-msg { margin-top: 8px; color: #f85149; font-size: 13px; }
.loading { color: #8b949e; padding: 20px 0; }
.empty-state { color: #8b949e; padding: 20px 0; font-size: 14px; }

.group-bindings-section {
  margin-top: 32px;
  padding-top: 24px;
  border-top: 1px solid #30363d;
}
.group-bindings-section h2 { font-size: 20px; color: #e1e4e8; margin-bottom: 4px; }

.known-groups {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.known-groups-label { font-size: 12px; color: #8b949e; margin-right: 4px; }
.group-chip {
  display: inline-block;
  padding: 2px 8px;
  background: #8957e522;
  border: 1px solid #8957e544;
  border-radius: 12px;
  font-size: 12px;
  color: #d2a8ff;
  cursor: pointer;
  transition: all 0.15s;
}
.group-chip:hover { background: #8957e544; border-color: #8957e5; }

.groups-cell {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.group-tag {
  display: inline-block;
  padding: 1px 6px;
  background: #8957e522;
  border-radius: 8px;
  font-size: 11px;
  color: #d2a8ff;
}

.admin-section {
  margin-top: 40px;
  padding-top: 24px;
  border-top: 1px solid #30363d;
}
.admin-section h2 { font-size: 20px; color: #e1e4e8; margin-bottom: 4px; }

.admin-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 13px;
  color: #e1e4e8;
}
.admin-toggle input { cursor: pointer; }
</style>
