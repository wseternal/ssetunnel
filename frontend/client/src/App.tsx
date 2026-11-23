import { useState, useEffect, useCallback, type ReactNode } from 'react';
import {
  ThemeProvider, createTheme, CssBaseline, Box, AppBar, Toolbar, Typography,
  Button, Tab, Tabs, Alert, Snackbar, Dialog, DialogTitle, DialogContent, Chip,
} from '@mui/material';
import { httpBridge } from './bridge';
import { wailsBridge } from './wails';
import type { Bridge, SessionDTO } from './types';
import LoginForm from './LoginForm';
import AgentPage from './AgentPage';
import ConnectPage from './ConnectPage';
import ModulesPage from './ModulesPage';

// Detect Wails runtime and fall back to HTTP bridge.
// Wails injects window.runtime and generated bindings; use those when present.
function resolveBridge(): Bridge {
  if (wailsBridge && (window as any).runtime) {
    return wailsBridge;
  }
  return httpBridge;
}

const theme = createTheme({
  palette: { mode: 'light' },
  typography: { fontFamily: '"Inter", "Helvetica Neue", Arial, sans-serif' },
});

interface TabPanelProps {
  children: ReactNode;
  value: number;
  index: number;
}

function TabPanel({ children, value, index }: TabPanelProps) {
  // Keep both panels mounted: unmounting reset page state, so a tab
  // round-trip silently re-targeted Restart with default (empty) config.
  return (
    <Box sx={{ p: 3, display: value === index ? 'block' : 'none' }} hidden={value !== index}>
      {children}
    </Box>
  );
}

export default function App() {
  const bridge = resolveBridge();
  const [session, setSession] = useState<SessionDTO | null>(null);
  const [tab, setTab] = useState(0);
  const [error, setError] = useState('');
  const [loginError, setLoginError] = useState('');
  const [loginOpen, setLoginOpen] = useState(false);

  const refreshSession = useCallback(async () => {
    try {
      const s = await bridge.getSession();
      setSession(s);
    } catch {
      setSession(null);
    }
  }, [bridge]);

  useEffect(() => { refreshSession(); }, [refreshSession]);

  const handleLogin = async (server: string, tunnelServer: string, username: string, password: string, totpCode: string) => {
    try {
      const result = await bridge.login(server, tunnelServer, username, password, totpCode);
      await refreshSession();
      setLoginError('');
      setLoginOpen(false);
      if (result.warning) {
        setError(result.warning);
      }
    } catch (e: any) {
      setLoginError(e.message || 'Login failed');
    }
  };

  const handleLogout = async () => {
    try {
      await bridge.logout();
    } catch { /* ignore */ }
    setSession(null);
  };

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <Box sx={{ flexGrow: 1 }}>
        <AppBar position="static" elevation={0} sx={{ bgcolor: 'grey.50', borderBottom: 1, borderColor: 'divider' }}>
          <Toolbar>
            <Typography variant="h6" sx={{ flexGrow: 0, mr: 4, color: 'text.primary', fontWeight: 600 }}>ssetunnel</Typography>
            <Tabs value={tab} onChange={(_, v) => setTab(v)} sx={{ flexGrow: 1 }}>
              <Tab label="Agent" />
              <Tab label="Connect" />
              <Tab label="Modules" />
            </Tabs>
            {session ? (
              <>
                <Chip label={`${session.username}`} size="small" variant="outlined" sx={{ mr: 1 }} />
                <Button color="inherit" onClick={handleLogout} sx={{ color: 'text.secondary' }} size="small">Logout</Button>
              </>
            ) : (
              <Button variant="outlined" size="small" onClick={() => { setLoginError(''); setLoginOpen(true); }}>
                Login
              </Button>
            )}
          </Toolbar>
        </AppBar>

        <TabPanel value={tab} index={0}>
          <AgentPage bridge={bridge} />
        </TabPanel>
        <TabPanel value={tab} index={1}>
          <ConnectPage bridge={bridge} />
        </TabPanel>
        <TabPanel value={tab} index={2}>
          <ModulesPage bridge={bridge} />
        </TabPanel>
      </Box>

      <Dialog open={loginOpen} onClose={() => setLoginOpen(false)} maxWidth="xs" fullWidth>
        <DialogTitle>Login to Server</DialogTitle>
        <DialogContent>
          <LoginForm onLogin={handleLogin} bridge={bridge} error={loginError} />
        </DialogContent>
      </Dialog>

      <Snackbar open={!!error} autoHideDuration={6000} onClose={() => setError('')} anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}>
        <Alert severity="error" onClose={() => setError('')}>{error}</Alert>
      </Snackbar>
    </ThemeProvider>
  );
}
