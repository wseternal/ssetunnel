import React, { useState, useEffect } from 'react';
import {
  ThemeProvider,
  createTheme,
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
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
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

const darkTheme = createTheme({
  palette: {
    mode: 'dark',
    primary: { main: '#00f2fe' },
    secondary: { main: '#4facfe' },
    background: { default: '#0B0F19', paper: '#111827' },
  },
  typography: {
    fontFamily: '"Inter", "Roboto", "Helvetica", "Arial", sans-serif',
  },
});

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

  const fetchSessions = async () => {
    try {
      const res = await fetch('/api/v1/sessions', { headers: authHeaders() });
      if (res.ok) {
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
      if (res.ok) setUsers(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgents = async () => {
    try {
      const res = await fetch('/api/v1/agents', { headers: authHeaders() });
      if (res.ok) setAgents(await res.json());
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

  const roleColor = (role: string): 'error' | 'warning' | 'primary' | 'default' => {
    switch (role) {
      case 'admin': return 'error';
      default: return 'primary';
    }
  };

  return (
    <ThemeProvider theme={darkTheme}>
      <CssBaseline />
      <Box sx={{ minHeight: '100vh', pb: 6 }}>
        <Box sx={{ borderBottom: 1, borderColor: 'divider', px: 4, py: 2, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <Typography variant="h6" fontWeight="bold" sx={{ background: 'linear-gradient(45deg, #00f2fe, #4facfe)', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent' }}>
            ssetunnel Console
          </Typography>
          {isLoggedIn && (
            <Button color="inherit" size="small" onClick={handleLogout}>
              Logout
            </Button>
          )}
        </Box>

        {!isLoggedIn ? (
          <Container maxWidth="xs" sx={{ mt: 10 }}>
            <Card sx={{ p: 2, borderRadius: 3, boxShadow: '0 8px 32px rgba(0,0,0,0.5)' }}>
              <CardContent sx={{ textAlign: 'center' }}>
                <LockOutlinedIcon sx={{ fontSize: 40, color: 'primary.main', mb: 1 }} />
                <Typography variant="h5" fontWeight="bold" gutterBottom>
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
                  <TextField
                    fullWidth
                    label="TOTP Passcode"
                    variant="outlined"
                    value={totpCode}
                    onChange={(e) => setTotpCode(e.target.value)}
                    placeholder="123456"
                    sx={{ mb: 3 }}
                  />
                  <Button fullWidth variant="contained" type="submit" size="large">
                    Sign In
                  </Button>
                </form>
              </CardContent>
            </Card>
          </Container>
        ) : (
          <Container maxWidth="lg" sx={{ mt: 4 }}>
            <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
              <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                <Tab label="Sessions" />
                <Tab label="Users" />
                <Tab label="Agents" />
              </Tabs>
            </Box>

            {tabIndex === 0 && (
              <Box>
                <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
                  <Typography variant="h6">Live Tunnel Sessions ({sessions.length})</Typography>
                  <Button startIcon={<RefreshIcon />} onClick={fetchSessions} size="small">Refresh</Button>
                </Box>
                <TableContainer component={Paper} sx={{ borderRadius: 2 }}>
                  <Table>
                    <TableHead>
                      <TableRow>
                        <TableCell>Session ID</TableCell>
                        <TableCell>Bytes Received (Up)</TableCell>
                        <TableCell>Bytes Sent (Down)</TableCell>
                        <TableCell>Connected At</TableCell>
                        <TableCell>Remote Addr</TableCell>
                        <TableCell>Status</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {sessions.length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={6} align="center">No active tunnel sessions</TableCell>
                        </TableRow>
                      ) : (
                        sessions.map((s) => (
                          <TableRow key={s.id}>
                            <TableCell sx={{ fontFamily: 'monospace' }}>{s.id}</TableCell>
                            <TableCell>{(s.bytes_received / 1024).toFixed(1)} KB</TableCell>
                            <TableCell>{(s.bytes_sent / 1024).toFixed(1)} KB</TableCell>
                            <TableCell>{new Date(s.created_at).toLocaleTimeString()}</TableCell>
                            <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.85rem' }}>{s.remote_addr}</TableCell>
                            <TableCell><Chip label="Active" color="success" size="small" /></TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </TableContainer>
              </Box>
            )}

            {tabIndex === 1 && (
              <Box>
                <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
                  <Typography variant="h6">Users ({users.length})</Typography>
                  <Box>
                    <Button startIcon={<RefreshIcon />} onClick={fetchUsers} size="small" sx={{ mr: 1 }}>Refresh</Button>
                    <Button variant="contained" startIcon={<PersonAddIcon />} onClick={openCreateUserDialog}>
                      Create User
                    </Button>
                  </Box>
                </Box>
                <TableContainer component={Paper} sx={{ borderRadius: 2 }}>
                  <Table>
                    <TableHead>
                      <TableRow>
                        <TableCell>ID</TableCell>
                        <TableCell>Username</TableCell>
                        <TableCell>Role</TableCell>
                        <TableCell>Created At</TableCell>
                        <TableCell>Status</TableCell>
                        <TableCell align="right">Actions</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {users.length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={6} align="center">No users</TableCell>
                        </TableRow>
                      ) : (
                        users.map((u) => (
                          <TableRow key={u.id}>
                            <TableCell>{u.id}</TableCell>
                            <TableCell sx={{ fontWeight: 500 }}>{u.username}</TableCell>
                            <TableCell>
                              <Chip label={u.role} color={roleColor(u.role)} size="small" />
                              {u.perm_connect && <Chip label="connect" size="small" sx={{ ml: 0.5 }} />}
                              {u.perm_agent && <Chip label="agent" size="small" sx={{ ml: 0.5 }} />}
                            </TableCell>
                            <TableCell>{new Date(u.created_at).toLocaleString()}</TableCell>
                            <TableCell>
                              {u.disabled_at
                                ? <Chip label="Disabled" color="error" size="small" />
                                : <Chip label="Active" color="success" size="small" />
                              }
                            </TableCell>
                            <TableCell align="right">
                              <IconButton color="primary" size="small" onClick={() => openEditUserDialog(u)}>
                                <EditIcon />
                              </IconButton>
                              <IconButton
                                color={u.disabled_at ? 'success' : 'warning'}
                                size="small"
                                onClick={() => handleToggleUser(u)}
                                title={u.disabled_at ? 'Enable user' : 'Disable user'}
                              >
                                {u.disabled_at ? <CheckCircleIcon /> : <BlockIcon />}
                              </IconButton>
                              <IconButton color="error" size="small" onClick={() => handleDeleteUser(u.id)}>
                                <DeleteIcon />
                              </IconButton>
                            </TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </TableContainer>
              </Box>
            )}

            {tabIndex === 2 && (
              <Box>
                <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
                  <Typography variant="h6">Agent Configs ({agents.length})</Typography>
                  <Box>
                    <Button startIcon={<RefreshIcon />} onClick={fetchAgents} size="small" sx={{ mr: 1 }}>Refresh</Button>
                    <Button variant="contained" startIcon={<AddIcon />} onClick={openCreateAgentDialog}>
                      Add Agent
                    </Button>
                  </Box>
                </Box>
                <TableContainer component={Paper} sx={{ borderRadius: 2 }}>
                  <Table>
                    <TableHead>
                      <TableRow>
                        <TableCell>ID</TableCell>
                        <TableCell>Agent ID</TableCell>
                        <TableCell>Description</TableCell>
                        <TableCell>Allowed Targets</TableCell>
                        <TableCell>Updated At</TableCell>
                        <TableCell align="right">Actions</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {agents.length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={6} align="center">No agent configs</TableCell>
                        </TableRow>
                      ) : (
                        agents.map((cfg) => (
                          <TableRow key={cfg.id} sx={cfg.agent_id === null ? { bgcolor: 'rgba(0, 242, 254, 0.05)' } : undefined}>
                            <TableCell>{cfg.id}</TableCell>
                            <TableCell sx={{ fontFamily: 'monospace', fontWeight: 500 }}>
                              {cfg.agent_id === null ? (
                                <Chip label="Default" color="secondary" size="small" />
                              ) : (
                                cfg.agent_id
                              )}
                            </TableCell>
                            <TableCell>{cfg.description}</TableCell>
                            <TableCell>
                              {cfg.allowed_targets.map((t, i) => (
                                <Chip key={i} label={t} size="small" sx={{ mr: 0.5, mb: 0.5, fontFamily: 'monospace' }} />
                              ))}
                            </TableCell>
                            <TableCell>{new Date(cfg.updated_at).toLocaleString()}</TableCell>
                            <TableCell align="right">
                              <IconButton color="primary" size="small" onClick={() => openEditAgentDialog(cfg)}>
                                <EditIcon />
                              </IconButton>
                              {cfg.agent_id !== null && (
                                <IconButton color="error" size="small" onClick={() => handleDeleteAgent(cfg.id)}>
                                  <DeleteIcon />
                                </IconButton>
                              )}
                            </TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </TableContainer>
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
          </Container>
        )}
      </Box>
    </ThemeProvider>
  );
}
