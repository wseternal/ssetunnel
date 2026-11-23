import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import ModuleWorkbench from './ModuleWorkbench';
import type { ModuleFetch, ModuleSummary } from './api';

// Module registry page shared by the console and `cli ui`: it lists the
// selected agent's modules, shows the detail of one module, and hands the
// operation workbench the module's OpenAPI-derived operations. Everything it
// knows about transport arrives through `moduleFetch`, so both surfaces render
// the same workflow instead of maintaining two divergent implementations.

interface ModulesPageProps {
  /** Agent whose modules are listed; an empty value renders the placeholder. */
  agentId: string;
  moduleFetch: ModuleFetch;
  /** Absolute base preceding `/v1/modules/invoke/...` in cURL snippets. */
  curlBase: string;
  /** Auth headers a human replaying the cURL snippet would need. */
  curlAuthHeaders: string[];
  /** Optional selector control (agent picker) rendered next to Refresh. */
  toolbar?: ReactNode;
  /** Optional note rendered above the registry (surface-specific hint). */
  note?: ReactNode;
  emptyHint?: ReactNode;
}

const healthTone = (health?: string): 'success' | 'warning' | 'error' | 'default' => {
  switch ((health || '').toLowerCase()) {
    case 'healthy':
    case 'ready':
      return 'success';
    case 'degraded':
      return 'warning';
    case 'unhealthy':
    case 'failed':
      return 'error';
    default:
      return 'default';
  }
};

export default function ModulesPage({ agentId, moduleFetch, curlBase, curlAuthHeaders, toolbar, note, emptyHint }: ModulesPageProps) {
  const [modules, setModules] = useState<ModuleSummary[]>([]);
  const [epoch, setEpoch] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selectedId, setSelectedId] = useState('');
  const [openAPI, setOpenAPI] = useState('');
  const [openAPILoading, setOpenAPILoading] = useState(false);
  const [openAPIError, setOpenAPIError] = useState('');
  const [showOpenAPI, setShowOpenAPI] = useState(false);

  const fetchRef = useRef(moduleFetch);
  fetchRef.current = moduleFetch;
  const listAbortRef = useRef<AbortController | null>(null);
  const openAPIAbortRef = useRef<AbortController | null>(null);

  const loadModules = useCallback(async () => {
    listAbortRef.current?.abort();
    const controller = new AbortController();
    listAbortRef.current = controller;

    if (!agentId) {
      setModules([]);
      setSelectedId('');
      setLoading(false);
      setError('');
      return;
    }

    setLoading(true);
    setError('');
    try {
      const res = await fetchRef.current(`/api/v1/modules?agent=${encodeURIComponent(agentId)}`, { signal: controller.signal });
      if (!res.ok) {
        setError((await res.text()) || `Failed to list modules (${res.status})`);
        return;
      }
      const list = (await res.json()) as ModuleSummary[];
      if (controller.signal.aborted) return;
      setModules(list || []);
      // The catalog epoch is a response header, not part of the list body.
      setEpoch(res.headers.get('X-Catalog-Epoch') || '');
      // Keep the selected module live across a refresh when it is still present;
      // otherwise fall back to the first entry (or nothing).
      setSelectedId((prev) => {
        if (prev && list.some((m) => m.module_id === prev)) return prev;
        return list.length > 0 ? list[0].module_id : '';
      });
    } catch (e) {
      if (controller.signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
      setError(e instanceof Error ? e.message : 'Failed to list modules');
    } finally {
      if (listAbortRef.current === controller) setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    // A different agent owns a different catalog: drop the selection first so
    // the detail panel cannot show the previous agent's module.
    setOpenAPI('');
    setOpenAPIError('');
    setShowOpenAPI(false);
    loadModules();
    return () => {
      listAbortRef.current?.abort();
      listAbortRef.current = null;
    };
  }, [loadModules]);

  const selected = modules.find((m) => m.module_id === selectedId) || null;

  // Load the raw OpenAPI document only when it is asked for; the workbench
  // obtains its operation models through its own endpoint.
  useEffect(() => {
    openAPIAbortRef.current?.abort();
    openAPIAbortRef.current = null;
    setOpenAPI('');
    setOpenAPIError('');
    if (!showOpenAPI || !selectedId) return;
    const controller = new AbortController();
    openAPIAbortRef.current = controller;
    setOpenAPILoading(true);
    (async () => {
      try {
        const res = await fetchRef.current(
          `/api/v1/modules/openapi/${encodeURIComponent(agentId)}/${encodeURIComponent(selectedId)}`,
          { signal: controller.signal },
        );
        if (!res.ok) {
          setOpenAPIError((await res.text()) || `Failed to load OpenAPI (${res.status})`);
          return;
        }
        const text = await res.text();
        if (!controller.signal.aborted) setOpenAPI(text);
      } catch (e) {
        if (controller.signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
        setOpenAPIError(e instanceof Error ? e.message : 'Failed to load OpenAPI');
      } finally {
        if (openAPIAbortRef.current === controller) setOpenAPILoading(false);
      }
    })();
    return () => {
      controller.abort();
    };
    // The fetcher is read through fetchRef: an adapter may rebuild it on every
    // render, and depending on its identity would abort and restart this load
    // endlessly while the document is open.
  }, [showOpenAPI, agentId, selectedId]);

  return (
    <Box>
      {note}

      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2, flexWrap: 'wrap' }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>Modules</Typography>
        {epoch && <Chip size="small" variant="outlined" label={`epoch ${epoch.slice(0, 12)}`} />}
        <Box sx={{ flex: 1 }} />
        {toolbar}
        <Button
          size="small"
          startIcon={<RefreshIcon />}
          onClick={loadModules}
          disabled={loading || !agentId}
        >
          Refresh
        </Button>
      </Box>

      {!agentId && (
        <Alert severity="info">{emptyHint || 'Select a connected agent to browse the modules it exposes.'}</Alert>
      )}

      {loading && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 2 }}>
          <CircularProgress size={18} />
          <Typography variant="body2" color="text.secondary">Loading modules…</Typography>
        </Box>
      )}

      {error && (
        <Alert severity="error" action={<Button size="small" onClick={loadModules}>Retry</Button>} sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {agentId && !loading && !error && modules.length === 0 && (
        <Alert severity="info">This agent exposes no modules.</Alert>
      )}

      {modules.length > 0 && (
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'flex-start', flexWrap: 'wrap' }}>
          <Paper variant="outlined" sx={{ flex: '0 0 240px', p: 1, maxHeight: 560, overflow: 'auto' }}>
            <Box role="listbox" aria-label="Modules">
              {modules.map((mod) => {
                const active = mod.module_id === selectedId;
                return (
                  <Box
                    key={mod.module_id}
                    role="option"
                    tabIndex={0}
                    aria-selected={active}
                    onClick={() => setSelectedId(mod.module_id)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        setSelectedId(mod.module_id);
                      }
                    }}
                    sx={{
                      px: 1,
                      py: 0.75,
                      borderRadius: 1,
                      cursor: 'pointer',
                      bgcolor: active ? 'action.selected' : 'transparent',
                      '&:hover, &:focus-visible': { bgcolor: 'action.hover' },
                      '&:focus-visible': { outline: '2px solid', outlineColor: 'primary.main' },
                    }}
                  >
                    <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: 12 }}>{mod.module_id}</Typography>
                    <Box sx={{ display: 'flex', gap: 0.5, alignItems: 'center', mt: 0.25 }}>
                      {mod.health && <Chip size="small" label={mod.health} color={healthTone(mod.health)} sx={{ height: 18, fontSize: 10 }} />}
                      {mod.has_ui && <Chip size="small" variant="outlined" label="UI" sx={{ height: 18, fontSize: 10 }} />}
                      {mod.revision && (
                        <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace' }}>
                          {mod.revision.slice(0, 8)}
                        </Typography>
                      )}
                    </Box>
                  </Box>
                );
              })}
            </Box>
          </Paper>

          {selected && (
            <Box sx={{ flex: 1, minWidth: 320 }}>
              <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                  <Typography variant="subtitle1" sx={{ fontFamily: 'monospace', fontWeight: 600 }}>
                    {selected.module_id}
                  </Typography>
                  {selected.health && <Chip size="small" label={selected.health} color={healthTone(selected.health)} />}
                  {selected.has_ui && <Chip size="small" variant="outlined" label="UI" />}
                  <Box sx={{ flex: 1 }} />
                  <Button size="small" onClick={() => setShowOpenAPI((v) => !v)}>
                    {showOpenAPI ? 'Hide OpenAPI' : 'View OpenAPI'}
                  </Button>
                </Box>
                <Box sx={{ display: 'flex', gap: 2, flexWrap: 'wrap', mt: 1 }}>
                  {selected.service_name && (
                    <Typography variant="caption" color="text.secondary">service: {selected.service_name}</Typography>
                  )}
                  {selected.service_version && (
                    <Typography variant="caption" color="text.secondary">version: {selected.service_version}</Typography>
                  )}
                  {selected.revision && (
                    <Typography variant="caption" color="text.secondary" sx={{ fontFamily: 'monospace' }}>
                      revision: {selected.revision}
                    </Typography>
                  )}
                </Box>
              </Paper>

              {showOpenAPI && (
                <Paper variant="outlined" sx={{ p: 1.5, mb: 2 }}>
                  {openAPILoading && (
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      <CircularProgress size={16} />
                      <Typography variant="caption" color="text.secondary">Loading OpenAPI…</Typography>
                    </Box>
                  )}
                  {openAPIError && <Alert severity="error">{openAPIError}</Alert>}
                  {openAPI && (
                    <pre style={{ fontSize: 11, margin: 0, maxHeight: 320, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                      {openAPI}
                    </pre>
                  )}
                </Paper>
              )}

              <ModuleWorkbench
                agentId={agentId}
                module={selected}
                moduleFetch={moduleFetch}
                curlBase={curlBase}
                curlAuthHeaders={curlAuthHeaders}
              />
            </Box>
          )}
        </Box>
      )}
    </Box>
  );
}
