import { useState, useEffect, useCallback } from 'react';
import {
  Box, Card, CardContent, TextField, Button, Typography,
  Alert, Chip, Stack, CircularProgress, IconButton, Table,
  TableHead, TableRow, TableCell, TableBody,
} from '@mui/material';
import DeleteIcon from '@mui/icons-material/Delete';
import type { Bridge, ConnectStatus, ConnectConfig } from './types';

interface ConnectPageProps {
  bridge: Bridge;
}

const defaultConfig: ConnectConfig = {
  server_url: '',
  agent_id: '',
  target: '',
  local: '',
  batch_size: 0,
};

export default function ConnectPage({ bridge }: ConnectPageProps) {
  const [instances, setInstances] = useState<ConnectStatus[]>([]);
  const [config, setConfig] = useState<ConnectConfig>(defaultConfig);
  const [name, setName] = useState('');
  const [loading, setLoading] = useState(true);
  const [action, setAction] = useState('');
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    try {
      const list = await bridge.getConnectInstances();
      setInstances(list);
    } catch {
      setInstances([]);
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
    if (!name) { setError('Name is required'); return; }
    if (!config.local) { setError('Local address is required'); return; }
    setAction('start');
    setError('');
    try {
      await bridge.startConnect(name, config);
      setName('');
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setAction('');
    }
  };

  const handleStop = async (n: string) => {
    setAction(`stop:${n}`);
    setError('');
    try {
      await bridge.stopConnect(n);
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setAction('');
    }
  };

  if (loading) return <CircularProgress />;

  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 2 }}>Connect</Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError('')}>{error}</Alert>}

      {instances.length > 0 && (
        <Card sx={{ mb: 2 }}>
          <CardContent>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Local</TableCell>
                  <TableCell>Server</TableCell>
                  <TableCell>Agent</TableCell>
                  <TableCell>Target</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {instances.map(inst => (
                  <TableRow key={inst.name}>
                    <TableCell>{inst.name}</TableCell>
                    <TableCell>{inst.local_addr}</TableCell>
                    <TableCell>{inst.server_url}</TableCell>
                    <TableCell>{inst.agent_id || '(any)'}</TableCell>
                    <TableCell>{inst.target || '(dynamic)'}</TableCell>
                    <TableCell>
                      <Chip label={inst.running ? 'Running' : 'Stopped'} color={inst.running ? 'success' : 'default'} size="small" variant="outlined" />
                    </TableCell>
                    <TableCell align="right">
                      <IconButton size="small" onClick={() => handleStop(inst.name)} disabled={!!action}>
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardContent>
          <Typography variant="subtitle1" sx={{ mb: 2, fontWeight: 600 }}>New Connection</Typography>
          <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
            <TextField
              label="Name" value={name}
              onChange={e => setName(e.target.value)}
              helperText="Unique instance name (e.g. my-ssh)"
              sx={{ width: 200 }}
            />
            <TextField
              label="Local Address" value={config.local}
              onChange={e => setConfig({ ...config, local: e.target.value })}
              helperText="e.g. 127.0.0.1:2222"
              sx={{ width: 200 }} required
            />
          </Stack>
          <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
            <TextField
              label="Agent ID" value={config.agent_id}
              onChange={e => setConfig({ ...config, agent_id: e.target.value })}
              sx={{ width: 200 }}
            />
            <TextField
              label="Target" value={config.target}
              onChange={e => setConfig({ ...config, target: e.target.value })}
              helperText="e.g. 127.0.0.1:22"
              sx={{ width: 200 }}
            />
            <TextField
              label="Server URL" value={config.server_url}
              onChange={e => setConfig({ ...config, server_url: e.target.value })}
              helperText="Empty = saved session"
              sx={{ flex: 1 }}
            />
          </Stack>
          <Button variant="contained" onClick={handleStart} disabled={!!action}>
            {action === 'start' ? 'Starting...' : 'Start Connection'}
          </Button>
        </CardContent>
      </Card>
    </Box>
  );
}
