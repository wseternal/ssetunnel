// Transport-neutral workflow contract for the ssetunnel client.
// Browser and Wails adapters implement this interface.
// Pages contain no transport-specific logic.

export interface SessionDTO {
  username: string;
  role: string;
  tunnel_url: string;
  expires_at: string;
}

export interface SavedSession {
  server_url: string;
  console_url: string;
  username?: string;
}

export interface AgentStatus {
  running: boolean;
  server_url: string;
  target: string;
  agent_id: string;
  batch_size: number;
  concurrency: number;
  compress: boolean;
}

export interface ConnectStatus {
  name: string;
  local_addr: string;
  server_url: string;
  agent_id: string;
  target: string;
  running: boolean;
}

export interface Bridge {
  login(server: string, tunnelServer: string, username: string, password: string, totpCode: string): Promise<SessionDTO>;
  checkTOTPRequired(server: string, username: string): Promise<boolean>;
  getSession(): Promise<SessionDTO | null>;
  listSessions(): Promise<SavedSession[]>;
  logout(): Promise<void>;

  getAgentStatus(): Promise<AgentStatus | null>;
  startAgent(cfg: AgentConfig): Promise<void>;
  stopAgent(): Promise<void>;
  restartAgent(cfg: AgentConfig): Promise<void>;

  getConnectInstances(): Promise<ConnectStatus[]>;
  startConnect(name: string, cfg: ConnectConfig): Promise<ConnectStatus>;
  stopConnect(name: string): Promise<void>;
  restartConnect(name: string, cfg: ConnectConfig): Promise<ConnectStatus>;
}

export interface AgentConfig {
  server_url: string;
  target: string;
  agent_id: string;
  batch_size: number;
  concurrency: number;
  compress: boolean;
  no_auto_tune: boolean;
  verbose: boolean;
}

export interface ConnectConfig {
  server_url: string;
  agent_id: string;
  target: string;
  local: string;
  batch_size: number;
}
