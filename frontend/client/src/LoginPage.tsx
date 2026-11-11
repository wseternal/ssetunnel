import { useState } from 'react';
import { Box, Card, CardContent, TextField, Button, Typography, Alert } from '@mui/material';
import type { Bridge } from './types';

interface LoginPageProps {
  onLogin: (server: string, tunnelServer: string, username: string, password: string, totpCode: string) => Promise<void>;
  bridge: Bridge;
  error: string;
}

export default function LoginPage({ onLogin, bridge, error }: LoginPageProps) {
  const [server, setServer] = useState('http://127.0.0.1:8081');
  const [tunnelServer, setTunnelServer] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [totpRequired, setTotpRequired] = useState(false);
  const [checking, setChecking] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const handleUsernameBlur = async () => {
    if (!username || !server) return;
    setChecking(true);
    try {
      const required = await bridge.checkTOTPRequired(server, username);
      setTotpRequired(required);
    } catch {
      // Allow login attempt even if check fails.
    } finally {
      setChecking(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    try {
      await onLogin(server, tunnelServer, username, password, totpCode);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '100vh', bgcolor: 'grey.50' }}>
      <Card sx={{ maxWidth: 440, width: '100%' }}>
        <CardContent sx={{ p: 4 }}>
          <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>ssetunnel Client</Typography>
          {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
          <Box component="form" onSubmit={handleSubmit}>
            <TextField
              fullWidth label="Server URL" value={server}
              onChange={e => setServer(e.target.value)}
              sx={{ mb: 2 }} required
            />
            <TextField
              fullWidth label="Tunnel Server URL (optional)" value={tunnelServer}
              onChange={e => setTunnelServer(e.target.value)}
              helperText="Leave empty to derive from server URL"
              sx={{ mb: 2 }}
            />
            <TextField
              fullWidth label="Username" value={username}
              onChange={e => setUsername(e.target.value)}
              onBlur={handleUsernameBlur}
              sx={{ mb: 2 }} required autoFocus
            />
            <TextField
              fullWidth label="Password" type="password" value={password}
              onChange={e => setPassword(e.target.value)}
              sx={{ mb: 2 }} required
            />
            <TextField
              fullWidth label={totpRequired ? 'TOTP or Recovery Code' : 'TOTP or Recovery Code (optional)'}
              value={totpCode}
              onChange={e => setTotpCode(e.target.value)}
              helperText={checking ? 'Checking...' : undefined}
              sx={{ mb: 3 }}
            />
            <Button fullWidth variant="contained" type="submit" disabled={submitting} size="large">
              {submitting ? 'Logging in...' : 'Login'}
            </Button>
          </Box>
        </CardContent>
      </Card>
    </Box>
  );
}
