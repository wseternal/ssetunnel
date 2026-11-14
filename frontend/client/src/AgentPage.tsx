import { useState, useEffect, useCallback } from 'react';
import {
  Box, Card, CardContent, TextField, Button, Typography, Switch,
  FormControlLabel, Alert, Chip, Stack, CircularProgress,
} from '@mui/material';
import type { Bridge, AgentStatus, AgentConfig, SavedSession } from './types';
import ServerURLField from './ServerURLField';

interface AgentPageProps {
  bridge: Bridge;
}

const defaultConfig: AgentConfig = {
  server_url: '',
  target: '',
  agent_id: '',
  batch_size: 262144,
  concurrency: 1,
  compress: false,
  no_auto_tune: false,
  verbose: false,
};

export default function AgentPage({ bridge }: AgentPageProps) {
  const [status, setStatus] = useState<AgentStatus | null>(null);
  const [config, setConfig] = useState<AgentConfig>(defaultConfig);
  const [loading, setLoading] = useState(true);
  const [action, setAction] = useState('');
  const [error, setError] = useState('');
  const [savedSessions, setSavedSessions] = useState<SavedSession[]>([]);

  useEffect(() => {
    bridge.listSessions().then(setSavedSessions).catch(() => {});
  }, [bridge]);

  const refresh = useCallback(async () => {
    try {
      const s = await bridge.getAgentStatus();
      setStatus(s);
    } catch {
      setStatus(null);
    } finally {
      setLoading(false);
    }
  }, [bridge]);

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      await refresh();
      if (!cancelled) setTimeout(poll, 5000);
    };
    poll();
    return () => { cancelled = true; };
  }, [refresh]);

  const handleStart = async () => {
    setAction('start');
    setError('');
    try {
      await bridge.startAgent(config);
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setAction('');
    }
  };

  const handleStop = async () => {
    setAction('stop');
    setError('');
    try {
      await bridge.stopAgent();
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setAction('');
    }
  };

  const handleRestart = async () => {
    setAction('restart');
    setError('');
    try {
      await bridge.restartAgent(config);
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setAction('');
    }
  };

  if (loading) return <CircularProgress />;

  const running = status?.running ?? false;

  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 2 }}>Agent</Typography>

      <Stack direction="row" spacing={1} sx={{ mb: 2 }}>
        <Chip
          label={running ? 'Running' : 'Stopped'}
          color={running ? 'success' : 'default'}
          variant="outlined"
        />
        {running && status && (
          <>
            <Chip label={`Server: ${status.server_url}`} variant="outlined" size="small" />
            <Chip label={`ID: ${status.agent_id || '(any)'}`} variant="outlined" size="small" />
            {status.target && <Chip label={`Target: ${status.target}`} variant="outlined" size="small" />}
          </>
        )}
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError('')}>{error}</Alert>}

      <Card sx={{ mb: 2 }}>
        <CardContent>
          <Typography variant="subtitle1" sx={{ mb: 2, fontWeight: 600 }}>Configuration</Typography>
          <ServerURLField
            value={config.server_url}
            onChange={url => setConfig({ ...config, server_url: url })}
            savedSessions={savedSessions}
            helperText="Leave empty to use saved session"
            sx={{ mb: 2 }}
          />
          <TextField
            fullWidth label="Target" value={config.target}
            onChange={e => setConfig({ ...config, target: e.target.value })}
            helperText="TCP address (e.g. 127.0.0.1:22). Empty for dynamic target mode."
            sx={{ mb: 2 }}
          />
          <TextField
            fullWidth label="Agent ID" value={config.agent_id}
            onChange={e => setConfig({ ...config, agent_id: e.target.value })}
            helperText="Routing identifier (e.g. mydevbox)"
            sx={{ mb: 2 }}
          />
          <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
            <TextField
              label="Batch Size" type="number" value={config.batch_size}
              onChange={e => setConfig({ ...config, batch_size: Number(e.target.value) })}
              inputProps={{ min: 1024, max: 1048576 }}
              sx={{ width: 160 }}
            />
            <TextField
              label="Concurrency" type="number" value={config.concurrency}
              onChange={e => setConfig({ ...config, concurrency: Number(e.target.value) })}
              inputProps={{ min: 1, max: 4 }}
              sx={{ width: 140 }}
            />
          </Stack>
          <Stack direction="row" spacing={2}>
            <FormControlLabel
              control={<Switch checked={config.compress} onChange={e => setConfig({ ...config, compress: e.target.checked })} />}
              label="Compress"
            />
            <FormControlLabel
              control={<Switch checked={config.no_auto_tune} onChange={e => setConfig({ ...config, no_auto_tune: e.target.checked })} />}
              label="No Auto-Tune"
            />
            <FormControlLabel
              control={<Switch checked={config.verbose} onChange={e => setConfig({ ...config, verbose: e.target.checked })} />}
              label="Verbose"
            />
          </Stack>
        </CardContent>
      </Card>

      <Stack direction="row" spacing={2}>
        {!running ? (
          <Button variant="contained" onClick={handleStart} disabled={!!action || !config.server_url || !config.agent_id}>
            {action === 'start' ? 'Starting...' : 'Start'}
          </Button>
        ) : (
          <>
            <Button variant="outlined" color="error" onClick={handleStop} disabled={!!action}>
              {action === 'stop' ? 'Stopping...' : 'Stop'}
            </Button>
            <Button variant="outlined" onClick={handleRestart} disabled={!!action}>
              {action === 'restart' ? 'Restarting...' : 'Restart'}
            </Button>
          </>
        )}
      </Stack>
    </Box>
  );
}
