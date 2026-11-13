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
  return value === index ? <Box sx={{ p: 3 }}>{children}</Box> : null;
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
      await bridge.login(server, tunnelServer, username, password, totpCode);
      await refreshSession();
      setLoginError('');
      setLoginOpen(false);
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
