import { useState, useEffect } from 'react';
import { Box, TextField, Button, Typography, Alert, Autocomplete } from '@mui/material';
import type { Bridge, SavedSession } from './types';

const DEFAULT_TUNNEL = 'http://127.0.0.1:8080';
const DEFAULT_CONSOLE = 'http://127.0.0.1:8081/console';

type TunnelOption = string | { label: string; group: string };
type ConsoleOption = string | { label: string };

interface LoginFormProps {
  onLogin: (server: string, tunnelServer: string, username: string, password: string, totpCode: string) => Promise<void>;
  bridge: Bridge;
  error: string;
}

function tunnelURL(opt: TunnelOption): string {
  return typeof opt === 'string' ? opt : opt.label;
}

function consoleURL(opt: ConsoleOption): string {
  return typeof opt === 'string' ? opt : opt.label;
}

export default function LoginForm({ onLogin, bridge, error }: LoginFormProps) {
  const defaultTunnelOpt: TunnelOption = { label: DEFAULT_TUNNEL, group: 'Default' };
  const defaultConsoleOpt: ConsoleOption = { label: DEFAULT_CONSOLE };

  const [tunnelOpt, setTunnelOpt] = useState<TunnelOption>(defaultTunnelOpt);
  const [consoleOpt, setConsoleOpt] = useState<ConsoleOption>(defaultConsoleOpt);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [totpRequired, setTotpRequired] = useState(false);
  const [checking, setChecking] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [savedSessions, setSavedSessions] = useState<SavedSession[]>([]);

  useEffect(() => {
    bridge.listSessions().then(setSavedSessions).catch(() => {});
  }, [bridge]);

  const tunnelOptions: TunnelOption[] = [
    defaultTunnelOpt,
    ...savedSessions.map(s => ({ label: s.server_url, group: 'Saved Sessions' })),
  ];

  // Auto-fill console URL when tunnel server selection changes.
  useEffect(() => {
    const url = tunnelURL(tunnelOpt);
    if (url === DEFAULT_TUNNEL) {
      setConsoleOpt(defaultConsoleOpt);
      return;
    }
    const match = savedSessions.find(s => s.server_url === url);
    if (match?.console_url) {
      setConsoleOpt({ label: match.console_url });
    }
  }, [tunnelOpt, savedSessions]);

  const handleUsernameBlur = async () => {
    const cs = consoleURL(consoleOpt);
    if (!username || !cs) return;
    setChecking(true);
    try {
      const required = await bridge.checkTOTPRequired(cs, username);
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
      await onLogin(consoleURL(consoleOpt), tunnelURL(tunnelOpt), username, password, totpCode);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Box component="form" onSubmit={handleSubmit}>
      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
      <Autocomplete
        freeSolo
        disableClearable
        options={tunnelOptions}
        groupBy={opt => typeof opt === 'string' ? '' : opt.group}
        getOptionLabel={opt => typeof opt === 'string' ? opt : opt.label}
        value={tunnelOpt}
        onChange={(_, v) => { if (v !== null) setTunnelOpt(v); }}
        renderInput={params => (
          <TextField {...params} fullWidth label="Server URL" required />
        )}
        sx={{ mb: 2 }}
      />
      <Autocomplete
        freeSolo
        disableClearable
        options={[defaultConsoleOpt]}
        getOptionLabel={opt => typeof opt === 'string' ? opt : opt.label}
        value={consoleOpt}
        onChange={(_, v) => { if (v !== null) setConsoleOpt(v); }}
        renderInput={params => (
          <TextField {...params} fullWidth label="Console URL" required />
        )}
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
  );
}
