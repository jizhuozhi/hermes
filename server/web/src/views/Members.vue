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
      <p class="page-desc">Manage all users across the system. Toggle the Admin checkbox to promote a user to super admin.</p>

      <!-- Create builtin user (only in builtin auth mode) -->
      <div v-if="isBuiltinAuth" class="add-member-section">
        <h3>Create User</h3>
        <div class="create-user-form">
          <input v-model="newUser.email" class="form-input" placeholder="Email" />
          <input v-model="newUser.name" class="form-input" placeholder="Name (optional)" />
          <input v-model="newUser.password" type="password" class="form-input" placeholder="Password (min 6)" />
          <label class="admin-toggle compact">
            <input type="checkbox" v-model="newUser.isAdmin">
            <span>Admin</span>
          </label>
          <button class="btn btn-primary" @click="createUser" :disabled="!newUser.email || !newUser.password">Create</button>
        </div>
        <div v-if="createUserError" class="error-msg">{{ createUserError }}</div>
        <div v-if="createUserSuccess" class="success-msg">{{ createUserSuccess }}</div>
      </div>

      <table v-if="allUsers.length" class="data-table">
        <thead>
          <tr>
            <th>Username</th>
            <th>Email</th>
            <th>Name</th>
            <th>Admin</th>
            <th>Last Seen</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in allUsers" :key="u.sub">
            <td>{{ u.username }}</td>
            <td class="dim">{{ u.email }}</td>
            <td>
              <template v-if="editingUser === u.sub">
                <input v-model="editForm.name" class="inline-input" placeholder="Name" @keyup.enter="saveEdit(u)" />
              </template>
              <template v-else>{{ u.name }}</template>
            </td>
            <td>
              <label class="admin-toggle">
                <input type="checkbox" :checked="u.is_admin" @change="toggleAdmin(u)" :disabled="u.sub === mySub">
                <span>{{ u.is_admin ? 'Yes' : 'No' }}</span>
              </label>
            </td>
            <td class="dim">{{ formatTime(u.last_seen) }}</td>
            <td class="actions-cell">
              <template v-if="u.sub !== mySub">
                <!-- Edit name -->
                <template v-if="editingUser === u.sub">
                  <button class="btn-action save" @click="saveEdit(u)" title="Save">&#10003;</button>
                  <button class="btn-action cancel" @click="cancelEdit" title="Cancel">&#10005;</button>
                </template>
                <button v-else class="btn-action" @click="startEdit(u)" title="Edit name">
                  <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor"><path d="M11.013 1.427a1.75 1.75 0 012.474 0l1.086 1.086a1.75 1.75 0 010 2.474l-8.61 8.61c-.21.21-.47.364-.756.445l-3.251.93a.75.75 0 01-.927-.928l.929-3.25a1.75 1.75 0 01.445-.758l8.61-8.61zm1.414 1.06a.25.25 0 00-.354 0L3.463 11.1a.25.25 0 00-.064.108l-.563 1.97 1.971-.564a.25.25 0 00.108-.064l8.61-8.61a.25.25 0 000-.354l-1.086-1.086z"/></svg>
                </button>
                <!-- Reset password (builtin users only) -->
                <button v-if="isBuiltinSub(u.sub)" class="btn-action" @click="promptResetPassword(u)" title="Reset password">
                  <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor"><path d="M8 1a3.5 3.5 0 00-3.5 3.5V7H3.75A1.75 1.75 0 002 8.75v5.5c0 .966.784 1.75 1.75 1.75h8.5A1.75 1.75 0 0014 14.25v-5.5A1.75 1.75 0 0012.25 7H11V4.5A3.5 3.5 0 008 1zm2 6V4.5a2 2 0 10-4 0V7h4z"/></svg>
                </button>
                <!-- Force password change (builtin users only) -->
                <button v-if="isBuiltinSub(u.sub)" class="btn-action" @click="forcePasswordChange(u)" :title="u.must_change_password ? 'Clear force-change flag' : 'Force password change on next login'">
                  <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor" :style="{ opacity: u.must_change_password ? 1 : 0.5 }"><path d="M5.029 2.217a6.5 6.5 0 019.437 5.11.75.75 0 101.492-.154 8 8 0 00-14.09-4.171L1.5 1.5v3.25a.75.75 0 00.75.75H5.5l-1.471-1.283zM1.195 8.673a6.5 6.5 0 009.466 4.465l.578 1.345a8 8 0 01-11.536-5.656.75.75 0 011.492-.154z"/></svg>
                </button>
                <!-- Delete -->
                <button class="btn-action danger" @click="deleteUser(u)" title="Delete user">
                  <svg viewBox="0 0 16 16" width="14" height="14" fill="currentColor"><path d="M6.5 1.75a.25.25 0 01.25-.25h2.5a.25.25 0 01.25.25V3h-3V1.75zM11 3V1.75A1.75 1.75 0 009.25 0h-2.5A1.75 1.75 0 005 1.75V3H2.75a.75.75 0 000 1.5h.68l.806 7.243A1.75 1.75 0 005.98 13.5h4.04a1.75 1.75 0 001.744-1.757L12.57 4.5h.68a.75.75 0 000-1.5H11z"/></svg>
                </button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="userActionError" class="error-msg" style="margin-top: 8px;">{{ userActionError }}</div>

      <!-- Reset password modal -->
      <div v-if="resetPasswordTarget" class="modal-overlay" @click.self="resetPasswordTarget = null">
        <div class="modal-box">
          <h3>Reset Password</h3>
          <p class="modal-desc">Set a new password for <strong>{{ resetPasswordTarget.email || resetPasswordTarget.username }}</strong></p>
          <input v-model="resetPasswordValue" type="password" class="form-input" placeholder="New password (min 6 chars)" @keyup.enter="confirmResetPassword" autofocus />
          <div class="modal-actions">
            <button class="btn btn-secondary" @click="resetPasswordTarget = null">Cancel</button>
            <button class="btn btn-primary" @click="confirmResetPassword" :disabled="!resetPasswordValue || resetPasswordValue.length < 6">Reset</button>
          </div>
          <div v-if="resetPasswordError" class="error-msg">{{ resetPasswordError }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import api, { getAuthConfig } from '../api.js'

export default {
  data() {
    return {
      members: [],
      allUsers: [],
      groupBindings: [],
      whoami: null,
      loading: true,
      authMode: null,
      newMemberSub: '',
      newMemberRole: 'viewer',
      addError: '',
      newGroupName: '',
      newGroupRole: 'viewer',
      groupError: '',
      // Create user
      newUser: { email: '', name: '', password: '', isAdmin: false },
      createUserError: '',
      createUserSuccess: '',
      // Edit user
      editingUser: null,
      editForm: { name: '' },
      // Reset password
      resetPasswordTarget: null,
      resetPasswordValue: '',
      resetPasswordError: '',
      // General user action error
      userActionError: '',
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
    isBuiltinAuth() {
      return this.authMode === 'builtin'
    },
    availableUsers() {
      const memberSubs = new Set(this.members.map(m => m.user_sub))
      return this.allUsers.filter(u => !memberSubs.has(u.sub))
    },
    knownGroups() {
      const myGroups = this.whoami?.groups || []
      const boundGroups = new Set(this.groupBindings.map(b => b.group))
      return myGroups.filter(g => !boundGroups.has(g)).sort()
    }
  },
  async created() {
    try {
      const cfg = await getAuthConfig()
      this.authMode = cfg.mode || null
    } catch {}
    await this.loadData()
  },
  methods: {
    isBuiltinSub(sub) {
      return sub && sub.startsWith('builtin:')
    },
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
    // Create builtin user
    async createUser() {
      this.createUserError = ''
      this.createUserSuccess = ''
      try {
        await api.createBuiltinUser(this.newUser.email, this.newUser.password, this.newUser.name, this.newUser.isAdmin)
        this.createUserSuccess = `User ${this.newUser.email} created successfully.`
        this.newUser = { email: '', name: '', password: '', isAdmin: false }
        await this.loadData()
        setTimeout(() => { this.createUserSuccess = '' }, 3000)
      } catch (e) {
        this.createUserError = e.response?.data?.error || 'Failed to create user'
      }
    },
    // Edit user
    startEdit(u) {
      this.editingUser = u.sub
      this.editForm.name = u.name || ''
      this.userActionError = ''
    },
    cancelEdit() {
      this.editingUser = null
      this.editForm.name = ''
    },
    async saveEdit(u) {
      this.userActionError = ''
      try {
        await api.updateUser(u.sub, { name: this.editForm.name })
        this.editingUser = null
        await this.loadData()
      } catch (e) {
        this.userActionError = e.response?.data?.error || 'Failed to update user'
      }
    },
    // Reset password
    promptResetPassword(u) {
      this.resetPasswordTarget = u
      this.resetPasswordValue = ''
      this.resetPasswordError = ''
    },
    async confirmResetPassword() {
      this.resetPasswordError = ''
      if (!this.resetPasswordValue || this.resetPasswordValue.length < 6) {
        this.resetPasswordError = 'Password must be at least 6 characters'
        return
      }
      try {
        await api.resetUserPassword(this.resetPasswordTarget.sub, this.resetPasswordValue)
        this.resetPasswordTarget = null
        this.resetPasswordValue = ''
      } catch (e) {
        this.resetPasswordError = e.response?.data?.error || 'Failed to reset password'
      }
    },
    // Force password change
    async forcePasswordChange(u) {
      this.userActionError = ''
      const newVal = !u.must_change_password
      try {
        await api.forcePasswordChange(u.sub, newVal)
        u.must_change_password = newVal
      } catch (e) {
        this.userActionError = e.response?.data?.error || 'Failed to update force-change flag'
      }
    },
    // Delete user
    async deleteUser(u) {
      if (!confirm(`Delete user "${u.email || u.username}"? This cannot be undone.`)) return
      this.userActionError = ''
      try {
        await api.deleteUser(u.sub)
        await this.loadData()
      } catch (e) {
        this.userActionError = e.response?.data?.error || 'Failed to delete user'
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
.admin-toggle.compact { font-size: 12px; white-space: nowrap; }

.create-user-form {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.create-user-form .form-input { flex: 1; min-width: 120px; }

.success-msg { margin-top: 8px; color: #3fb950; font-size: 13px; }

.actions-cell {
  display: flex;
  gap: 4px;
  align-items: center;
}

.btn-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  background: transparent;
  border: 1px solid #30363d;
  border-radius: 4px;
  color: #8b949e;
  cursor: pointer;
  transition: all 0.15s;
}
.btn-action:hover { color: #e1e4e8; border-color: #58a6ff; background: #1f6feb22; }
.btn-action.danger:hover { color: #f85149; border-color: #f85149; background: #f8514922; }
.btn-action.save { color: #3fb950; border-color: #3fb95044; font-weight: bold; font-size: 14px; }
.btn-action.save:hover { border-color: #3fb950; background: #23863622; }
.btn-action.cancel { color: #f85149; border-color: #f8514944; font-weight: bold; font-size: 14px; }
.btn-action.cancel:hover { border-color: #f85149; background: #f8514922; }

.inline-input {
  padding: 4px 8px;
  background: #0d1117;
  border: 1px solid #58a6ff;
  border-radius: 4px;
  color: #e1e4e8;
  font-size: 13px;
  width: 100%;
  max-width: 180px;
}
.inline-input:focus { outline: none; box-shadow: 0 0 0 2px #1f6feb44; }

/* Modal */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal-box {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 10px;
  padding: 24px;
  min-width: 360px;
  max-width: 480px;
}
.modal-box h3 { color: #e1e4e8; font-size: 18px; margin-bottom: 8px; }
.modal-desc { color: #8b949e; font-size: 13px; margin-bottom: 16px; }
.modal-box .form-input { width: 100%; margin-bottom: 12px; box-sizing: border-box; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; }

.btn-secondary {
  padding: 8px 16px;
  border: 1px solid #30363d;
  border-radius: 6px;
  background: transparent;
  color: #e1e4e8;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s;
}
.btn-secondary:hover { border-color: #8b949e; }
</style>
