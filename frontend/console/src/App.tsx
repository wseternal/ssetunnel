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
  Grid,
  Select,
  MenuItem,
  FormControl,
  InputLabel,
} from '@mui/material';
import DeleteIcon from '@mui/icons-material/Delete';
import RefreshIcon from '@mui/icons-material/Refresh';
import LockOutlinedIcon from '@mui/icons-material/LockOutlined';
import VpnKeyIcon from '@mui/icons-material/VpnKey';

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

interface Token {
  id: number;
  role: string;
  description: string;
  created_at: string;
  revoked_at?: string;
}

interface Session {
  id: string;
  bytes_sent: number;
  bytes_received: number;
  created_at: string;
  remote_addr: string;
}

export default function App() {
  const [isLoggedIn, setIsLoggedIn] = useState(false);
  const [totpCode, setTotpCode] = useState('');
  const [tabIndex, setTabIndex] = useState(0);
  const [tokens, setTokens] = useState<Token[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState('');
  
  // New token dialog
  const [openTokenDialog, setOpenTokenDialog] = useState(false);
  const [newTokenRole, setNewTokenRole] = useState('agent');
  const [newTokenDesc, setNewTokenDesc] = useState('');
  const [createdTokenVal, setCreatedTokenVal] = useState('');

  // Enroll result
  const [enrollData, setEnrollData] = useState<{ pin: string; qr_code_base64?: string } | null>(null);

  const fetchTokens = async () => {
    try {
      const res = await fetch('/api/v1/tokens');
      if (res.ok) {
        const data = await res.json();
        setTokens(data);
      }
    } catch (e) {
      console.error(e);
    }
  };

  const fetchSessions = async () => {
    try {
      const res = await fetch('/api/v1/sessions');
      if (res.ok) {
        const data = await res.json();
        setSessions(data);
      }
    } catch (e) {
      console.error(e);
    }
  };

  useEffect(() => {
    if (isLoggedIn) {
      fetchTokens();
      fetchSessions();
      const interval = setInterval(fetchSessions, 3000);
      return () => clearInterval(interval);
    }
  }, [isLoggedIn]);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const res = await fetch('/api/v1/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ totp_code: totpCode }),
      });
      if (res.ok) {
        setIsLoggedIn(true);
      } else {
        setError('Invalid TOTP code or credentials');
      }
    } catch (err) {
      setError('Failed to connect to server');
    }
  };

  const handleCreateToken = async () => {
    try {
      const res = await fetch('/api/v1/tokens', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: newTokenRole, description: newTokenDesc }),
      });
      if (res.ok) {
        const data = await res.json();
        setCreatedTokenVal(data.token);
        fetchTokens();
      }
    } catch (err) {
      setError('Failed to create token');
    }
  };

  const handleRevokeToken = async (id: number) => {
    try {
      const res = await fetch(`/api/v1/tokens/${id}`, { method: 'DELETE' });
      if (res.ok) {
        fetchTokens();
      }
    } catch (err) {
      setError('Failed to revoke token');
    }
  };

  const handleEnroll = async () => {
    try {
      const res = await fetch('/api/v1/enroll', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'agent' }),
      });
      if (res.ok) {
        const data = await res.json();
        setEnrollData(data);
      }
    } catch (err) {
      setError('Failed to generate PIN');
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
            <Button color="inherit" size="small" onClick={() => setIsLoggedIn(false)}>
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
                  Admin Login
                </Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
                  Enter your 6-digit TOTP authentication code
                </Typography>
                {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
                <form onSubmit={handleLogin}>
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
                    Authenticate
                  </Button>
                </form>
              </CardContent>
            </Card>
          </Container>
        ) : (
          <Container maxWidth="lg" sx={{ mt: 4 }}>
            <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
              <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                <Tab label="Active Sessions" />
                <Tab label="Bearer Tokens" />
                <Tab label="Enrollment PINs" />
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
                        <TableCell>Status</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {sessions.length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={5} align="center">No active tunnel sessions</TableCell>
                        </TableRow>
                      ) : (
                        sessions.map((s) => (
                          <TableRow key={s.id}>
                            <TableCell sx={{ fontFamily: 'monospace' }}>{s.id}</TableCell>
                            <TableCell>{(s.bytes_received / 1024).toFixed(1)} KB</TableCell>
                            <TableCell>{(s.bytes_sent / 1024).toFixed(1)} KB</TableCell>
                            <TableCell>{new Date(s.created_at).toLocaleTimeString()}</TableCell>
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
                  <Typography variant="h6">Authentication Tokens</Typography>
                  <Button variant="contained" startIcon={<VpnKeyIcon />} onClick={() => { setCreatedTokenVal(''); setOpenTokenDialog(true); }}>
                    Generate Token
                  </Button>
                </Box>
                <TableContainer component={Paper} sx={{ borderRadius: 2 }}>
                  <Table>
                    <TableHead>
                      <TableRow>
                        <TableCell>ID</TableCell>
                        <TableCell>Role</TableCell>
                        <TableCell>Description</TableCell>
                        <TableCell>Created At</TableCell>
                        <TableCell>Status</TableCell>
                        <TableCell align="right">Actions</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {tokens.map((t) => (
                        <TableRow key={t.id}>
                          <TableCell>{t.id}</TableCell>
                          <TableCell><Chip label={t.role} color={t.role === 'admin' ? 'secondary' : 'primary'} size="small" /></TableCell>
                          <TableCell>{t.description || '-'}</TableCell>
                          <TableCell>{new Date(t.created_at).toLocaleString()}</TableCell>
                          <TableCell>
                            {t.revoked_at ? <Chip label="Revoked" color="error" size="small" /> : <Chip label="Active" color="success" size="small" />}
                          </TableCell>
                          <TableCell align="right">
                            {!t.revoked_at && (
                              <IconButton color="error" onClick={() => handleRevokeToken(t.id)}>
                                <DeleteIcon />
                              </IconButton>
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </TableContainer>
              </Box>
            )}

            {tabIndex === 2 && (
              <Box>
                <Typography variant="h6" gutterBottom>Enroll Agent with Single-Use PIN</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
                  Generate a 15-minute temporary PIN for one-time agent registration. The PIN is redeemed for a persistent token on first connection.
                </Typography>
                <Button variant="contained" onClick={handleEnroll} sx={{ mb: 3 }}>
                  Generate Enrollment PIN
                </Button>

                {enrollData && (
                  <Card sx={{ p: 3, maxWidth: 500, borderRadius: 2 }}>
                    <Typography variant="subtitle2" color="text.secondary">Single-Use Enrollment PIN:</Typography>
                    <Typography variant="h4" fontWeight="bold" color="primary.main" sx={{ letterSpacing: 4, my: 1 }}>
                      {enrollData.pin}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">Valid for 15 minutes. One-time registration use only.</Typography>

                    {enrollData.qr_code_base64 && (
                      <Box sx={{ mt: 3, textAlign: 'center' }}>
                        <Typography variant="subtitle2" gutterBottom>Admin TOTP QR Code:</Typography>
                        <img src={enrollData.qr_code_base64} alt="TOTP QR Code" style={{ borderRadius: 8 }} />
                      </Box>
                    )}
                  </Card>
                )}
              </Box>
            )}

            {/* Create Token Dialog */}
            <Dialog open={openTokenDialog} onClose={() => setOpenTokenDialog(false)} maxWidth="xs" fullWidth>
              <DialogTitle>Generate {newTokenRole.charAt(0).toUpperCase() + newTokenRole.slice(1)} Token</DialogTitle>
              <DialogContent>
                <FormControl fullWidth margin="normal">
                  <InputLabel>Role</InputLabel>
                  <Select
                    value={newTokenRole}
                    label="Role"
                    onChange={(e) => setNewTokenRole(e.target.value)}
                  >
                    <MenuItem value="agent">agent</MenuItem>
                    <MenuItem value="user">user</MenuItem>
                    <MenuItem value="admin">admin</MenuItem>
                  </Select>
                </FormControl>
                <TextField
                  fullWidth
                  label="Description"
                  value={newTokenDesc}
                  onChange={(e) => setNewTokenDesc(e.target.value)}
                  margin="normal"
                />
                {createdTokenVal && (
                  <Alert severity="success" sx={{ mt: 2, wordBreak: 'break-all' }}>
                    Token Generated (Copy now, shown once):<br />
                    <strong>{createdTokenVal}</strong>
                  </Alert>
                )}
              </DialogContent>
              <DialogActions>
                <Button onClick={() => setOpenTokenDialog(false)}>Close</Button>
                {!createdTokenVal && <Button variant="contained" onClick={handleCreateToken}>Generate</Button>}
              </DialogActions>
            </Dialog>
          </Container>
        )}
      </Box>
    </ThemeProvider>
  );
}
