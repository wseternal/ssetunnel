import React, { useState, useEffect, useRef, useCallback } from 'react';
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
  Tooltip,
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
import TerminalIcon from '@mui/icons-material/Terminal';
import { Terminal, type IDisposable } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { QRCodeSVG } from 'qrcode.react';
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip as RTooltip, Legend, ResponsiveContainer,
} from 'recharts';
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

interface MetricSnapshot {
  throughput_up_p50_bps: number;
  throughput_up_p95_bps: number;
  throughput_dn_p50_bps: number;
  throughput_dn_p95_bps: number;
  latency_p50_ms: number;
  latency_p95_ms: number;
  error_rate: number;
  active_conns: number;
  total_posts: number;
  total_errors: number;
}

interface TransportParams {
  concurrency: number;
  batch_size: number;
  compress: boolean;
}

interface TuningDecision {
  timestamp: string;
  agent_id: string;
  old_params: TransportParams;
  new_params: TransportParams;
  reason: string;
}

interface AgentMetrics {
  agent_id: string;
  snapshot: MetricSnapshot;
  params: TransportParams;
  last_decision?: TuningDecision;
}

interface MetricsOverview {
  active_agents: number;
  throughput_up_bps: number;
  throughput_dn_bps: number;
  error_rate: number;
}

interface MetricSample {
  timestamp: string;
  agent_id: string;
  throughput_up_bps: number;
  throughput_dn_bps: number;
  latency_p50_ms: number;
  latency_p95_ms: number;
  error_rate: number;
  active_conns: number;
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
  const [userRole, setUserRole] = useState(() => localStorage.getItem('userRole') || '');
  const isAdmin = userRole === 'admin';
  // roleConfirmed is true once /console/api/v1/me has resolved, preventing double-fetch
  // before the actual role is known.
  const [roleConfirmed, setRoleConfirmed] = useState(false);
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

  // Metrics / Statistics
  const [metricsOverview, setMetricsOverview] = useState<MetricsOverview | null>(null);
  const [agentMetrics, setAgentMetrics] = useState<AgentMetrics[]>([]);
  const [selectedAgentSamples, setSelectedAgentSamples] = useState<MetricSample[]>([]);
  const [selectedAgentDecisions, setSelectedAgentDecisions] = useState<TuningDecision[]>([]);
  const [selectedStatsAgent, setSelectedStatsAgent] = useState<string>('');

  // Cloud Shell
  const [shellAgent, setShellAgent] = useState<string>('');
  const [shellConnected, setShellConnected] = useState(false);
  const [shellSessionId, setShellSessionId] = useState<string>('');
  const termRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const shellAbortRef = useRef<AbortController | null>(null);
  const inputDisposableRef = useRef<IDisposable | null>(null);

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
      const res = await fetch('/console/api/v1/sessions', { headers: authHeaders() });
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
      const res = await fetch('/console/api/v1/users', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setUsers(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgents = async () => {
    try {
      const res = await fetch('/console/api/v1/agents', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setAgents(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchMetricsOverview = async () => {
    try {
      const res = await fetch('/console/api/v1/metrics/overview', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setMetricsOverview(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgentMetrics = async () => {
    try {
      const res = await fetch('/console/api/v1/metrics/agents', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setAgentMetrics(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgentSamples = async (agentID: string) => {
    try {
      const res = await fetch(`/console/api/v1/metrics/agents/${encodeURIComponent(agentID)}/samples`, { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setSelectedAgentSamples(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchAgentDecisions = async (agentID: string) => {
    try {
      const res = await fetch(`/console/api/v1/metrics/agents/${encodeURIComponent(agentID)}/decisions`, { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setSelectedAgentDecisions(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const selectStatsAgent = (agentID: string) => {
    setSelectedStatsAgent(agentID);
    if (agentID) {
      fetchAgentSamples(agentID);
      fetchAgentDecisions(agentID);
    } else {
      setSelectedAgentSamples([]);
      setSelectedAgentDecisions([]);
    }
  };

  // --- Cloud Shell ---

  // Parse SSE frames from a text chunk and return the data payloads.
  const parseSSEFrames = useCallback((text: string): string[] => {
    const frames: string[] = [];
    const lines = text.split('\n');
    let currentData = '';
    for (const line of lines) {
      if (line.startsWith('data:')) {
        currentData += line.slice(5);
      } else if (line === '' && currentData !== '') {
        frames.push(currentData);
        currentData = '';
      }
    }
    if (currentData !== '') frames.push(currentData);
    return frames;
  }, []);

  const disconnectShell = useCallback(() => {
    if (shellAbortRef.current) {
      shellAbortRef.current.abort();
      shellAbortRef.current = null;
    }
    if (inputDisposableRef.current) {
      inputDisposableRef.current.dispose();
      inputDisposableRef.current = null;
    }
    if (xtermRef.current) {
      xtermRef.current.writeln('\r\n\x1b[33m[Disconnected]\x1b[0m');
    }
    setShellConnected(false);
    setShellSessionId('');
  }, []);

  const connectShell = useCallback(async (agentID: string) => {
    if (!agentID || !sessionToken) return;

    // Disconnect any existing shell first.
    if (shellAbortRef.current) {
      shellAbortRef.current.abort();
    }

    // Initialize xterm if not already.
    if (!xtermRef.current && termRef.current) {
      const fitAddon = new FitAddon();
      const term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", Menlo, Monaco, monospace',
        theme: { background: '#1e1e2e', foreground: '#cdd6f4', cursor: '#f5c2e7' },
        allowTransparency: false,
        scrollback: 5000,
      });
      term.loadAddon(fitAddon);
      term.open(termRef.current);
      fitAddon.fit();
      xtermRef.current = term;
      fitAddonRef.current = fitAddon;
    }

    const term = xtermRef.current!;
    term.clear();
    term.writeln('\x1b[36m[Connecting to ' + agentID + '...]\x1b[0m');

    const abort = new AbortController();
    shellAbortRef.current = abort;

    // Generate a random connect session ID.
    const sid = Array.from(crypto.getRandomValues(new Uint8Array(16)))
      .map(b => b.toString(16).padStart(2, '0')).join('');
    setShellSessionId(sid);

    // Build SSE URL — auth via Authorization header (not query param).
    const sseURL = `/console/api/v1/shell/connect?id=${encodeURIComponent(sid)}&agent=${encodeURIComponent(agentID)}`;

    // Set up input handler: send keystrokes via POST.
    const sendInput = async (data: string) => {
      if (!shellConnected && !abort.signal.aborted) {
        // Still connecting, buffer input? No, just send.
      }
      try {
        await fetch('/console/api/v1/shell/connect-up', {
          method: 'POST',
          headers: { ...authHeaders(), 'X-SSET-Session': sid },
          body: data,
          signal: abort.signal,
        });
      } catch (e) {
        if (!abort.signal.aborted) {
          term.writeln('\x1b[31m[Send error]\x1b[0m');
        }
      }
    };

    // Set up input handler: send keystrokes via POST. Dispose previous
    // handler if any (e.g., from a prior connection that wasn't cleaned up).
    if (inputDisposableRef.current) {
      inputDisposableRef.current.dispose();
    }
    const inputDisposable = term.onData(sendInput);
    inputDisposableRef.current = inputDisposable;

    // Start SSE stream using fetch (ReadableStream) for full header control.
    try {
      const resp = await fetch(sseURL, {
        headers: { Authorization: `Bearer ${sessionToken}` },
        signal: abort.signal,
      });
      if (!resp.ok) {
        const errText = await resp.text();
        term.writeln(`\x1b[31m[Error: ${resp.status} ${errText}]\x1b[0m`);
        setShellConnected(false);
        inputDisposable.dispose();
        return;
      }

      setShellConnected(true);
      term.writeln('\x1b[32m[Connected]\x1b[0m\r\n');

      const reader = resp.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        // Process complete SSE frames.
        const frames = parseSSEFrames(buffer);
        // Keep incomplete trailing data in buffer.
        const lastDoubleNewline = buffer.lastIndexOf('\n\n');
        buffer = lastDoubleNewline >= 0 ? buffer.slice(lastDoubleNewline + 2) : buffer;

        for (const frame of frames) {
          if (frame === '') continue; // heartbeat
          // Decode base64 data (SSE frames contain base64-encoded binary data).
          try {
            const binary = atob(frame);
            term.write(binary);
          } catch {
            // If not valid base64, write as plain text (fallback).
            term.write(frame);
          }
        }
      }

      if (!abort.signal.aborted) {
        term.writeln('\r\n\x1b[33m[Connection closed by server]\x1b[0m');
      }
    } catch (e) {
      if (!abort.signal.aborted) {
        term.writeln(`\r\n\x1b[31m[Connection error: ${e}]\x1b[0m`);
      }
    } finally {
      if (inputDisposableRef.current) {
        inputDisposableRef.current.dispose();
        inputDisposableRef.current = null;
      }
      setShellConnected(false);
      setShellSessionId('');
      shellAbortRef.current = null;
    }
  }, [sessionToken, shellConnected, parseSSEFrames]);

  // Validate restored token on mount
  useEffect(() => {
    if (sessionToken) {
      fetch('/console/api/v1/me', { headers: { Authorization: `Bearer ${sessionToken}` } })
        .then(async res => {
          if (res.ok) {
            const me = await res.json();
            setUserRole(me.role ?? 'user');
            localStorage.setItem('userRole', me.role ?? 'user');
            setIsLoggedIn(true);
            setRoleConfirmed(true);
          } else {
            localStorage.removeItem('sessionToken');
            localStorage.removeItem('userRole');
            setSessionToken('');
            setUserRole('');
            setRoleConfirmed(true);
          }
        })
        .catch(() => {
          localStorage.removeItem('sessionToken');
          localStorage.removeItem('userRole');
          setSessionToken('');
          setUserRole('');
          setRoleConfirmed(true);
        });
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Reset tabIndex when role changes to avoid pointing at a non-existent tab.
  useEffect(() => { setTabIndex(0); }, [isAdmin]);

  // Cleanup xterm and shell connection on unmount.
  useEffect(() => {
    return () => {
      if (shellAbortRef.current) shellAbortRef.current.abort();
      if (xtermRef.current) { xtermRef.current.dispose(); xtermRef.current = null; }
    };
  }, []);

  // Refit terminal on window resize.
  useEffect(() => {
    const onResize = () => { if (fitAddonRef.current) fitAddonRef.current.fit(); };
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  useEffect(() => {
    if (isLoggedIn && roleConfirmed) {
      fetchSessions();
      fetchAgents();
      fetchMetricsOverview();
      fetchAgentMetrics();
      if (isAdmin) fetchUsers();
      const sessionInterval = setInterval(fetchSessions, 3000);
      const agentInterval = setInterval(fetchAgents, 10000);
      const metricsInterval = setInterval(() => { fetchMetricsOverview(); fetchAgentMetrics(); }, 10000);
      return () => { clearInterval(sessionInterval); clearInterval(agentInterval); clearInterval(metricsInterval); };
    }
  }, [isLoggedIn, isAdmin, roleConfirmed]);

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const res = await fetch('/console/api/v1/user-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, totp_code: totpCode }),
      });
      if (res.ok) {
        const data = await res.json();
        setSessionToken(data.token);
        localStorage.setItem('sessionToken', data.token);
        setUserRole(data.role ?? 'user');
        localStorage.setItem('userRole', data.role ?? 'user');
        setIsLoggedIn(true);
        setRoleConfirmed(true);
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
    fetch('/console/api/v1/logout', { method: 'POST', headers: authHeaders() }).catch(() => {});
    localStorage.removeItem('sessionToken');
    localStorage.removeItem('userRole');
    setSessionToken('');
    setUserRole('');
    setIsLoggedIn(false);
    setRoleConfirmed(false);
    setTotpEnrolled(false);
  };

  const handleUsernameBlur = async () => {
    if (!username) return;
    try {
      const res = await fetch('/console/api/v1/user-login-check', {
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
      const res = await fetch('/console/api/v1/totp/begin-setup', {
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
      const res = await fetch('/console/api/v1/totp/verify-setup', {
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
        const res = await fetch(`/console/api/v1/users/${editingUserId}`, {
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
        const res = await fetch('/console/api/v1/users', {
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
      const res = await fetch(`/console/api/v1/users/${user.id}`, {
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
      const res = await fetch(`/console/api/v1/users/${id}`, { method: 'DELETE', headers: authHeaders() });
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
        const res = await fetch(`/console/api/v1/agents/${editingAgentId}`, {
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
        const res = await fetch('/console/api/v1/agents', {
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
      const res = await fetch(`/console/api/v1/agents/${id}`, { method: 'DELETE', headers: authHeaders() });
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

  // Read-only agent columns for non-admin users (no actions).
  const readOnlyAgentColumns: AdminTableColumn<AgentConfig>[] = agentColumns.filter(
    (col) => col.key !== 'actions'
  );

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
              <Tooltip title={totpEnrolled ? 'Two-factor authentication enabled' : 'Two-factor authentication not set up — click to configure'}>
                <IconButton
                  color={totpEnrolled ? 'default' : 'warning'}
                  size="small"
                  onClick={() => setOpenTOTPDialog(true)}
                >
                  <SecurityIcon />
                </IconButton>
              </Tooltip>
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
          /* Main content */
          <Container maxWidth="lg" sx={{ mt: 4 }}>
            {isAdmin ? (
              <>
                <Paper sx={{ mb: 3 }}>
                  <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                    <Tab label="Sessions" />
                    <Tab label="Users" />
                    <Tab label="Agents" />
                    <Tab label="Statistics" />
                    <Tab label="Shell" icon={<TerminalIcon />} iconPosition="start" />
                  </Tabs>
                </Paper>

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

                {tabIndex === 3 && (
                  <Box>
                    <PageHeader
                      title="Statistics"
                      actions={
                        <Button startIcon={<RefreshIcon />} onClick={() => { fetchMetricsOverview(); fetchAgentMetrics(); }} size="small">
                          Refresh
                        </Button>
                      }
                    />
                    <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2, mb: 3 }}>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Active Agents</Typography>
                        <Typography variant="h4">{metricsOverview?.active_agents ?? 0}</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Upload Throughput</Typography>
                        <Typography variant="h4">{((metricsOverview?.throughput_up_bps ?? 0) / 1024).toFixed(1)} KB/s</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Download Throughput</Typography>
                        <Typography variant="h4">{((metricsOverview?.throughput_dn_bps ?? 0) / 1024).toFixed(1)} KB/s</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Error Rate</Typography>
                        <Typography variant="h4" color={((metricsOverview?.error_rate ?? 0) > 0.01) ? 'error.main' : 'text.primary'}>
                          {((metricsOverview?.error_rate ?? 0) * 100).toFixed(2)}%
                        </Typography>
                      </CardContent></Card>
                    </Box>

                    <Typography variant="h6" sx={{ mb: 1 }}>Per-Agent Metrics</Typography>
                    {agentMetrics.length === 0 ? (
                      <Alert severity="info" sx={{ mb: 3 }}>No metrics data yet. Enable metrics with <code>--metrics-dir</code>.</Alert>
                    ) : (
                      <Box sx={{ mb: 3 }}>
                        {agentMetrics.map((am) => (
                          <Card key={am.agent_id} sx={{ mb: 1.5, cursor: 'pointer', border: selectedStatsAgent === am.agent_id ? 2 : 0, borderColor: 'primary.main' }}
                            onClick={() => selectStatsAgent(selectedStatsAgent === am.agent_id ? '' : am.agent_id)}>
                            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                              <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 1 }}>
                                <Typography variant="subtitle1" sx={{ fontWeight: 600, fontFamily: 'monospace' }}>{am.agent_id}</Typography>
                                <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap' }}>
                                  <Chip size="small" label={`Up: ${(am.snapshot.throughput_up_p50_bps / 1024).toFixed(1)} KB/s`} />
                                  <Chip size="small" label={`Dn: ${(am.snapshot.throughput_dn_p50_bps / 1024).toFixed(1)} KB/s`} />
                                  <Chip size="small" label={`P95: ${am.snapshot.latency_p95_ms.toFixed(0)}ms`} />
                                  <Chip size="small" label={`Batch: ${(am.params.batch_size / 1024).toFixed(0)}KB`} />
                                  <Chip size="small" label={`Conc: ${am.params.concurrency}`} />
                                  <Chip size="small" label={am.params.compress ? 'gzip' : 'raw'} color={am.params.compress ? 'info' : 'default'} />
                                  <Chip size="small" label={`Err: ${(am.snapshot.error_rate * 100).toFixed(1)}%`} color={am.snapshot.error_rate > 0.01 ? 'error' : 'default'} />
                                </Box>
                              </Box>
                              {am.last_decision && (
                                <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
                                  Last tune: {am.last_decision.reason} ({new Date(am.last_decision.timestamp).toLocaleTimeString()})
                                </Typography>
                              )}
                            </CardContent>
                          </Card>
                        ))}
                      </Box>
                    )}

                    {selectedStatsAgent && (
                      <Box>
                        <Typography variant="h6" sx={{ mb: 1 }}>Agent: {selectedStatsAgent}</Typography>
                        {selectedAgentSamples.length > 0 ? (
                          <Card sx={{ mb: 2 }}>
                            <CardContent>
                              <Typography variant="subtitle2" sx={{ mb: 1 }}>Throughput (last 24h)</Typography>
                              <ResponsiveContainer width="100%" height={200}>
                                <LineChart data={selectedAgentSamples}>
                                  <CartesianGrid strokeDasharray="3 3" />
                                  <XAxis dataKey="timestamp" tickFormatter={(v: string) => new Date(v).toLocaleTimeString()} fontSize={10} />
                                  <YAxis fontSize={10} tickFormatter={(v: number) => `${(v / 1024).toFixed(0)}K`} />
                                  <RTooltip labelFormatter={(v: string) => new Date(v).toLocaleString()} />
                                  <Legend />
                                  <Line type="monotone" dataKey="throughput_up_bps" name="Upload" stroke="#1976d2" dot={false} strokeWidth={1.5} />
                                  <Line type="monotone" dataKey="throughput_dn_bps" name="Download" stroke="#2e7d32" dot={false} strokeWidth={1.5} />
                                </LineChart>
                              </ResponsiveContainer>
                              <Typography variant="subtitle2" sx={{ mb: 1, mt: 2 }}>Latency (ms)</Typography>
                              <ResponsiveContainer width="100%" height={150}>
                                <LineChart data={selectedAgentSamples}>
                                  <CartesianGrid strokeDasharray="3 3" />
                                  <XAxis dataKey="timestamp" tickFormatter={(v: string) => new Date(v).toLocaleTimeString()} fontSize={10} />
                                  <YAxis fontSize={10} />
                                  <RTooltip labelFormatter={(v: string) => new Date(v).toLocaleString()} />
                                  <Legend />
                                  <Line type="monotone" dataKey="latency_p50_ms" name="P50" stroke="#ed6c02" dot={false} strokeWidth={1.5} />
                                  <Line type="monotone" dataKey="latency_p95_ms" name="P95" stroke="#d32f2f" dot={false} strokeWidth={1.5} />
                                </LineChart>
                              </ResponsiveContainer>
                            </CardContent>
                          </Card>
                        ) : (
                          <Alert severity="info" sx={{ mb: 2 }}>No time-series samples available for this agent yet.</Alert>
                        )}

                        {selectedAgentDecisions.length > 0 && (
                          <Card>
                            <CardContent>
                              <Typography variant="subtitle2" sx={{ mb: 1 }}>Tuning Decisions</Typography>
                              {selectedAgentDecisions.map((d, i) => (
                                <Box key={i} sx={{ mb: 1, pb: 1, borderBottom: i < selectedAgentDecisions.length - 1 ? 1 : 0, borderColor: 'divider' }}>
                                  <Typography variant="caption" color="text.secondary">
                                    {new Date(d.timestamp).toLocaleString()}
                                  </Typography>
                                  <Typography variant="body2">{d.reason}</Typography>
                                  <Typography variant="caption" sx={{ fontFamily: 'monospace' }}>
                                    batch: {d.old_params.batch_size} → {d.new_params.batch_size} | conc: {d.old_params.concurrency} → {d.new_params.concurrency} | compress: {String(d.old_params.compress)} → {String(d.new_params.compress)}
                                  </Typography>
                                </Box>
                              ))}
                            </CardContent>
                          </Card>
                        )}
                      </Box>
                    )}
                  </Box>
                )}

                {tabIndex === 4 && (
                  <Box>
                    <PageHeader
                      title="Cloud Shell"
                      actions={
                        <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
                          <FormControl size="small" sx={{ minWidth: 180 }}>
                            <InputLabel>Agent</InputLabel>
                            <Select
                              value={shellAgent}
                              label="Agent"
                              onChange={(e) => setShellAgent(e.target.value)}
                              disabled={shellConnected}
                            >
                              {agentMetrics.map((am) => (
                                <MenuItem key={am.agent_id} value={am.agent_id}>{am.agent_id}</MenuItem>
                              ))}
                            </Select>
                          </FormControl>
                          {shellConnected ? (
                            <Button variant="outlined" color="warning" onClick={disconnectShell}>
                              Disconnect
                            </Button>
                          ) : (
                            <Button
                              variant="contained"
                              startIcon={<TerminalIcon />}
                              onClick={() => connectShell(shellAgent)}
                              disabled={!shellAgent}
                            >
                              Connect
                            </Button>
                          )}
                        </Box>
                      }
                    />
                    {agentMetrics.length === 0 && (
                      <Alert severity="info" sx={{ mb: 2 }}>No agents connected. Start an agent to use the cloud shell.</Alert>
                    )}
                    <Paper
                      sx={{
                        bgcolor: '#1e1e2e',
                        p: 0.5,
                        borderRadius: 2,
                        minHeight: 400,
                        '& .xterm': { p: 1 },
                        '& .xterm-viewport': { borderRadius: 1 },
                      }}
                    >
                      <div ref={termRef} style={{ height: 450 }} />
                    </Paper>
                    {shellConnected && (
                      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: 'block' }}>
                        Session: {shellSessionId} | Agent: {shellAgent}
                      </Typography>
                    )}
                  </Box>
                )}
              </>
            ) : (
              <>
                <Paper sx={{ mb: 3 }}>
                  <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                    <Tab label="Sessions" />
                    <Tab label="Agents" />
                    <Tab label="Statistics" />
                    <Tab label="Shell" icon={<TerminalIcon />} iconPosition="start" />
                  </Tabs>
                </Paper>

                {tabIndex === 0 && (
                  <Box>
                    <PageHeader
                      title={`My Sessions (${sessions.length})`}
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
                          title="No active sessions"
                        />
                      }
                    />
                  </Box>
                )}

                {tabIndex === 1 && (
                  <Box>
                    <PageHeader
                      title={`Agents (${agents.length})`}
                      actions={
                        <Button startIcon={<RefreshIcon />} onClick={fetchAgents} size="small">
                          Refresh
                        </Button>
                      }
                    />
                    <AdminTable
                      columns={readOnlyAgentColumns}
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

                {tabIndex === 2 && (
                  <Box>
                    <PageHeader
                      title="Statistics"
                      actions={
                        <Button startIcon={<RefreshIcon />} onClick={() => { fetchMetricsOverview(); fetchAgentMetrics(); }} size="small">
                          Refresh
                        </Button>
                      }
                    />
                    <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 2, mb: 3 }}>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Active Agents</Typography>
                        <Typography variant="h4">{metricsOverview?.active_agents ?? 0}</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Upload Throughput</Typography>
                        <Typography variant="h4">{((metricsOverview?.throughput_up_bps ?? 0) / 1024).toFixed(1)} KB/s</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Download Throughput</Typography>
                        <Typography variant="h4">{((metricsOverview?.throughput_dn_bps ?? 0) / 1024).toFixed(1)} KB/s</Typography>
                      </CardContent></Card>
                      <Card><CardContent>
                        <Typography variant="caption" color="text.secondary">Error Rate</Typography>
                        <Typography variant="h4" color={((metricsOverview?.error_rate ?? 0) > 0.01) ? 'error.main' : 'text.primary'}>
                          {((metricsOverview?.error_rate ?? 0) * 100).toFixed(2)}%
                        </Typography>
                      </CardContent></Card>
                    </Box>

                    <Typography variant="h6" sx={{ mb: 1 }}>Per-Agent Metrics</Typography>
                    {agentMetrics.length === 0 ? (
                      <Alert severity="info" sx={{ mb: 3 }}>No metrics data yet.</Alert>
                    ) : (
                      <Box sx={{ mb: 3 }}>
                        {agentMetrics.map((am) => (
                          <Card key={am.agent_id} sx={{ mb: 1.5, cursor: 'pointer', border: selectedStatsAgent === am.agent_id ? 2 : 0, borderColor: 'primary.main' }}
                            onClick={() => selectStatsAgent(selectedStatsAgent === am.agent_id ? '' : am.agent_id)}>
                            <CardContent sx={{ py: 1.5, '&:last-child': { pb: 1.5 } }}>
                              <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 1 }}>
                                <Typography variant="subtitle1" sx={{ fontWeight: 600, fontFamily: 'monospace' }}>{am.agent_id}</Typography>
                                <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap' }}>
                                  <Chip size="small" label={`Up: ${(am.snapshot.throughput_up_p50_bps / 1024).toFixed(1)} KB/s`} />
                                  <Chip size="small" label={`Dn: ${(am.snapshot.throughput_dn_p50_bps / 1024).toFixed(1)} KB/s`} />
                                  <Chip size="small" label={`P95: ${am.snapshot.latency_p95_ms.toFixed(0)}ms`} />
                                  <Chip size="small" label={`Err: ${(am.snapshot.error_rate * 100).toFixed(1)}%`} color={am.snapshot.error_rate > 0.01 ? 'error' : 'default'} />
                                </Box>
                              </Box>
                            </CardContent>
                          </Card>
                        ))}
                      </Box>
                    )}

                    {selectedStatsAgent && (
                      <Box>
                        <Typography variant="h6" sx={{ mb: 1 }}>Agent: {selectedStatsAgent}</Typography>
                        {selectedAgentSamples.length > 0 ? (
                          <Card sx={{ mb: 2 }}>
                            <CardContent>
                              <Typography variant="subtitle2" sx={{ mb: 1 }}>Throughput (last 24h)</Typography>
                              <ResponsiveContainer width="100%" height={200}>
                                <LineChart data={selectedAgentSamples}>
                                  <CartesianGrid strokeDasharray="3 3" />
                                  <XAxis dataKey="timestamp" tickFormatter={(v: string) => new Date(v).toLocaleTimeString()} fontSize={10} />
                                  <YAxis fontSize={10} tickFormatter={(v: number) => `${(v / 1024).toFixed(0)}K`} />
                                  <RTooltip labelFormatter={(v: string) => new Date(v).toLocaleString()} />
                                  <Legend />
                                  <Line type="monotone" dataKey="throughput_up_bps" name="Upload" stroke="#1976d2" dot={false} strokeWidth={1.5} />
                                  <Line type="monotone" dataKey="throughput_dn_bps" name="Download" stroke="#2e7d32" dot={false} strokeWidth={1.5} />
                                </LineChart>
                              </ResponsiveContainer>
                              <Typography variant="subtitle2" sx={{ mb: 1, mt: 2 }}>Latency (ms)</Typography>
                              <ResponsiveContainer width="100%" height={150}>
                                <LineChart data={selectedAgentSamples}>
                                  <CartesianGrid strokeDasharray="3 3" />
                                  <XAxis dataKey="timestamp" tickFormatter={(v: string) => new Date(v).toLocaleTimeString()} fontSize={10} />
                                  <YAxis fontSize={10} />
                                  <RTooltip labelFormatter={(v: string) => new Date(v).toLocaleString()} />
                                  <Legend />
                                  <Line type="monotone" dataKey="latency_p50_ms" name="P50" stroke="#ed6c02" dot={false} strokeWidth={1.5} />
                                  <Line type="monotone" dataKey="latency_p95_ms" name="P95" stroke="#d32f2f" dot={false} strokeWidth={1.5} />
                                </LineChart>
                              </ResponsiveContainer>
                            </CardContent>
                          </Card>
                        ) : (
                          <Alert severity="info" sx={{ mb: 2 }}>No time-series samples available for this agent yet.</Alert>
                        )}
                      </Box>
                    )}
                  </Box>
                )}

                {tabIndex === 3 && (
                  <Box>
                    <PageHeader
                      title="Cloud Shell"
                      actions={
                        <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
                          <FormControl size="small" sx={{ minWidth: 180 }}>
                            <InputLabel>Agent</InputLabel>
                            <Select
                              value={shellAgent}
                              label="Agent"
                              onChange={(e) => setShellAgent(e.target.value)}
                              disabled={shellConnected}
                            >
                              {agents.map((a) => (
                                <MenuItem key={a.id} value={a.agent_id ?? ''}>{a.agent_id ?? '(default)'}</MenuItem>
                              ))}
                            </Select>
                          </FormControl>
                          {shellConnected ? (
                            <Button variant="outlined" color="warning" onClick={disconnectShell}>
                              Disconnect
                            </Button>
                          ) : (
                            <Button
                              variant="contained"
                              startIcon={<TerminalIcon />}
                              onClick={() => connectShell(shellAgent)}
                              disabled={!shellAgent}
                            >
                              Connect
                            </Button>
                          )}
                        </Box>
                      }
                    />
                    {agents.length === 0 && (
                      <Alert severity="info" sx={{ mb: 2 }}>No agents configured. Contact an admin to add an agent.</Alert>
                    )}
                    <Paper
                      sx={{
                        bgcolor: '#1e1e2e',
                        p: 0.5,
                        borderRadius: 2,
                        minHeight: 400,
                        '& .xterm': { p: 1 },
                        '& .xterm-viewport': { borderRadius: 1 },
                      }}
                    >
                      <div ref={termRef} style={{ height: 450 }} />
                    </Paper>
                    {shellConnected && (
                      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: 'block' }}>
                        Session: {shellSessionId} | Agent: {shellAgent}
                      </Typography>
                    )}
                  </Box>
                )}
              </>
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
