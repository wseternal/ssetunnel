// HTTP bridge for the browser client. Implements the shared Bridge contract
// by calling the local API served by `cli ui`.
// The launch secret is extracted from the URL fragment on first load and
// stored in sessionStorage. It is sent as an Authorization header on every
// request to the local API.
//
// NOTE: sessionStorage is tab-scoped — closing the tab loses the secret.
// The user must re-open the authenticated URL (from the terminal where
// `cli ui` is running) to start a new tab session.

import type {
  Bridge, SessionDTO, AgentStatus, AgentConfig,
  ConnectStatus, ConnectConfig
} from './types';

function getSecret(): string {
  const stored = sessionStorage.getItem('ssetunnel-secret');
  if (stored) return stored;

  const hash = window.location.hash.slice(1);
  if (hash) {
    sessionStorage.setItem('ssetunnel-secret', hash);
    // Remove the fragment from the URL without a reload.
    window.history.replaceState(null, '', window.location.pathname + window.location.search);
    return hash;
  }
  return '';
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const secret = getSecret();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init?.headers as Record<string, string> || {}),
  };
  if (secret) {
    headers['Authorization'] = `Bearer ${secret}`;
  }

  const resp = await fetch(`/api${path}`, { ...init, headers });
  if (!resp.ok) {
    const body = await resp.text().catch(() => '');
    throw new Error(`${resp.status}: ${body || resp.statusText}`);
  }
  if (resp.status === 204) return undefined as T;
  return resp.json();
}

export const httpBridge: Bridge = {
  async login(server, tunnelServer, username, password, totpCode): Promise<SessionDTO> {
    return apiFetch<SessionDTO>('/login', {
      method: 'POST',
      body: JSON.stringify({ server, tunnel_server: tunnelServer, username, password, totp_code: totpCode }),
    });
  },

  async checkTOTPRequired(server, username): Promise<boolean> {
    const result = await apiFetch<{ totp_required: boolean }>('/totp-check', {
      method: 'POST',
      body: JSON.stringify({ server, username }),
    });
    return result.totp_required;
  },

  async getSession(): Promise<SessionDTO | null> {
    try {
      return await apiFetch<SessionDTO>('/session');
    } catch {
      return null;
    }
  },

  async logout(): Promise<void> {
    await apiFetch<void>('/logout', { method: 'POST' });
  },

  async getAgentStatus(): Promise<AgentStatus | null> {
    try {
      return await apiFetch<AgentStatus>('/agent/status');
    } catch {
      return null;
    }
  },

  async startAgent(cfg: AgentConfig): Promise<void> {
    await apiFetch<void>('/agent/start', {
      method: 'POST',
      body: JSON.stringify(cfg),
    });
  },

  async stopAgent(): Promise<void> {
    await apiFetch<void>('/agent/stop', { method: 'POST' });
  },

  async restartAgent(cfg: AgentConfig): Promise<void> {
    await apiFetch<void>('/agent/restart', {
      method: 'POST',
      body: JSON.stringify(cfg),
    });
  },

  async getConnectInstances(): Promise<ConnectStatus[]> {
    return apiFetch<ConnectStatus[]>('/connect/list');
  },

  async startConnect(name: string, cfg: ConnectConfig): Promise<ConnectStatus> {
    return apiFetch<ConnectStatus>('/connect/start', {
      method: 'POST',
      body: JSON.stringify({ name, ...cfg }),
    });
  },

  async stopConnect(name: string): Promise<void> {
    await apiFetch<void>(`/connect/stop/${encodeURIComponent(name)}`, { method: 'POST' });
  },

  async restartConnect(name: string, cfg: ConnectConfig): Promise<ConnectStatus> {
    return apiFetch<ConnectStatus>(`/connect/restart/${encodeURIComponent(name)}`, {
      method: 'POST',
      body: JSON.stringify(cfg),
    });
  },
};
