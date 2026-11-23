// Console adapter for the shared module surface. The console reaches modules
// through its own authenticated API (`<base>/api/v1/modules/...`), which the
// console server proxies over the tunnel to the agent's module API.
//
// Everything the shared components need arrives through `moduleFetch`: the
// session bearer token is attached here, and a 401 is surfaced to the shell so
// an expired session still logs the operator out instead of rendering a
// "Unauthorized" error panel.

import { useMemo, type ReactNode } from 'react';
import ModulesPage from '../../shared/modules/ModulesPage';
import type { ModuleFetch } from '../../shared/modules/api';

const apiBase = import.meta.env.BASE_URL.replace(/\/$/, '');

interface ModulesPanelProps {
  agentId: string;
  token: string;
  onUnauthorized: () => void;
  /** Agent selector, rendered in the page toolbar. */
  toolbar?: ReactNode;
}

export default function ModulesPanel({ agentId, token, onUnauthorized, toolbar }: ModulesPanelProps) {
  const moduleFetch = useMemo<ModuleFetch>(() => async (path, init) => {
    const headers: Record<string, string> = {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers || {}),
    };
    const res = await fetch(`${apiBase}${path}`, {
      method: init?.method ?? 'GET',
      headers,
      body: init?.body,
      signal: init?.signal,
    });
    if (res.status === 401) onUnauthorized();
    return res;
  }, [token, onUnauthorized]);

  // The cURL snippet replays the console surface, so it carries the token as a
  // placeholder instead of the live session value.
  const curlBase = `${window.location.origin}${apiBase}/api`;
  const curlAuthHeaders = ['Authorization: Bearer $SSETUNNEL_TOKEN'];

  return (
    <ModulesPage
      agentId={agentId}
      moduleFetch={moduleFetch}
      curlBase={curlBase}
      curlAuthHeaders={curlAuthHeaders}
      toolbar={toolbar}
      emptyHint="Select a connected agent to browse the modules it exposes."
    />
  );
}
