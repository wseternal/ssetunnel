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
  ToggleButton,
  ToggleButtonGroup,
} from '@mui/material';
import { useTheme } from '@mui/material/styles';
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
import DesktopWindowsIcon from '@mui/icons-material/DesktopWindows';
import FullscreenIcon from '@mui/icons-material/Fullscreen';
import FullscreenExitIcon from '@mui/icons-material/FullscreenExit';
import ZoomInIcon from '@mui/icons-material/ZoomIn';
import PaletteIcon from '@mui/icons-material/Palette';
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
  agent_id?: string;
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

interface ConnectedAgent {
  agent_id: string;
  session_id: string;
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

interface DesktopLogEntry {
  ts: string;
  sev: 'info' | 'warn' | 'error';
  src: 'agent' | 'server';
  msg: string;
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
  { key: 'agent_id', label: 'Agent ID', render: (r) => (
    <Typography sx={{ fontFamily: 'monospace', fontSize: '0.8125rem' }}>{r.agent_id || r.id}</Typography>
  )},
  { key: 'up', label: 'Bytes Received (Up)', render: (r) => `${(r.bytes_received / 1024).toFixed(1)} KB` },
  { key: 'down', label: 'Bytes Sent (Down)', render: (r) => `${(r.bytes_sent / 1024).toFixed(1)} KB` },
  { key: 'at', label: 'Connected At', render: (r) => new Date(r.created_at).toLocaleTimeString() },
  { key: 'addr', label: 'Remote Addr', render: (r) => (
    <Typography sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>{r.remote_addr}</Typography>
  )},
  { key: 'status', label: 'Status', render: () => <StatusPill tone="success" label="Active" /> },
];

const MAGNIFIER_ZOOM = 3;
const MAGNIFIER_SIZE = 180;

interface ShellTheme {
  label: string;
  background: string;
  foreground: string;
  cursor: string;
  selectionBackground: string;
  black: string;
  red: string;
  green: string;
  yellow: string;
  blue: string;
  magenta: string;
  cyan: string;
  white: string;
  brightBlack: string;
  brightRed: string;
  brightGreen: string;
  brightYellow: string;
  brightBlue: string;
  brightMagenta: string;
  brightCyan: string;
  brightWhite: string;
}

const SHELL_THEMES: Record<string, ShellTheme> = {
  dark: {
    label: 'Dark',
    background: '#1e1e2e', foreground: '#cdd6f4', cursor: '#f5c2e7', selectionBackground: '#585b7066',
    black: '#45475a', red: '#f38ba8', green: '#a6e3a1', yellow: '#f9e2af',
    blue: '#89b4fa', magenta: '#f5c2e7', cyan: '#94e2d5', white: '#bac2de',
    brightBlack: '#585b70', brightRed: '#f38ba8', brightGreen: '#a6e3a1', brightYellow: '#f9e2af',
    brightBlue: '#89b4fa', brightMagenta: '#f5c2e7', brightCyan: '#94e2d5', brightWhite: '#a6adc8',
  },
  solarizedLight: {
    label: 'Solarized Light',
    background: '#fdf6e3', foreground: '#657b83', cursor: '#cb4b16', selectionBackground: '#eee8d5',
    black: '#073642', red: '#dc322f', green: '#859900', yellow: '#b58900',
    blue: '#268bd2', magenta: '#d33682', cyan: '#2aa198', white: '#eee8d5',
    brightBlack: '#002b36', brightRed: '#cb4b16', brightGreen: '#586e75', brightYellow: '#657b83',
    brightBlue: '#839496', brightMagenta: '#6c71c4', brightCyan: '#93a1a1', brightWhite: '#fdf6e3',
  },
};
const SHELL_THEME_KEYS = Object.keys(SHELL_THEMES);

export default function App() {
  const theme = useTheme();
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
  const [metricsDuration, setMetricsDuration] = useState<string>('1h');

  const durationToRange = (d: string): { from: string; to: string } => {
    const now = new Date();
    const hours: Record<string, number> = { '1h': 1, '6h': 6, '12h': 12, '1d': 24, '7d': 168 };
    const h = hours[d] ?? 1;
    const from = new Date(now.getTime() - h * 3600 * 1000);
    return { from: from.toISOString(), to: now.toISOString() };
  };

  // Cloud Shell
  const [connectedAgents, setConnectedAgents] = useState<ConnectedAgent[]>([]);
  const [shellAgent, setShellAgent] = useState<string>('');
  const [shellConnected, setShellConnected] = useState(false);
  const [shellSessionId, setShellSessionId] = useState<string>('');
  const [shellPersistentId, setShellPersistentId] = useState<string>('');
  const [shellReattached, setShellReattached] = useState(false);
  const [isShellFullscreen, setIsShellFullscreen] = useState(false);
  const [shellThemeKey, setShellThemeKey] = useState(() => {
    const stored = localStorage.getItem('shellTheme');
    return stored && stored in SHELL_THEMES ? stored : 'solarizedLight';
  });
  const termRef = useRef<HTMLDivElement>(null);
  const shellContainerRef = useRef<HTMLDivElement>(null);
  const shellLineBufRef = useRef<string>('');
  const shellPersistentIdRef = useRef<string>('');
  const xtermRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const shellAbortRef = useRef<AbortController | null>(null);
  const inputDisposableRef = useRef<IDisposable | null>(null);
  const resizeDisposableRef = useRef<IDisposable | null>(null);

  // Remote Desktop
  const [desktopAgent, setDesktopAgent] = useState<string>('');
  const [desktopConnected, setDesktopConnected] = useState(false);
  const [desktopSessionId, setDesktopSessionId] = useState<string>('');
  const [screenWidth, setScreenWidth] = useState<number>(0);
  const [screenHeight, setScreenHeight] = useState<number>(0);
  const screenWidthRef = useRef<number>(0);
  const screenHeightRef = useRef<number>(0);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const desktopAbortRef = useRef<AbortController | null>(null);
  const desktopImgRef = useRef<HTMLImageElement | null>(null);
  const desktopContainerRef = useRef<HTMLDivElement | null>(null);
  const desktopMouseMoveRef = useRef<number>(0); // throttle: last mouse_move timestamp
  const [desktopMetrics, setDesktopMetrics] = useState<MetricSnapshot | null>(null);

  // Command palette state
  const [paletteOpen, setPaletteOpen] = useState(false);
  const metaDownRef = useRef(false);
  const paletteOpenRef = useRef(false);

  // Keep paletteOpenRef in sync so the keyboard handler can read
  // palette state without re-attaching listeners on every toggle.
  useEffect(() => { paletteOpenRef.current = paletteOpen; }, [paletteOpen]);

  // Text editor state (Send Text command palette action)
  const [textEditorOpen, setTextEditorOpen] = useState(false);
  const [textEditorContent, setTextEditorContent] = useState('');

  // Magnifier lens state
  const [magnifierOn, setMagnifierOn] = useState(false);
  const magnifierPosRef = useRef<{ x: number; y: number }>({ x: 0, y: 0 });
  const magnifierRafRef = useRef<number>(0);
  const magnifierLensRef = useRef<HTMLDivElement | null>(null);
  const magnifierLastSrcRef = useRef<string>('');
  const [desktopLogs, setDesktopLogs] = useState<DesktopLogEntry[]>([]);
  const desktopLogRef = useRef<HTMLDivElement | null>(null);
  const MAX_DESKTOP_LOGS = 200;
  // Input ack tooltip: shows live feedback when the agent receives input events.
  const [desktopTooltip, setDesktopTooltip] = useState<string>('');
  const desktopTooltipTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const authHeaders = (): HeadersInit => sessionToken ? { Authorization: `Bearer ${sessionToken}` } : {};

  // Format an InputAck into a brief human-readable tooltip label.
  const formatInputAckLabel = (ack: { type?: string; detail?: string }): string => {
    const t = ack.type ?? '';
    const d = ack.detail ?? '';
    switch (t) {
      case 'mouse_click': return d ? `Click ${d}` : 'Click';
      case 'mouse_scroll': return d ? `Scroll ${d}` : 'Scroll';
      case 'mouse_move': return '';
      case 'key_tap': return d ? `Key ${d}` : 'Key tap';
      case 'key_toggle': return d ? `Toggle ${d}` : 'Toggle';
      case 'type_text': return d ? `Type "${d}"` : 'Type';
      case 'mouse_drag': return d ? `Drag ${d}` : 'Drag';
      default: return t;
    }
  };

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

  const fetchConnectedAgents = async () => {
    try {
      const res = await fetch('/console/api/v1/connected-agents', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) setConnectedAgents(await res.json());
    } catch (e) {
      console.error(e);
    }
  };

  const fetchShellSessions = useCallback(async () => {
    try {
      const res = await fetch('/console/api/v1/shell/sessions', { headers: authHeaders() });
      if (checkAuth(res) && res.ok) {
        const sessions = await res.json();
        return sessions || [];
      }
    } catch (e) {
      console.error(e);
    }
    return [];
  }, [sessionToken]); // eslint-disable-line react-hooks/exhaustive-deps

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

  const fetchAgentSamples = async (agentID: string, duration?: string) => {
    try {
      const { from, to } = durationToRange(duration ?? metricsDuration);
      const params = new URLSearchParams({ from, to });
      const res = await fetch(`/console/api/v1/metrics/agents/${encodeURIComponent(agentID)}/samples?${params}`, { headers: authHeaders() });
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

  // Re-fetch samples when duration changes.
  useEffect(() => {
    if (selectedStatsAgent) {
      fetchAgentSamples(selectedStatsAgent, metricsDuration);
    }
  }, [metricsDuration]); // eslint-disable-line react-hooks/exhaustive-deps

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

  const disconnectShell = useCallback(async () => {
    // Explicitly terminate the persistent shell session on the server.
    // Await to prevent race where a fast reconnect creates a new session
    // that the still-in-flight DELETE would then kill.
    // Use ref to avoid stale closure — sendInput captures disconnectShell
    // before shellPersistentId state is populated.
    const persistentId = shellPersistentIdRef.current;
    if (persistentId) {
      try {
        await fetch('/console/api/v1/shell/sessions/delete', {
          method: 'POST',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: persistentId }),
        });
      } catch { /* best-effort server cleanup */ }
    }
    if (shellAbortRef.current) {
      shellAbortRef.current.abort();
      shellAbortRef.current = null;
    }
    if (inputDisposableRef.current) {
      inputDisposableRef.current.dispose();
      inputDisposableRef.current = null;
    }
    if (resizeDisposableRef.current) {
      resizeDisposableRef.current.dispose();
      resizeDisposableRef.current = null;
    }
    if (xtermRef.current) {
      xtermRef.current.writeln('\r\n\x1b[33m[Disconnected]\x1b[0m');
    }
    setShellConnected(false);
    setShellSessionId('');
    setShellPersistentId('');
    shellPersistentIdRef.current = '';
    setShellReattached(false);
    fetchShellSessions();
  }, [fetchShellSessions]); // eslint-disable-line react-hooks/exhaustive-deps

  const connectShell = useCallback(async (agentID: string, reattachId?: string) => {
    if (!agentID) return;

    // Disconnect any existing shell first.
    if (shellAbortRef.current) {
      shellAbortRef.current.abort();
    }

    // If xterm exists but its DOM element is detached (Shell tab was unmounted),
    // dispose and recreate — otherwise output goes to an invisible orphaned node.
    if (xtermRef.current && termRef.current && !termRef.current.contains(xtermRef.current.element)) {
      xtermRef.current.dispose();
      xtermRef.current = null;
      fitAddonRef.current = null;
    }

    // Initialize xterm if not already.
    if (!xtermRef.current && termRef.current) {
      const fitAddon = new FitAddon();
      const term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", Menlo, Monaco, monospace',
        theme: SHELL_THEMES[shellThemeKey],
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

    // If no explicit reattachId, check for existing sessions for this agent.
    let effectiveReattachId = reattachId;
    if (!effectiveReattachId) {
      const sessions = await fetchShellSessions();
      if (sessions) {
        const existing = (sessions as { id: string; agent_id: string }[]).find(
          (s: { agent_id: string }) => s.agent_id === agentID
        );
        if (existing) {
          effectiveReattachId = existing.id;
        }
      }
    }

    if (effectiveReattachId) {
      term.clear();
      term.writeln('\x1b[36m[Reattaching to existing session...]\x1b[0m');
      setShellReattached(true);
    } else {
      term.clear();
      term.writeln('\x1b[36m[Connecting to ' + agentID + '...]\x1b[0m');
      setShellReattached(false);
    }

    const abort = new AbortController();
    shellAbortRef.current = abort;

    // Generate a random connect session ID.
    const sid = Array.from(crypto.getRandomValues(new Uint8Array(16)))
      .map(b => b.toString(16).padStart(2, '0')).join('');
    setShellSessionId(sid);

    // Build SSE URL — auth via Authorization header (not query param).
    let sseURL = `/console/api/v1/shell/connect?id=${encodeURIComponent(sid)}&agent=${encodeURIComponent(agentID)}`;
    if (effectiveReattachId) {
      sseURL += `&reattach=${encodeURIComponent(effectiveReattachId)}`;
    }

    // Set up input handler: send keystrokes via POST.
    shellLineBufRef.current = '';
    const sendInput = async (data: string) => {
      // Track typed line to detect exit/logout commands.
      // Reset on line-cancel controls (Ctrl-U/Ctrl-C/etc.) so stale
      // buffer content doesn't trigger a false disconnect.
      for (const ch of data) {
        if (ch === '\r' || ch === '\n') {
          const cmd = shellLineBufRef.current.trim();
          shellLineBufRef.current = '';
          if (cmd === 'exit' || cmd === 'logout') {
            setTimeout(() => disconnectShell(), 500);
          }
        } else if (ch === '\x7f' || ch === '\b') {
          shellLineBufRef.current = shellLineBufRef.current.slice(0, -1);
        } else if (ch === '\x04') {
          // Ctrl-D: EOF — disconnect immediately.
          shellLineBufRef.current = '';
          setTimeout(() => disconnectShell(), 300);
        } else if (ch < ' ') {
          // Control characters (Ctrl-U, Ctrl-C, ESC, etc.): reset line buffer.
          shellLineBufRef.current = '';
        } else {
          shellLineBufRef.current += ch;
        }
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
      let resp = await fetch(sseURL, {
        headers: authHeaders(),
        signal: abort.signal,
      });

      // 404 reattach fallback: session expired between listing and connect.
      // Clear reattach ID, rebuild URL, and retry once as a fresh connection.
      if (!resp.ok && resp.status === 404 && effectiveReattachId) {
        term.writeln('\x1b[33m[Previous session expired, creating new session...]\x1b[0m');
        effectiveReattachId = '';
        setShellReattached(false);
        sseURL = `/console/api/v1/shell/connect?id=${encodeURIComponent(sid)}&agent=${encodeURIComponent(agentID)}`;
        resp = await fetch(sseURL, {
          headers: authHeaders(),
          signal: abort.signal,
        });
      }

      if (!resp.ok) {
        const errText = await resp.text();
        term.writeln(`\x1b[31m[Error: ${resp.status} ${errText}]\x1b[0m`);
        setShellConnected(false);
        inputDisposable.dispose();
        return;
      }

      // Read the persistent session ID from the response header.
      const persistentId = resp.headers.get('X-SSET-Shell-Session') || '';
      setShellPersistentId(persistentId);
      shellPersistentIdRef.current = persistentId;

      setShellConnected(true);
      if (effectiveReattachId) {
        term.writeln('\x1b[32m[Reattached — scrollback restored]\x1b[0m\r\n');
      } else {
        term.writeln('\x1b[32m[Connected]\x1b[0m\r\n');
      }

      // Set up resize handler: forward xterm.js dimensions to the PTY.
      if (resizeDisposableRef.current) {
        resizeDisposableRef.current.dispose();
      }
      const sendResize = (cols: number, rows: number) => {
        fetch('/console/api/v1/shell/resize', {
          method: 'POST',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: sid, cols, rows }),
          signal: abort.signal,
        }).catch(() => {});
      };
      resizeDisposableRef.current = term.onResize(({ cols, rows }) => sendResize(cols, rows));
      // Send initial size so the PTY matches xterm.js from the start.
      sendResize(term.cols, term.rows);

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
          // Convert to Uint8Array to preserve all byte values — passing a JS
          // string to term.write() causes UTF-8 re-encoding which corrupts
          // bytes > 127 (e.g. ANSI 256-color, UTF-8 shell output).
          try {
            const binary = atob(frame);
            const bytes = new Uint8Array(binary.length);
            for (let i = 0; i < binary.length; i++) {
              bytes[i] = binary.charCodeAt(i);
            }
            term.write(bytes);
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
      if (resizeDisposableRef.current) {
        resizeDisposableRef.current.dispose();
        resizeDisposableRef.current = null;
      }
      setShellConnected(false);
      setShellSessionId('');
      setShellPersistentId('');
      shellPersistentIdRef.current = '';
      if (shellAbortRef.current === abort) shellAbortRef.current = null;
      fetchShellSessions();
    }
  }, [sessionToken, parseSSEFrames, fetchShellSessions, shellThemeKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // --- Remote Desktop ---

  const sendDesktopInput = useCallback(async (sid: string, event: Record<string, unknown>, signal: AbortSignal) => {
    try {
      await fetch('/console/api/v1/remoteapp/connect-up', {
        method: 'POST',
        headers: { ...authHeaders(), 'X-SSET-Session': sid, 'Content-Type': 'application/json' },
        body: JSON.stringify(event),
        signal,
      });
    } catch {
      // Ignore send errors during interaction
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // resetDesktopState clears all desktop session state (magnifier, tooltip,
  // metrics, connection). Called by both disconnectDesktop and the finally
  // block of connectDesktop to avoid duplicated cleanup.
  const resetDesktopState = useCallback(() => {
    setDesktopConnected(false);
    setDesktopSessionId('');
    setScreenWidth(0);
    setScreenHeight(0);
    screenWidthRef.current = 0;
    screenHeightRef.current = 0;
    setDesktopMetrics(null);
    setDesktopLogs([]);
    setDesktopTooltip('');
    setMagnifierOn(false);
    magnifierPosRef.current = { x: 0, y: 0 };
    magnifierLastSrcRef.current = '';
    if (desktopImgRef.current) {
      desktopImgRef.current.src = '';
    }
    if (magnifierRafRef.current) {
      cancelAnimationFrame(magnifierRafRef.current);
      magnifierRafRef.current = 0;
    }
    if (desktopTooltipTimerRef.current) {
      clearTimeout(desktopTooltipTimerRef.current);
      desktopTooltipTimerRef.current = null;
    }
    setPaletteOpen(false);
    metaDownRef.current = false;
    setTextEditorOpen(false);
    setTextEditorContent('');
  }, []);

  const disconnectDesktop = useCallback(() => {
    if (desktopAbortRef.current) {
      desktopAbortRef.current.abort();
      desktopAbortRef.current = null;
    }
    resetDesktopState();
  }, [resetDesktopState]);

  const connectDesktop = useCallback(async (agentID: string) => {
    if (!agentID) return;

    if (desktopAbortRef.current) {
      desktopAbortRef.current.abort();
    }

    const abort = new AbortController();
    desktopAbortRef.current = abort;

    const sid = Array.from(crypto.getRandomValues(new Uint8Array(16)))
      .map(b => b.toString(16).padStart(2, '0')).join('');
    setDesktopSessionId(sid);

    const sseURL = `/console/api/v1/remoteapp/connect?id=${encodeURIComponent(sid)}&agent=${encodeURIComponent(agentID)}`;

    try {
      const resp = await fetch(sseURL, {
        headers: authHeaders(),
        signal: abort.signal,
      });
      if (!resp.ok) {
        setError(`Remote desktop error: ${resp.status} ${await resp.text()}`);
        setDesktopConnected(false);
        return;
      }

      setDesktopConnected(true);

      const reader = resp.body!.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        // Process complete SSE events
        const events = buffer.split('\n\n');
        buffer = events.pop() || '';

        for (const evt of events) {
          if (!evt.trim()) continue;

          let eventType = '';
          let eventData = '';
          for (const line of evt.split('\n')) {
            if (line.startsWith('event: ')) eventType = line.slice(7);
            else if (line.startsWith('data: ')) eventData = line.slice(6);
          }
          if (!eventData) continue;

          if (eventType === 'screeninfo') {
            try {
              const info = JSON.parse(atob(eventData));
              setScreenWidth(info.width);
              setScreenHeight(info.height);
              screenWidthRef.current = info.width;
              screenHeightRef.current = info.height;
            } catch { /* ignore */ }
          } else if (eventType === 'log') {
            try {
              const entry: DesktopLogEntry = JSON.parse(atob(eventData));
              setDesktopLogs(prev => {
                const next = [...prev, entry];
                return next.length > MAX_DESKTOP_LOGS ? next.slice(-MAX_DESKTOP_LOGS) : next;
              });
            } catch (e) { console.warn('failed to parse log event:', e); }
          } else if (eventType === 'inputack') {
            try {
              const ack = JSON.parse(atob(eventData));
              const label = formatInputAckLabel(ack);
              if (label) {  // skip empty labels (e.g. mouse_move) to avoid unnecessary re-renders
                setDesktopTooltip(label);
                if (desktopTooltipTimerRef.current) clearTimeout(desktopTooltipTimerRef.current);
                desktopTooltipTimerRef.current = setTimeout(() => setDesktopTooltip(''), 1500);
              }
            } catch { /* ignore */ }
          } else {
            // Screenshot frame: update image src
            if (desktopImgRef.current) {
              desktopImgRef.current.src = `data:image/jpeg;base64,${eventData}`;
            }
          }
        }
      }
    } catch (e) {
      if (!abort.signal.aborted) {
        setError(`Remote desktop connection error: ${e}`);
      }
    } finally {
      resetDesktopState();
      if (desktopAbortRef.current === abort) desktopAbortRef.current = null;
    }
  }, [sessionToken, resetDesktopState]);

  // Desktop mouse handler: translate browser coords to agent screen coords.
  // Uses refs for screen dimensions to avoid stale-closure issues when the
  // screeninfo SSE event arrives after the handler was created.
  const handleDesktopMouse = useCallback((e: React.MouseEvent<HTMLDivElement>, sid: string, signal: AbortSignal) => {
    // Intercept all mouse events when command palette is open.
    if (paletteOpenRef.current) return;
    const container = desktopContainerRef.current;
    const img = desktopImgRef.current;
    const sw = screenWidthRef.current;
    const sh = screenHeightRef.current;
    if (!container || !img || !sw || !sh) return;

    const rect = img.getBoundingClientRect();
    const scaleX = sw / rect.width;
    const scaleY = sh / rect.height;
    const x = Math.max(0, Math.min(sw - 1, Math.round((e.clientX - rect.left) * scaleX)));
    const y = Math.max(0, Math.min(sh - 1, Math.round((e.clientY - rect.top) * scaleY)));

    if (e.type === 'click') {
      sendDesktopInput(sid, { type: 'mouse_click', x, y, button: 'left' }, signal);
    } else if (e.type === 'contextmenu') {
      e.preventDefault();
      sendDesktopInput(sid, { type: 'mouse_click', x, y, button: 'right' }, signal);
    } else if (e.type === 'wheel') {
      const dir = (e as unknown as WheelEvent).deltaY < 0 ? 'up' : 'down';
      sendDesktopInput(sid, { type: 'mouse_scroll', x, y, direction: dir, amount: 3 }, signal);
    } else if (e.type === 'mousemove') {
      // Throttle mouse_move to ~30 Hz (33 ms interval) to avoid flooding the agent.
      const now = Date.now();
      if (now - desktopMouseMoveRef.current < 33) return;
      desktopMouseMoveRef.current = now;
      sendDesktopInput(sid, { type: 'mouse_move', x, y }, signal);
    }
  }, [sendDesktopInput]);

  // Desktop keyboard handler
  const handleDesktopKey = useCallback((e: KeyboardEvent, sid: string, signal: AbortSignal) => {
    // Intercept all keyboard events when command palette is open.
    if (paletteOpenRef.current) return;
    e.preventDefault();
    const modifiers: string[] = [];
    if (e.ctrlKey) modifiers.push('ctrl');
    if (e.altKey) modifiers.push('alt');
    if (e.shiftKey) modifiers.push('shift');
    if (e.metaKey) modifiers.push('cmd');

    let key = e.key.toLowerCase();
    // Map JS key names to robotgo key names
    const keyMap: Record<string, string> = {
      'enter': 'enter', 'tab': 'tab', 'escape': 'escape', 'backspace': 'backspace',
      'delete': 'delete', 'arrowup': 'up', 'arrowdown': 'down', 'arrowleft': 'left',
      'arrowright': 'right', 'home': 'home', 'end': 'end', 'pageup': 'pageup',
      'pagedown': 'pagedown', ' ': 'space',
    };

    if (e.type === 'keydown') {
      if (keyMap[key]) {
        sendDesktopInput(sid, { type: 'key_tap', key: keyMap[key], modifiers }, signal);
      } else if (key.length === 1 && modifiers.length === 0) {
        sendDesktopInput(sid, { type: 'type_text', text: e.key }, signal);
      } else if (key.startsWith('f') && !isNaN(parseInt(key.slice(1)))) {
        sendDesktopInput(sid, { type: 'key_tap', key, modifiers }, signal);
      } else if (key.length === 1 && modifiers.length > 0) {
        sendDesktopInput(sid, { type: 'key_tap', key, modifiers }, signal);
      }
    }
  }, [sendDesktopInput]);

  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      desktopContainerRef.current?.requestFullscreen().catch(() => {});
    }
  }, []);

  // Palette action handler
  const handlePaletteAction = useCallback((action: string) => {
    setPaletteOpen(false);
    const sid = desktopSessionId;
    const abort = desktopAbortRef.current;
    if (!sid || !abort) return;
    switch (action) {
      case 'refresh-screenshot':
        sendDesktopInput(sid, { type: 'refresh_screenshot' }, abort.signal);
        break;
      case 'send-text':
        setTextEditorContent('');
        setTextEditorOpen(true);
        break;
      case 'toggle-fullscreen':
        toggleFullscreen();
        break;
      case 'disconnect':
        disconnectDesktop();
        break;
    }
  }, [desktopSessionId, sendDesktopInput, toggleFullscreen, disconnectDesktop]); // eslint-disable-line react-hooks/exhaustive-deps

  // Close text editor and restore focus to the desktop container.
  const closeTextEditor = useCallback(() => {
    setTextEditorOpen(false);
    desktopContainerRef.current?.focus();
  }, []);

  // Send multi-line text to remote desktop by splitting into type_text + enter events.
  // Lines longer than 256 UTF-8 bytes are chunked to fit ValidateText's limit.
  // Sends are sequential (awaited) to preserve ordering on the wire.
  const sendTextToDesktop = useCallback(async () => {
    const raw = textEditorContent;
    setTextEditorOpen(false);
    setTextEditorContent('');
    desktopContainerRef.current?.focus();
    const sid = desktopSessionId;
    const abort = desktopAbortRef.current;
    if (!sid || !abort || !raw) return;

    // Normalize line endings and cap total size
    const text = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
    const MAX_SEND_TEXT_LENGTH = 4096;
    if (text.length > MAX_SEND_TEXT_LENGTH) {
      console.warn(`sendTextToDesktop: text truncated from ${text.length} to ${MAX_SEND_TEXT_LENGTH} chars`);
    }
    const capped = text.slice(0, MAX_SEND_TEXT_LENGTH);

    const encoder = new TextEncoder();
    const lines = capped.split('\n');
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      if (line.length > 0) {
        // Chunk by UTF-8 byte length to fit ValidateText's 256-byte limit.
        // Fresh decoder per line to avoid cross-line state leakage.
        const decoder = new TextDecoder();
        const encoded = encoder.encode(line);
        for (let j = 0; j < encoded.length; j += 256) {
          const chunkBytes = encoded.slice(j, Math.min(j + 256, encoded.length));
          const chunk = decoder.decode(chunkBytes, { stream: true });
          if (chunk) {
            await sendDesktopInput(sid, { type: 'type_text', text: chunk }, abort.signal);
          }
        }
        // Flush any remaining bytes from an incomplete multi-byte
        // sequence at the end of the last chunk.
        const remaining = decoder.decode();
        if (remaining) {
          await sendDesktopInput(sid, { type: 'type_text', text: remaining }, abort.signal);
        }
      }
      // Send Enter between lines (not after the last line)
      if (i < lines.length - 1) {
        await sendDesktopInput(sid, { type: 'key_tap', key: 'enter' }, abort.signal);
      }
    }
  }, [textEditorContent, desktopSessionId, sendDesktopInput]);

  // Attach/detach keyboard listeners for desktop (includes command palette handling)
  useEffect(() => {
    if (!desktopConnected || !desktopSessionId) return;
    const abort = desktopAbortRef.current;
    if (!abort) return;
    const sid = desktopSessionId;

    const handler = (e: KeyboardEvent) => {
      // Skip if focus is on an input, textarea, or select element
      const target = e.target as HTMLElement;
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT') return;

      // Command palette: meta key toggle (Cmd on macOS, Ctrl on non-Mac)
      const isMac = navigator.platform.includes('Mac');
      const isMetaKey = e.key === 'Meta' || (e.key === 'Control' && !isMac);
      if (isMetaKey) {
        e.preventDefault();
        if (!metaDownRef.current) {
          metaDownRef.current = true;
          setPaletteOpen(prev => !prev);
        }
        return;
      }

      // When palette is open, handle shortcut keys
      if (paletteOpenRef.current) {
        e.preventDefault();
        if (e.key === 'Escape') {
          setPaletteOpen(false);
          return;
        }
        switch (e.key.toLowerCase()) {
          case 'r': handlePaletteAction('refresh-screenshot'); return;
          case 't': handlePaletteAction('send-text'); return;
          case 'f': handlePaletteAction('toggle-fullscreen'); return;
          case 'q': handlePaletteAction('disconnect'); return;
        }
        return; // swallow all other keys while palette is open
      }

      handleDesktopKey(e, sid, abort.signal);
    };

    const keyUpHandler = (e: KeyboardEvent) => {
      const isMac = navigator.platform.includes('Mac');
      if (e.key === 'Meta' || (e.key === 'Control' && !isMac)) {
        metaDownRef.current = false;
      }
    };

    window.addEventListener('keydown', handler);
    window.addEventListener('keyup', keyUpHandler);
    return () => {
      window.removeEventListener('keydown', handler);
      window.removeEventListener('keyup', keyUpHandler);
      metaDownRef.current = false;
    };
  }, [desktopConnected, desktopSessionId, handleDesktopKey, handlePaletteAction]);

  // Sync fullscreen state with the browser Fullscreen API.
  // Check which element is fullscreen to avoid coupling desktop and shell state.
  useEffect(() => {
    const handler = () => {
      const el = document.fullscreenElement;
      setIsFullscreen(el === desktopContainerRef.current);
      setIsShellFullscreen(el === shellContainerRef.current);
    };
    document.addEventListener('fullscreenchange', handler);
    return () => document.removeEventListener('fullscreenchange', handler);
  }, []);

  const toggleShellFullscreen = useCallback(() => {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      shellContainerRef.current?.requestFullscreen().catch(() => {});
    }
  }, []);

  // Re-fit xterm terminal when shell fullscreen toggles (container height changes).
  useEffect(() => {
    if (xtermRef.current && fitAddonRef.current) {
      const timer = setTimeout(() => fitAddonRef.current?.fit(), 50);
      return () => clearTimeout(timer);
    }
  }, [isShellFullscreen]);

  // Sync xterm theme when shell theme changes.
  useEffect(() => {
    if (xtermRef.current) {
      xtermRef.current.options.theme = SHELL_THEMES[shellThemeKey];
    }
    localStorage.setItem('shellTheme', shellThemeKey);
  }, [shellThemeKey]);

  const cycleShellTheme = useCallback(() => {
    setShellThemeKey((prev) => {
      const idx = SHELL_THEME_KEYS.indexOf(prev);
      return SHELL_THEME_KEYS[(idx + 1) % SHELL_THEME_KEYS.length];
    });
  }, []);

  // Shared shell panel UI — rendered in both admin and non-admin tab layouts
  // with different tabIndex values. The outer Box stays mounted (CSS-hidden)
  // so the xterm instance and SSE read loop survive tab switches.
  const renderShellPanel = (shellTabIndex: number) => (
    <Box sx={{ display: tabIndex === shellTabIndex ? 'block' : 'none' }}>
      {tabIndex === shellTabIndex && (
        <>
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
                    {connectedAgents.map((ca) => (
                      <MenuItem key={ca.agent_id} value={ca.agent_id}>{ca.agent_id}</MenuItem>
                    ))}
                  </Select>
                </FormControl>
                {shellConnected ? (
                  <Button variant="outlined" color="warning" onClick={disconnectShell} tabIndex={-1}>
                    Disconnect
                  </Button>
                ) : (
                  <Button
                    variant="contained"
                    startIcon={<TerminalIcon />}
                    onClick={() => connectShell(shellAgent)}
                    disabled={!shellAgent}
                    tabIndex={-1}
                  >
                    Connect
                  </Button>
                )}
                <Tooltip title={`Theme: ${SHELL_THEMES[shellThemeKey].label}`}>
                  <IconButton
                    size="small"
                    onClick={cycleShellTheme}
                    tabIndex={-1}
                  >
                    <PaletteIcon fontSize="small" />
                  </IconButton>
                </Tooltip>
                <Tooltip title={isShellFullscreen ? 'Exit fullscreen' : 'Fullscreen'}>
                  <IconButton
                    size="small"
                    onClick={toggleShellFullscreen}
                    tabIndex={-1}
                  >
                    {isShellFullscreen ? <FullscreenExitIcon fontSize="small" /> : <FullscreenIcon fontSize="small" />}
                  </IconButton>
                </Tooltip>
              </Box>
            }
          />
          {connectedAgents.length === 0 && (
            <Alert severity="info" sx={{ mb: 2 }}>No agents connected. Start an agent to use the cloud shell.</Alert>
          )}
        </>
      )}
      <Paper
        ref={shellContainerRef}
        elevation={0}
        sx={{
          bgcolor: SHELL_THEMES[shellThemeKey].background,
          border: 'none',
          p: 0.5,
          borderRadius: 2,
          minHeight: 400,
          '& .xterm': { p: 1 },
          '& .xterm-viewport': { borderRadius: 1, bgcolor: SHELL_THEMES[shellThemeKey].background },
          ...(isShellFullscreen && { height: '100vh', borderRadius: 0 }),
        }}
      >
        <div ref={termRef} style={{ height: isShellFullscreen ? 'calc(100vh - 16px)' : 450 }} />
      </Paper>
      {tabIndex === shellTabIndex && shellConnected && (
        <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: 'block' }}>
          Session: {shellSessionId} | Agent: {shellAgent}{shellReattached ? ' (reattached)' : ''}
        </Typography>
      )}
    </Box>
  );

  // Magnifier: track cursor position over the screenshot and update the
  // lens overlay via requestAnimationFrame (no React re-renders).
  const handleDesktopMouseMoveForMagnifier = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    if (!magnifierOn) return;
    const img = desktopImgRef.current;
    if (!img) return;
    // Only store cursor coords; rect and imgSrc are read fresh inside rAF.
    const rect = img.getBoundingClientRect();
    magnifierPosRef.current.x = e.clientX - rect.left;
    magnifierPosRef.current.y = e.clientY - rect.top;
    if (!magnifierRafRef.current) {
      magnifierRafRef.current = requestAnimationFrame(() => {
        magnifierRafRef.current = 0;
        try {
          const el = magnifierLensRef.current;
          if (!el) return;
          const currentImg = desktopImgRef.current;
          if (!currentImg) return;
          // Re-read rect inside rAF to avoid stale geometry after resize.
          const currentRect = currentImg.getBoundingClientRect();
          const pos = magnifierPosRef.current;
          // Account for image offset within the flex container (C1 fix).
          const container = desktopContainerRef.current;
          const containerRect = container?.getBoundingClientRect();
          const imgOffsetX = currentRect.left - (containerRect?.left ?? 0);
          const imgOffsetY = currentRect.top - (containerRect?.top ?? 0);
          el.style.left = `${pos.x + imgOffsetX}px`;
          el.style.top = `${pos.y + imgOffsetY}px`;
          // Dirty-check: only reassign backgroundImage when src actually changed.
          if (currentImg.src !== magnifierLastSrcRef.current) {
            el.style.backgroundImage = currentImg.src ? `url("${currentImg.src}")` : 'none';
            magnifierLastSrcRef.current = currentImg.src;
          }
          el.style.backgroundPosition = `${-pos.x * MAGNIFIER_ZOOM + MAGNIFIER_SIZE / 2}px ${-pos.y * MAGNIFIER_ZOOM + MAGNIFIER_SIZE / 2}px`;
          el.style.backgroundSize = `${currentRect.width * MAGNIFIER_ZOOM}px ${currentRect.height * MAGNIFIER_ZOOM}px`;
          el.style.display = (pos.x >= 0 && pos.x <= currentRect.width && pos.y >= 0 && pos.y <= currentRect.height) ? 'block' : 'none';
        } catch (err) {
          console.warn('[magnifier] rAF update failed:', err);
        }
      });
    }
  }, [magnifierOn]);

  // Clean up magnifier rAF on unmount.
  useEffect(() => {
    return () => {
      magnifierPosRef.current = { x: 0, y: 0 };
      if (magnifierRafRef.current) cancelAnimationFrame(magnifierRafRef.current);
    };
  }, []);

  // Poll per-agent metrics while desktop is connected.
  useEffect(() => {
    if (!desktopConnected || !desktopAgent || !sessionToken) return;
    const ac = new AbortController();
    const fetchDesktopMetrics = async () => {
      try {
        const res = await fetch('/console/api/v1/metrics/agents', {
          headers: authHeaders(),
          signal: ac.signal,
        });
        if (res.ok) {
          const all: AgentMetrics[] = await res.json();
          const match = all.find((am) => am.agent_id === desktopAgent);
          setDesktopMetrics(match ? match.snapshot : null);
        }
      } catch { /* ignore polling / abort errors */ }
    };
    fetchDesktopMetrics();
    const interval = setInterval(fetchDesktopMetrics, 10000);
    return () => { clearInterval(interval); ac.abort(); };
  }, [desktopConnected, desktopAgent, sessionToken]); // eslint-disable-line react-hooks/exhaustive-deps

  // Cleanup desktop connection on unmount
  useEffect(() => {
    return () => {
      if (desktopAbortRef.current) desktopAbortRef.current.abort();
    };
  }, []);

  // Auto-scroll desktop log to bottom on new entries.
  useEffect(() => {
    if (desktopLogRef.current) {
      requestAnimationFrame(() => {
        if (desktopLogRef.current) {
          desktopLogRef.current.scrollTop = desktopLogRef.current.scrollHeight;
        }
      });
    }
  }, [desktopLogs]);

  // Validate restored token on mount, or detect auth-disabled mode.
  // Always call /me: if auth is disabled (--disable-auth), the server injects
  // a synthetic admin session and returns 200 even without a token.
  useEffect(() => {
    const headers: HeadersInit = sessionToken
      ? { Authorization: `Bearer ${sessionToken}` }
      : {};
    fetch('/console/api/v1/me', { headers })
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
      fetchConnectedAgents();
      fetchMetricsOverview();
      fetchAgentMetrics();
      if (isAdmin) fetchUsers();
      const sessionInterval = setInterval(fetchSessions, 3000);
      const agentInterval = setInterval(fetchAgents, 10000);
      const connectedInterval = setInterval(fetchConnectedAgents, 3000);
      return () => { clearInterval(sessionInterval); clearInterval(agentInterval); clearInterval(connectedInterval); };
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

  // Shared Desktop panel JSX (used in both admin and non-admin tab sections).
  const desktopPanel = (
    <Box>
      <PageHeader
        title="Remote Desktop"
        actions={
          <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
            <FormControl size="small" sx={{ minWidth: 180 }}>
              <InputLabel>Agent</InputLabel>
              <Select
                value={desktopAgent}
                label="Agent"
                onChange={(e) => setDesktopAgent(e.target.value)}
                disabled={desktopConnected}
              >
                {connectedAgents.map((ca) => (
                  <MenuItem key={ca.agent_id} value={ca.agent_id}>{ca.agent_id}</MenuItem>
                ))}
              </Select>
            </FormControl>
            {desktopConnected ? (
              <Button variant="outlined" color="warning" onClick={disconnectDesktop}>
                Disconnect
              </Button>
            ) : (
              <Button
                variant="contained"
                startIcon={<DesktopWindowsIcon />}
                onClick={() => connectDesktop(desktopAgent)}
                disabled={!desktopAgent}
              >
                Connect
              </Button>
            )}
          </Box>
        }
      />
      {connectedAgents.length === 0 && (
        <Alert severity="info" sx={{ mb: 2 }}>No agents connected. Start an agent to use remote desktop.</Alert>
      )}
      <Paper
        ref={desktopContainerRef}
        sx={{
          bgcolor: '#1e1e2e',
          p: 0.5,
          borderRadius: 2,
          minHeight: 400,
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'center',
          cursor: desktopConnected ? 'crosshair' : 'default',
          overflow: 'hidden',
          position: 'relative',
        }}
        onClick={(e) => desktopConnected && desktopAbortRef.current && handleDesktopMouse(e, desktopSessionId, desktopAbortRef.current.signal)}
        onContextMenu={(e) => desktopConnected && desktopAbortRef.current && handleDesktopMouse(e, desktopSessionId, desktopAbortRef.current.signal)}
        onWheel={(e) => desktopConnected && desktopAbortRef.current && handleDesktopMouse(e as unknown as React.MouseEvent<HTMLDivElement>, desktopSessionId, desktopAbortRef.current.signal)}
        onMouseMove={(e) => {
          if (desktopConnected && desktopAbortRef.current) handleDesktopMouse(e, desktopSessionId, desktopAbortRef.current.signal);
          handleDesktopMouseMoveForMagnifier(e);
        }}
      >
        {desktopTooltip && (
          <Chip
            label={desktopTooltip}
            size="small"
            sx={{
              position: 'absolute',
              top: 8,
              right: 8,
              zIndex: 10,
              bgcolor: 'rgba(0, 0, 0, 0.7)',
              color: '#fff',
              fontFamily: 'monospace',
              fontSize: '0.75rem',
              animation: 'fadeIn 0.15s ease-in',
              '@keyframes fadeIn': {
                from: { opacity: 0 },
                to: { opacity: 1 },
              },
            }}
          />
        )}
        {desktopConnected && (
          <Box
            sx={{
              position: 'absolute',
              top: 8,
              right: desktopTooltip ? 140 : 8,
              zIndex: 11,
              display: 'flex',
              gap: 0.5,
            }}
          >
            <Tooltip title={magnifierOn ? 'Disable magnifier' : 'Magnifier'}>
              <IconButton
                size="small"
                onClick={(e) => {
                  e.stopPropagation();
                  setMagnifierOn(prev => {
                    if (prev) {
                      // Reset position and cancel pending rAF when toggling off.
                      magnifierPosRef.current = { x: 0, y: 0 };
                      magnifierLastSrcRef.current = '';
                      if (magnifierRafRef.current) {
                        cancelAnimationFrame(magnifierRafRef.current);
                        magnifierRafRef.current = 0;
                      }
                    }
                    return !prev;
                  });
                }}
                sx={{
                  bgcolor: magnifierOn ? 'rgba(0, 0, 0, 0.8)' : 'rgba(0, 0, 0, 0.5)',
                  color: magnifierOn ? 'primary.main' : '#fff',
                  '&:hover': { bgcolor: 'rgba(0, 0, 0, 0.7)' },
                }}
              >
                <ZoomInIcon fontSize="small" />
              </IconButton>
            </Tooltip>
            <Tooltip title={isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}>
              <IconButton
                size="small"
                onClick={(e) => { e.stopPropagation(); toggleFullscreen(); }}
                sx={{
                  bgcolor: 'rgba(0, 0, 0, 0.5)',
                  color: '#fff',
                  '&:hover': { bgcolor: 'rgba(0, 0, 0, 0.7)' },
                }}
              >
                {isFullscreen ? <FullscreenExitIcon fontSize="small" /> : <FullscreenIcon fontSize="small" />}
              </IconButton>
            </Tooltip>
          </Box>
        )}
        {!desktopConnected && !desktopAgent && (
          <Typography variant="body1" color="text.secondary">
            Select an agent and click Connect to start remote desktop
          </Typography>
        )}
        {/* Magnifier lens overlay */}
        {magnifierOn && desktopConnected && (
          <div
            ref={magnifierLensRef}
            style={{
              position: 'absolute',
              width: MAGNIFIER_SIZE,
              height: MAGNIFIER_SIZE,
              borderRadius: '50%',
              border: '2px solid rgba(255, 255, 255, 0.6)',
              outline: '1px solid rgba(0, 0, 0, 0.3)',
              boxShadow: '0 2px 12px rgba(0, 0, 0, 0.5)',
              pointerEvents: 'none',
              zIndex: 15,
              display: 'none',
              transform: 'translate(-50%, -50%)',
              backgroundRepeat: 'no-repeat',
              overflow: 'hidden',
              willChange: 'transform',
            }}
          />
        )}
        <img
          ref={desktopImgRef}
          alt="Remote Desktop"
          className="remote-desktop-img"
          style={{
            maxWidth: '100%',
            maxHeight: isFullscreen ? '100vh' : '80vh',
            display: desktopConnected ? 'block' : 'none',
            userSelect: 'none',
            pointerEvents: 'none',
          }}
          draggable={false}
        />
        <style>{`.remote-desktop-img { image-rendering: crisp-edges; image-rendering: -webkit-optimize-contrast; }`}</style>
        {/* Command palette overlay */}
        {paletteOpen && desktopConnected && (
          <Box
            sx={{
              position: 'absolute',
              inset: 0,
              zIndex: 20,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              bgcolor: 'rgba(0, 0, 0, 0.5)',
            }}
            onClick={() => setPaletteOpen(false)}
          >
            <Paper
              elevation={8}
              sx={{
                minWidth: 260,
                borderRadius: 2,
                overflow: 'hidden',
                bgcolor: 'background.paper',
              }}
              onClick={(e) => e.stopPropagation()}
            >
              <Box sx={{ px: 2, py: 1.25, borderBottom: 1, borderColor: 'divider' }}>
                <Typography variant="overline" color="text.secondary" sx={{ letterSpacing: 1.5 }}>
                  Command Palette
                </Typography>
              </Box>
              {[
                { id: 'refresh-screenshot', label: 'Refresh Screenshot', shortcut: 'R' },
                { id: 'send-text', label: 'Send Text', shortcut: 'T' },
                { id: 'toggle-fullscreen', label: 'Toggle Fullscreen', shortcut: 'F' },
                { id: 'disconnect', label: 'Disconnect', shortcut: 'Q' },
              ].map((item) => (
                <Box
                  key={item.id}
                  onClick={() => handlePaletteAction(item.id)}
                  sx={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    px: 2,
                    py: 1.25,
                    cursor: 'pointer',
                    '&:hover': { bgcolor: 'action.hover' },
                    transition: 'background-color 0.1s',
                  }}
                >
                  <Typography variant="body2">{item.label}</Typography>
                  <Chip
                    label={item.shortcut}
                    size="small"
                    sx={{
                      fontFamily: 'monospace',
                      fontWeight: 600,
                      minWidth: 28,
                      height: 22,
                      fontSize: '0.75rem',
                    }}
                  />
                </Box>
              ))}
              <Box sx={{ px: 2, py: 1, borderTop: 1, borderColor: 'divider' }}>
                <Typography variant="caption" color="text.secondary">
                  Press <strong>Esc</strong> to close • <strong>{navigator.platform.includes('Mac') ? '⌘' : 'Ctrl'}</strong> to toggle
                </Typography>
              </Box>
            </Paper>
          </Box>
        )}
        {/* Text editor overlay (Send Text command palette action) */}
        {textEditorOpen && desktopConnected && (
          <Box
            sx={{
              position: 'absolute',
              inset: 0,
              zIndex: 20,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              bgcolor: 'rgba(0, 0, 0, 0.5)',
            }}
            onClick={closeTextEditor}
          >
            <Paper
              elevation={8}
              sx={{
                width: 400,
                borderRadius: 2,
                overflow: 'hidden',
                bgcolor: 'background.paper',
              }}
              onClick={(e) => e.stopPropagation()}
            >
              <Box sx={{ px: 2, py: 1.25, borderBottom: 1, borderColor: 'divider' }}>
                <Typography variant="overline" color="text.secondary" sx={{ letterSpacing: 1.5 }}>
                  Send Text
                </Typography>
              </Box>
              <Box sx={{ p: 2 }}>
                <textarea
                  autoFocus
                  value={textEditorContent}
                  onChange={(e) => setTextEditorContent(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') {
                      e.preventDefault();
                      closeTextEditor();
                    } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault();
                      sendTextToDesktop();
                    }
                  }}
                  placeholder="Type text to send to the remote desktop..."
                  style={{
                    width: '100%',
                    minHeight: 150,
                    padding: '8px 12px',
                    fontFamily: 'monospace',
                    fontSize: '0.875rem',
                    lineHeight: 1.5,
                    border: '1px solid',
                    borderColor: theme.palette.divider,
                    borderRadius: 4,
                    resize: 'vertical',
                    outline: 'none',
                    boxSizing: 'border-box',
                    backgroundColor: theme.palette.background.default,
                    color: theme.palette.text.primary,
                  }}
                  onFocus={(e) => { e.target.style.borderColor = theme.palette.primary.main; }}
                  onBlur={(e) => { e.target.style.borderColor = theme.palette.divider; }}
                />
                <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
                  ⌘/Ctrl+Enter to send • Enter for new line • <strong>Esc</strong> to close
                </Typography>
              </Box>
              <Box sx={{ px: 2, py: 1.5, borderTop: 1, borderColor: 'divider', display: 'flex', justifyContent: 'flex-end', gap: 1 }}>
                <Button
                  size="small"
                  onClick={closeTextEditor}
                >
                  Cancel
                </Button>
                <Button
                  size="small"
                  variant="contained"
                  disabled={!textEditorContent.trim()}
                  onClick={sendTextToDesktop}
                >
                  Send
                </Button>
              </Box>
            </Paper>
          </Box>
        )}
      </Paper>
      {desktopConnected && (
        <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: 'block' }}>
          Session: {desktopSessionId} | Agent: {desktopAgent}
          {screenWidth > 0 && ` | Screen: ${screenWidth}x${screenHeight}`}
        </Typography>
      )}
      {desktopConnected && desktopMetrics && (
        <Box sx={{ display: 'flex', gap: 1.5, mt: 1.5, flexWrap: 'wrap' }}>
          {[
            { label: 'Upload', value: `${(desktopMetrics.throughput_up_p50_bps / 1024).toFixed(1)} KB/s` },
            { label: 'Download', value: `${(desktopMetrics.throughput_dn_p50_bps / 1024).toFixed(1)} KB/s` },
            { label: 'Latency p50', value: `${desktopMetrics.latency_p50_ms.toFixed(0)} ms` },
            { label: 'Latency p95', value: `${desktopMetrics.latency_p95_ms.toFixed(0)} ms` },
            { label: 'Error Rate', value: `${(desktopMetrics.error_rate * 100).toFixed(2)}%`, color: desktopMetrics.error_rate > 0.01 ? 'error.main' : undefined },
            { label: 'Active Conns', value: `${desktopMetrics.active_conns}` },
          ].map((m) => (
            <Card key={m.label} sx={{ minWidth: 110, flex: '1 1 110px' }}>
              <CardContent sx={{ py: 0.75, '&:last-child': { pb: 0.75 } }}>
                <Typography variant="caption" color="text.secondary">{m.label}</Typography>
                <Typography variant="body2" sx={{ fontWeight: 600, color: m.color ?? 'text.primary' }}>{m.value}</Typography>
              </CardContent>
            </Card>
          ))}
        </Box>
      )}
      {desktopLogs.length > 0 && (
        <Box sx={{ mt: 1.5 }}>
          <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 0.5 }}>Activity Log</Typography>
          <Paper
            ref={desktopLogRef}
            sx={{
              bgcolor: '#1e1e2e',
              p: 1,
              borderRadius: 1,
              maxHeight: 200,
              overflow: 'auto',
              fontFamily: '"JetBrains Mono", "Fira Code", Menlo, Monaco, monospace',
              fontSize: '0.75rem',
              lineHeight: 1.6,
            }}
          >
            {desktopLogs.map((entry, i) => {
              const sevColor = entry.sev === 'error' ? '#f44336' : entry.sev === 'warn' ? '#ffa726' : '#e0e0e0';
              const srcColor = entry.src === 'server' ? '#42a5f5' : '#66bb6a';
              const time = entry.ts ? new Date(entry.ts).toLocaleTimeString() : '';
              return (
                <Box key={`${entry.ts}-${i}`} sx={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  <span style={{ color: '#666' }}>{time}</span>
                  {' '}
                  <span style={{ color: sevColor, fontWeight: 600, textTransform: 'uppercase', minWidth: 40, display: 'inline-block' }}>{entry.sev}</span>
                  {' '}
                  <span style={{ color: srcColor }}>[{entry.src}]</span>
                  {' '}
                  <span style={{ color: '#cdd6f4' }}>{entry.msg}</span>
                </Box>
              );
            })}
          </Paper>
        </Box>
      )}
    </Box>
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
                    <Tab label="Desktop" icon={<DesktopWindowsIcon />} iconPosition="start" />
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

                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                      <Typography variant="h6">Per-Agent Metrics</Typography>
                      <Box sx={{ flexGrow: 1 }} />
                      <ToggleButtonGroup
                        value={metricsDuration}
                        exclusive
                        onChange={(_, v) => { if (v) setMetricsDuration(v); }}
                        size="small"
                      >
                        <ToggleButton value="1h">1h</ToggleButton>
                        <ToggleButton value="6h">6h</ToggleButton>
                        <ToggleButton value="12h">12h</ToggleButton>
                        <ToggleButton value="1d">1d</ToggleButton>
                        <ToggleButton value="7d">7d</ToggleButton>
                      </ToggleButtonGroup>
                      <Tooltip title="Refresh metrics">
                        <IconButton size="small" onClick={() => { fetchMetricsOverview(); fetchAgentMetrics(); if (selectedStatsAgent) fetchAgentSamples(selectedStatsAgent); }}>
                          <RefreshIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                    </Box>
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
                              <Typography variant="subtitle2" sx={{ mb: 1 }}>Throughput (last {metricsDuration})</Typography>
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

                {renderShellPanel(4)}
                {tabIndex === 5 && desktopPanel}
              </>
            ) : (
              <>
                <Paper sx={{ mb: 3 }}>
                  <Tabs value={tabIndex} onChange={(_, v) => setTabIndex(v)}>
                    <Tab label="Sessions" />
                    <Tab label="Agents" />
                    <Tab label="Statistics" />
                    <Tab label="Shell" icon={<TerminalIcon />} iconPosition="start" />
                    <Tab label="Desktop" icon={<DesktopWindowsIcon />} iconPosition="start" />
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

                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                      <Typography variant="h6">Per-Agent Metrics</Typography>
                      <Box sx={{ flexGrow: 1 }} />
                      <ToggleButtonGroup
                        value={metricsDuration}
                        exclusive
                        onChange={(_, v) => { if (v) setMetricsDuration(v); }}
                        size="small"
                      >
                        <ToggleButton value="1h">1h</ToggleButton>
                        <ToggleButton value="6h">6h</ToggleButton>
                        <ToggleButton value="12h">12h</ToggleButton>
                        <ToggleButton value="1d">1d</ToggleButton>
                        <ToggleButton value="7d">7d</ToggleButton>
                      </ToggleButtonGroup>
                      <Tooltip title="Refresh metrics">
                        <IconButton size="small" onClick={() => { fetchMetricsOverview(); fetchAgentMetrics(); if (selectedStatsAgent) fetchAgentSamples(selectedStatsAgent); }}>
                          <RefreshIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                    </Box>
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
                              <Typography variant="subtitle2" sx={{ mb: 1 }}>Throughput (last {metricsDuration})</Typography>
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

                {renderShellPanel(3)}
                {tabIndex === 4 && desktopPanel}
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
