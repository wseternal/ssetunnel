import React, { useState, useEffect } from 'react';
import {
  ThemeProvider,
  CssBaseline,
  Box,
  Container,
  Typography,
  Card,
  CardContent,
  TextField,
  Button,
  Tabs,
  Tab,
  Paper,
  Chip,
  IconButton,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Select,
  MenuItem,
  FormControl,
  InputLabel,
  Checkbox,
  FormControlLabel,
} from '@mui/material';
import DeleteIcon from '@mui/icons-material/Delete';
import RefreshIcon from '@mui/icons-material/Refresh';
import LockOutlinedIcon from '@mui/icons-material/LockOutlined';
import PersonAddIcon from '@mui/icons-material/PersonAdd';
import EditIcon from '@mui/icons-material/Edit';
import BlockIcon from '@mui/icons-material/Block';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import AddIcon from '@mui/icons-material/Add';
import CableIcon from '@mui/icons-material/Cable';
import GroupIcon from '@mui/icons-material/Group';
import RouterIcon from '@mui/icons-material/Router';
import SecurityIcon from '@mui/icons-material/Security';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import { QRCodeSVG } from 'qrcode.react';
import {
  AdminTable,
  type AdminTableColumn,
  PageHeader,
  StatusPill,
  EmptyState,
  type StatusPillTone,
} from '@doublefin/orca-ui';
import { theme } from './theme/theme';

interface Session {
  id: string;
  bytes_sent: number;
  bytes_received: number;
  created_at: string;
  remote_addr: string;
}

interface User {
  id: number;
  username: string;
  role: string;
  perm_connect: boolean;
  perm_agent: boolean;
  created_at: string;
  disabled_at?: string;
}

interface AgentConfig {
  id: number;
  agent_id: string | null;
  allowed_targets: string[];
  description: string;
  created_at: string;
  updated_at: string;
}

const SESSION_COLUMNS: AdminTableColumn<Session>[] = [
  { key: 'id', label: 'Session ID', render: (r) => (
    <Typography sx={{ fontFamily: 'monospace', fontSize: '0.8125rem' }}>{r.id}</Typography>
  )},
  { key: 'up', label: 'Bytes Received (Up)', render: (r) => `${(r.bytes_received / 1024).toFixed(1)} KB` },
  { key: 'down', label: 'Bytes Sent (Down)', render: (r) => `${(r.bytes_sent / 1024).toFixed(1)} KB` },
  { key: 'at', label: 'Connected At', render: (r) => new Date(r.created_at).toLocaleTimeString() },
  { key: 'addr', label: 'Remote Addr', render: (r) => (
    <Typography sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>{r.remote_addr}</Typography>
  )},
  { key: 'status', label: 'Status', render: () => <StatusPill tone="success" label="Active" /> },
];

export default function App() {
  const [isLoggedIn, setIsLoggedIn] = useState(false);
  const [sessionToken, setSessionToken] = useState(() => localStorage.getItem('sessionToken') || '');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [tabIndex, setTabIndex] = useState(0);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [agents, setAgents] = useState<AgentConfig[]>([]);
  const [error, setError] = useState('');

  // TOTP login state
  const [totpRequired, setTotpRequired] = useState(false);
  const [totpEnrolled, setTotpEnrolled] = useState(false);

  // TOTP setup dialog state
  const [openTOTPDialog, setOpenTOTPDialog] = useState(false);
  const [totpStep, setTotpStep] = useState<'setup' | 'verify' | 'done'>('setup');
  const [totpSecret, setTotpSecret] = useState('');
  const [totpKeyURL, setTotpKeyURL] = useState('');
  const [totpVerifyCode, setTotpVerifyCode] = useState('');
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);

  // User dialog
  const [openUserDialog, setOpenUserDialog] = useState(false);
  const [editingUserId, setEditingUserId] = useState<number | null>(null);
  const [formUsername, setFormUsername] = useState('');
  const [formPassword, setFormPassword] = useState('');
  const [formRole, setFormRole] = useState('user');
  const [formPermConnect, setFormPermConnect] = useState(true);
  const [formPermAgent, setFormPermAgent] = useState(true);

  // Agent dialog
  const [openAgentDialog, setOpenAgentDialog] = useState(false);
  const [editingAgentId, setEditingAgentId] = useState<number | null>(null);
  const [formAgentID, setFormAgentID] = useState('');
  const [formDescription, setFormDescription] = useState('');
  const [formAllowedTargets, setFormAllowedTargets] = useState('');

  const authHeaders = (): HeadersInit => sessionToken ? { Authorization: `Bearer ${sessionToken}` } : {};

  // Check for 401 responses on authenticated requests and trigger logout.
  const checkAuth = (res: Response): boolean => {
    if (res.status === 401) {
      localStorage.removeItem('sessionToken');
      setSessionToken('');
      setIsLoggedIn(false);
      setError('Session expired — please log in again');
      return false;
    }
    return true;
  };

  const fetchSessions = async () => {
    try {
      const res = await fetch('/api/v1/sessions', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) {
        const data = await res.json();
        setSessions(data);
      }
    } catch (e) {
      console.error(e);
    }
  };

  const fetchUsers = async () => {
    try {
      const res = await fetch('/api/v1/users', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setUsers(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgents = async () => {
    try {
      const res = await fetch('/api/v1/agents', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setAgents(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  // Validate restored token on mount
  useEffect(() => {
    if (sessionToken) {
      fetch('/api/v1/me', { headers: { Authorization: `Bearer ${sessionToken}` } })
        .then(res => {
          if (res.ok) setIsLoggedIn(true);
          else {
            localStorage.removeItem('sessionToken');
            setSessionToken('');
          }
        })
        .catch(() => {
          localStorage.removeItem('sessionToken');
          setSessionToken('');
        });
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (isLoggedIn) {
      fetchSessions();
      fetchUsers();
      fetchAgents();
      const interval = setInterval(fetchSessions, 3000);
      return () => clearInterval(interval);
    }
  }, [isLoggedIn]);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const res = await fetch('/api/v1/user-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, totp_code: totpCode }),
      });
      if (res.ok) {
        const data = await res.json();
        setSessionToken(data.token);
        localStorage.setItem('sessionToken', data.token);
        setIsLoggedIn(true);
        setTotpEnrolled(data.totp_enrolled ?? false);
      } else {
        const text = await res.text();
        setError(text || 'Login failed');
      }
    } catch {
      setError('Failed to connect to server');
    }
  };

  const handleLogout = () => {
    fetch('/api/v1/logout', { method: 'POST', headers: authHeaders() }).catch(() => {});
    localStorage.removeItem('sessionToken');
    setSessionToken('');
    setIsLoggedIn(false);
    setTotpEnrolled(false);
  };

  const handleUsernameBlur = async () => {
    if (!username) return;
    try {
      const res = await fetch('/api/v1/user-login-check', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username }),
      });
      if (res.ok) {
        const data = await res.json();
        setTotpRequired(data.totp_required ?? false);
      } else {
        // Fail closed: show TOTP field on server error (more secure).
        setTotpRequired(true);
      }
    } catch {
      // Fail closed: show TOTP field on network error (more secure).
      setTotpRequired(true);
    }
  };

  const handleBeginTOTPSetup = async () => {
    try {
      const res = await fetch('/api/v1/totp/begin-setup', {
        method: 'POST',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      });
      if (res.ok) {
        const data = await res.json();
        setTotpSecret(data.secret);
        setTotpKeyURL(data.key_url);
        setTotpStep('verify');
      } else {
        setError(await res.text() || 'Failed to begin TOTP setup');
      }
    } catch {
      setError('Failed to begin TOTP setup');
    }
  };

  const handleVerifyTOTPSetup = async () => {
    try {
      const res = await fetch('/api/v1/totp/verify-setup', {
        method: 'POST',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ secret: totpSecret, code: totpVerifyCode }),
      });
      if (res.ok) {
        const data = await res.json();
        setRecoveryCodes(data.recovery_codes || []);
        setTotpStep('done');
        setTotpEnrolled(true);
      } else {
        setError(await res.text() || 'Invalid TOTP code');
      }
    } catch {
      setError('Failed to verify TOTP setup');
    }
  };

  const handleCloseTOTPDialog = () => {
    setOpenTOTPDialog(false);
    setTotpStep('setup');
    setTotpSecret('');
    setTotpKeyURL('');
    setTotpVerifyCode('');
    setRecoveryCodes([]);
  };

  const handleCopyRecoveryCodes = () => {
    navigator.clipboard.writeText(recoveryCodes.join('\n')).catch(() => {});
  };

  const openCreateUserDialog = () => {
    setEditingUserId(null);
    setFormUsername('');
    setFormPassword('');
    setFormRole('user');
    setFormPermConnect(true);
    setFormPermAgent(true);
    setOpenUserDialog(true);
  };

  const openEditUserDialog = (user: User) => {
    setEditingUserId(user.id);
    setFormUsername(user.username);
    setFormPassword('');
    setFormRole(user.role);
    setFormPermConnect(user.perm_connect);
    setFormPermAgent(user.perm_agent);
    setOpenUserDialog(true);
  };

  const handleSaveUser = async () => {
    if (editingUserId !== null) {
      try {
        const body: Record<string, unknown> = { role: formRole, perm_connect: formPermConnect, perm_agent: formPermAgent };
        if (formPassword) body.password = formPassword;
        const res = await fetch(`/api/v1/users/${editingUserId}`, {
          method: 'PATCH',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (res.ok) { setOpenUserDialog(false); fetchUsers(); }
        else setError(await res.text() || 'Failed to update user');
      } catch {
        setError('Failed to update user');
      }
    } else {
      try {
        const res = await fetch('/api/v1/users', {
          method: 'POST',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: formUsername, password: formPassword, role: formRole, perm_connect: formPermConnect, perm_agent: formPermAgent }),
        });
        if (res.ok) { setOpenUserDialog(false); fetchUsers(); }
        else setError(await res.text());
      } catch {
        setError('Failed to create user');
      }
    }
  };

  const handleToggleUser = async (user: User) => {
    try {
      const res = await fetch(`/api/v1/users/${user.id}`, {
        method: 'PATCH',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ disabled: !user.disabled_at }),
      });
      if (res.ok) fetchUsers();
      else setError(await res.text() || 'Failed to toggle user');
    } catch {
      setError('Failed to toggle user');
    }
  };

  const handleDeleteUser = async (id: number) => {
    try {
      const res = await fetch(`/api/v1/users/${id}`, { method: 'DELETE', headers: authHeaders() });
      if (res.ok) fetchUsers();
      else setError(await res.text() || 'Failed to delete user');
    } catch {
      setError('Failed to delete user');
    }
  };

  const openCreateAgentDialog = () => {
    setEditingAgentId(null);
    setFormAgentID('');
    setFormDescription('');
    setFormAllowedTargets('127.0.0.1:*');
    setOpenAgentDialog(true);
  };

  const openEditAgentDialog = (cfg: AgentConfig) => {
    setEditingAgentId(cfg.id);
    setFormAgentID(cfg.agent_id ?? '');
    setFormDescription(cfg.description);
    setFormAllowedTargets(cfg.allowed_targets.join(', '));
    setOpenAgentDialog(true);
  };

  const handleSaveAgent = async () => {
    const targets = formAllowedTargets.split(',').map(s => s.trim()).filter(Boolean);
    if (editingAgentId !== null) {
      try {
        const body: Record<string, unknown> = { description: formDescription, allowed_targets: targets };
        if (formAgentID) body.agent_id = formAgentID;
        const res = await fetch(`/api/v1/agents/${editingAgentId}`, {
          method: 'PATCH',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (res.ok) { setOpenAgentDialog(false); fetchAgents(); }
        else setError(await res.text() || 'Failed to update agent');
      } catch {
        setError('Failed to update agent');
      }
    } else {
      try {
        const res = await fetch('/api/v1/agents', {
          method: 'POST',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify({ agent_id: formAgentID, description: formDescription, allowed_targets: targets }),
        });
        if (res.ok) { setOpenAgentDialog(false); fetchAgents(); }
        else setError(await res.text());
      } catch {
        setError('Failed to create agent');
      }
    }
  };

  const handleDeleteAgent = async (id: number) => {
    try {
      const res = await fetch(`/api/v1/agents/${id}`, { method: 'DELETE', headers: authHeaders() });
      if (res.ok) fetchAgents();
      else setError(await res.text() || 'Failed to delete agent');
    } catch {
      setError('Failed to delete agent');
    }
  };

  // Column definitions (close over handlers, defined inside component)
  const userColumns: AdminTableColumn<User>[] = [
    { key: 'id', label: 'ID', width: 60, render: (r) => String(r.id) },
    { key: 'username', label: 'Username', render: (r) => (
      <Typography sx={{ fontWeight: 500 }}>{r.username}</Typography>
    )},
    { key: 'role', label: 'Role', render: (r) => (
      <Box sx={{ display: 'flex', gap: 0.5, alignItems: 'center', flexWrap: 'wrap' }}>
        <StatusPill
          tone={r.role === 'admin' ? 'error' : 'neutral'}
          label={r.role}
        />
        {r.perm_connect && <Chip label="connect" size="small" variant="outlined" />}
        {r.perm_agent && <Chip label="agent" size="small" variant="outlined" />}
      </Box>
    )},
    { key: 'created', label: 'Created At', render: (r) => new Date(r.created_at).toLocaleString() },
    { key: 'status', label: 'Status', render: (r) => (
      <StatusPill
        tone={r.disabled_at ? 'error' : 'success'}
        label={r.disabled_at ? 'Disabled' : 'Active'}
      />
    )},
    { key: 'actions', label: '', align: 'right', render: (r) => (
      <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 0.5 }} onClick={(e) => e.stopPropagation()}>
        <IconButton color="primary" size="small" onClick={() => openEditUserDialog(r)}>
          <EditIcon />
        </IconButton>
        <IconButton
          color={r.disabled_at ? 'success' : 'warning'}
          size="small"
          onClick={() => handleToggleUser(r)}
          title={r.disabled_at ? 'Enable user' : 'Disable user'}
        >
          {r.disabled_at ? <CheckCircleIcon /> : <BlockIcon />}
        </IconButton>
        <IconButton color="error" size="small" onClick={() => handleDeleteUser(r.id)}>
          <DeleteIcon />
        </IconButton>
      </Box>
    )},
  ];

  const agentColumns: AdminTableColumn<AgentConfig>[] = [
    { key: 'id', label: 'ID', width: 60, render: (r) => String(r.id) },
    { key: 'agent_id', label: 'Agent ID', render: (r) => (
      r.agent_id === null
        ? <StatusPill tone="info" label="Default" />
        : <Typography sx={{ fontFamily: 'monospace', fontWeight: 500 }}>{r.agent_id}</Typography>
    )},
    { key: 'description', label: 'Description', render: (r) => r.description },
    { key: 'targets', label: 'Allowed Targets', render: (r) => (
      <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
        {r.allowed_targets.map((t, i) => (
          <Chip key={i} label={t} size="small" variant="outlined" sx={{ fontFamily: 'monospace' }} />
        ))}
      </Box>
    )},
    { key: 'updated', label: 'Updated At', render: (r) => new Date(r.updated_at).toLocaleString() },
    { key: 'actions', label: '', align: 'right', render: (r) => (
      <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 0.5 }} onClick={(e) => e.stopPropagation()}>
        <IconButton color="primary" size="small" onClick={() => openEditAgentDialog(r)}>
          <EditIcon />
        </IconButton>
        {r.agent_id !== null && (
          <IconButton color="error" size="small" onClick={() => handleDeleteAgent(r.id)}>
            <DeleteIcon />
          </IconButton>
        )}
      </Box>
    )},
  ];

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <Box sx={{ minHeight: '100vh', pb: 6 }}>
        {/* Top header bar */}
        <Box
          component="header"
          sx={{
            borderBottom: 1,
            borderColor: 'divider',
            bgcolor: 'background.paper',
            px: 4,
            py: 1.5,
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
          }}
        >
          <Typography variant="h6" sx={{ color: 'text.primary' }}>
            ssetunnel Console
          </Typography>
          {isLoggedIn && (
            <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
              <IconButton
                color="default"
                size="small"
                onClick={() => setOpenTOTPDialog(true)}
                title="Security (TOTP)"
              >
                <SecurityIcon />
              </IconButton>
              <Button color="inherit" size="small" onClick={handleLogout}>
                Logout
              </Button>
            </Box>
          )}
        </Box>

        {!isLoggedIn ? (
          /* Login form */
          <Container maxWidth="xs" sx={{ mt: 10 }}>
            <Card sx={{ p: 2, borderRadius: 3 }}>
              <CardContent sx={{ textAlign: 'center' }}>
                <LockOutlinedIcon sx={{ fontSize: 40, color: 'primary.main', mb: 1 }} />
                <Typography variant="h5" gutterBottom>
                  Login
                </Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
                  Sign in with your credentials
                </Typography>
                {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
                <form onSubmit={handleLogin}>
                  <TextField
                    fullWidth
                    label="Username"
                    variant="outlined"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    onBlur={handleUsernameBlur}
                    sx={{ mb: 2 }}
                    autoFocus
                  />
                  <TextField
                    fullWidth
                    label="Password"
                    type="password"
                    variant="outlined"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    sx={{ mb: 2 }}
                  />
                  {totpRequired && (
                    <TextField
                      fullWidth
                      label="TOTP or Recovery Code"
                      variant="outlined"
                      value={totpCode}
                      onChange={(e) => setTotpCode(e.target.value)}
                      placeholder="123456 or recovery code"
                      sx={{ mb: 3 }}
                    />
                  )}
                  <Button fullWidth variant="contained" type="submit" size="large">
                    Sign In
                  </Button>
                </form>
              </CardContent>
            </Card>
          </Container>
        ) : (
          /* Main content with tabs */
          <Container maxWidth="lg" sx={{ mt: 4 }}>
            <Paper sx={{ mb: 3 }}>
              <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                <Tab label="Sessions" />
                <Tab label="Users" />
                <Tab label="Agents" />
              </Tabs>
            </Paper>

            {!totpEnrolled && (
              <Alert
                severity="info"
                sx={{ mb: 2 }}
                action={
                  <Button color="inherit" size="small" onClick={() => setOpenTOTPDialog(true)}>
                    Set Up
                  </Button>
                }
              >
                Two-factor authentication is not configured. Click "Set Up" to enable TOTP.
              </Alert>
            )}

            {tabIndex === 0 && (
              <Box>
                <PageHeader
                  title={`Live Tunnel Sessions (${sessions.length})`}
                  actions={
                    <Button startIcon={<RefreshIcon />} onClick={fetchSessions} size="small">
                      Refresh
                    </Button>
                  }
                />
                <AdminTable
                  columns={SESSION_COLUMNS}
                  rows={sessions}
                  rowKey="id"
                  empty={
                    <EmptyState
                      icon={<CableIcon sx={{ fontSize: 48, color: 'text.disabled' }} />}
                      title="No active tunnel sessions"
                    />
                  }
                />
              </Box>
            )}

            {tabIndex === 1 && (
              <Box>
                <PageHeader
                  title={`Users (${users.length})`}
                  actions={
                    <Box sx={{ display: 'flex', gap: 1 }}>
                      <Button startIcon={<RefreshIcon />} onClick={fetchUsers} size="small">
                        Refresh
                      </Button>
                      <Button variant="contained" startIcon={<PersonAddIcon />} onClick={openCreateUserDialog}>
                        Create User
                      </Button>
                    </Box>
                  }
                />
                <AdminTable
                  columns={userColumns}
                  rows={users}
                  rowKey="id"
                  empty={
                    <EmptyState
                      icon={<GroupIcon sx={{ fontSize: 48, color: 'text.disabled' }} />}
                      title="No users"
                    />
                  }
                />
              </Box>
            )}

            {tabIndex === 2 && (
              <Box>
                <PageHeader
                  title={`Agent Configs (${agents.length})`}
                  actions={
                    <Box sx={{ display: 'flex', gap: 1 }}>
                      <Button startIcon={<RefreshIcon />} onClick={fetchAgents} size="small">
                        Refresh
                      </Button>
                      <Button variant="contained" startIcon={<AddIcon />} onClick={openCreateAgentDialog}>
                        Add Agent
                      </Button>
                    </Box>
                  }
                />
                <AdminTable
                  columns={agentColumns}
                  rows={agents}
                  rowKey="id"
                  empty={
                    <EmptyState
                      icon={<RouterIcon sx={{ fontSize: 48, color: 'text.disabled' }} />}
                      title="No agent configs"
                    />
                  }
                />
              </Box>
            )}

            {/* Create/Edit User Dialog */}
            <Dialog open={openUserDialog} onClose={() => setOpenUserDialog(false)} maxWidth="xs" fullWidth>
              <DialogTitle>{editingUserId !== null ? 'Edit User' : 'Create User'}</DialogTitle>
              <DialogContent>
                <TextField
                  fullWidth
                  label="Username"
                  value={formUsername}
                  onChange={(e) => setFormUsername(e.target.value)}
                  margin="normal"
                  disabled={editingUserId !== null}
                  autoFocus
                />
                <TextField
                  fullWidth
                  label={editingUserId !== null ? 'New Password (leave blank to keep)' : 'Password'}
                  type="password"
                  value={formPassword}
                  onChange={(e) => setFormPassword(e.target.value)}
                  margin="normal"
                />
                <FormControl fullWidth margin="normal">
                  <InputLabel>Role</InputLabel>
                  <Select
                    value={formRole}
                    label="Role"
                    onChange={(e) => setFormRole(e.target.value)}
                  >
                    <MenuItem value="user">user</MenuItem>
                    <MenuItem value="admin">admin</MenuItem>
                  </Select>
                </FormControl>
                <FormControlLabel
                  control={<Checkbox checked={formPermConnect} onChange={(e) => setFormPermConnect(e.target.checked)} />}
                  label="Can Connect (connect to agent tunnels)"
                />
                <FormControlLabel
                  control={<Checkbox checked={formPermAgent} onChange={(e) => setFormPermAgent(e.target.checked)} />}
                  label="Can Agent (register agent tunnels)"
                />
              </DialogContent>
              <DialogActions>
                <Button onClick={() => setOpenUserDialog(false)}>Cancel</Button>
                <Button variant="contained" onClick={handleSaveUser}>
                  {editingUserId !== null ? 'Save' : 'Create'}
                </Button>
              </DialogActions>
            </Dialog>

            {/* Create/Edit Agent Dialog */}
            <Dialog open={openAgentDialog} onClose={() => setOpenAgentDialog(false)} maxWidth="xs" fullWidth>
              <DialogTitle>{editingAgentId !== null ? 'Edit Agent Config' : 'Add Agent Config'}</DialogTitle>
              <DialogContent>
                <TextField
                  fullWidth
                  label="Agent ID"
                  value={formAgentID}
                  onChange={(e) => setFormAgentID(e.target.value)}
                  margin="normal"
                  disabled={editingAgentId !== null && formAgentID === ''}
                  placeholder="e.g. mydevbox"
                  autoFocus
                />
                <TextField
                  fullWidth
                  label="Description"
                  value={formDescription}
                  onChange={(e) => setFormDescription(e.target.value)}
                  margin="normal"
                />
                <TextField
                  fullWidth
                  label="Allowed Targets (comma-separated)"
                  value={formAllowedTargets}
                  onChange={(e) => setFormAllowedTargets(e.target.value)}
                  margin="normal"
                  helperText="e.g. 127.0.0.1:*, *:22, *"
                />
              </DialogContent>
              <DialogActions>
                <Button onClick={() => setOpenAgentDialog(false)}>Cancel</Button>
                <Button variant="contained" onClick={handleSaveAgent}>
                  {editingAgentId !== null ? 'Save' : 'Create'}
                </Button>
              </DialogActions>
            </Dialog>

            {/* TOTP Setup Dialog */}
            <Dialog open={openTOTPDialog} onClose={handleCloseTOTPDialog} maxWidth="sm" fullWidth>
              <DialogTitle>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <SecurityIcon /> Two-Factor Authentication
                </Box>
              </DialogTitle>
              <DialogContent>
                {totpStep === 'setup' && (
                  <Box sx={{ textAlign: 'center', py: 2 }}>
                    <Typography variant="body1" sx={{ mb: 2 }}>
                      {totpEnrolled
                        ? 'TOTP is already configured for your account.'
                        : 'TOTP is not configured. Set up two-factor authentication to protect your account.'}
                    </Typography>
                    {!totpEnrolled && (
                      <Button variant="contained" onClick={handleBeginTOTPSetup}>
                        Set Up TOTP
                      </Button>
                    )}
                  </Box>
                )}
                {totpStep === 'verify' && (
                  <Box sx={{ textAlign: 'center', py: 2 }}>
                    <Typography variant="body2" sx={{ mb: 2 }}>
                      Scan this QR code with your authenticator app:
                    </Typography>
                    <Box sx={{ display: 'flex', justifyContent: 'center', mb: 2 }}>
                      <QRCodeSVG value={totpKeyURL} size={200} />
                    </Box>
                    <Typography variant="caption" color="text.secondary" sx={{ mb: 1, display: 'block' }}>
                      Or enter this secret manually:
                    </Typography>
                    <Typography
                      variant="body2"
                      sx={{ fontFamily: 'monospace', bgcolor: 'grey.100', p: 1, borderRadius: 1, mb: 2, userSelect: 'all' }}
                    >
                      {totpSecret}
                    </Typography>
                    <TextField
                      fullWidth
                      label="Enter 6-digit code to verify"
                      value={totpVerifyCode}
                      onChange={(e) => setTotpVerifyCode(e.target.value)}
                      placeholder="123456"
                      sx={{ mb: 2 }}
                    />
                    <Button variant="contained" onClick={handleVerifyTOTPSetup}>
                      Verify & Enable
                    </Button>
                  </Box>
                )}
                {totpStep === 'done' && (
                  <Box sx={{ py: 2 }}>
                    <Alert severity="success" sx={{ mb: 2 }}>
                      TOTP has been enabled for your account.
                    </Alert>
                    <Typography variant="body2" sx={{ mb: 1, fontWeight: 600 }}>
                      Save these recovery codes. Each can be used once:
                    </Typography>
                    <Paper
                      variant="outlined"
                      sx={{ p: 2, mb: 2, bgcolor: 'grey.50', fontFamily: 'monospace', whiteSpace: 'pre-line' }}
                    >
                      {recoveryCodes.join('\n')}
                    </Paper>
                    <Button
                      variant="outlined"
                      startIcon={<ContentCopyIcon />}
                      onClick={handleCopyRecoveryCodes}
                      sx={{ mb: 2 }}
                    >
                      Copy Codes
                    </Button>
                    <Alert severity="warning">
                      Store these codes in a safe place. If you lose your authenticator and recovery codes, you will be locked out.
                    </Alert>
                  </Box>
                )}
              </DialogContent>
              <DialogActions>
                <Button onClick={handleCloseTOTPDialog}>
                  {totpStep === 'done' ? 'Done' : 'Cancel'}
                </Button>
              </DialogActions>
            </Dialog>
          </Container>
        )}
      </Box>
    </ThemeProvider>
  );
}
