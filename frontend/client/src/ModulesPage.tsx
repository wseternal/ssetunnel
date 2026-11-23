// `cli ui` adapter for the shared module surface.
//
// Requests travel through the local server's authenticated proxy
// (`/api/v1/modules/...`), which forwards them to the loopback module API the
// running agent published; the browser never talks to the agent directly. The
// cURL snippet is the opposite on purpose: it points at the agent's own
// `module_api_url`, which is the endpoint a script on this machine can call
// without the launch secret.

import { useEffect, useMemo, useState } from 'react';
import { Alert, Box, Chip, Typography } from '@mui/material';
import ModulesSurface from '../../shared/modules/ModulesPage';
import type { ModuleFetch } from '../../shared/modules/api';
import type { AgentStatus, Bridge } from './types';

interface ModulesPageProps {
  bridge: Bridge;
}

export default function ModulesPage({ bridge }: ModulesPageProps) {
  const [status, setStatus] = useState<AgentStatus | null>(null);

  // The module endpoint appears only once the agent has started its module
  // subsystem, so the page keeps polling the agent status while it is open.
  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const s = await bridge.getAgentStatus();
        if (!cancelled) setStatus(s);
      } catch {
        if (!cancelled) setStatus(null);
      }
      if (!cancelled) setTimeout(poll, 5000);
    };
    poll();
    return () => { cancelled = true; };
  }, [bridge]);

  const moduleFetch = useMemo<ModuleFetch>(
    () => (path, init) => bridge.fetchModule(path, init),
    [bridge],
  );

  const running = status?.running ?? false;
  // An agent with no configured ID answers for the well-known "local" name.
  const agentId = running ? (status?.agent_id || 'local') : '';
  const moduleAPIBase = status?.module_api_url?.replace(/\/$/, '') || '';

  if (running && !moduleAPIBase) {
    // Modules are served by the agent process itself, so without its published
    // endpoint there is nothing to browse — say so instead of showing a
    // registry that can only fail through the proxy.
    return (
      <Alert severity="warning">
        This agent did not publish a module API endpoint (it may be a binary from before module support, or an
        externally started agent). Module operations are unavailable.
      </Alert>
    );
  }

  return (
    <Box>
      <ModulesSurface
        agentId={agentId}
        moduleFetch={moduleFetch}
        curlBase={`${moduleAPIBase}/api`}
        curlAuthHeaders={[]}
        toolbar={agentId ? <Chip size="small" variant="outlined" label={`agent ${agentId}`} /> : undefined}
        note={moduleAPIBase ? (
          <Alert severity="info" sx={{ mb: 2 }}>
            <Typography variant="body2" component="div">
              Agent module API: <code>{moduleAPIBase}</code>
            </Typography>
            <Typography variant="caption" color="text.secondary">
              Browser requests go through this page's authenticated loopback proxy; the cURL snippets call the
              agent endpoint directly, which needs no launch secret.
            </Typography>
          </Alert>
        ) : null}
        emptyHint="Start the agent to browse the modules it exposes."
      />
    </Box>
  );
}
