import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  TextField,
  Typography,
} from '@mui/material';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';

// Generic module API workbench. Given a module and its agent, it fetches
// form-friendly operation models derived from the module's OpenAPI document
// (`GET /api/v1/modules/<agent>/<module>/operations`) and lets the operator
// compose and send requests without knowing the contract by heart.

interface APISchema {
  type?: string;
  format?: string;
  ref?: string;
  description?: string;
  nullable?: boolean;
  enum?: unknown[];
  default?: unknown;
  example?: unknown;
  properties?: APIProperty[];
  items?: APISchema;
  truncated?: boolean;
  recursive?: boolean;
}

interface APIProperty {
  name: string;
  required: boolean;
  schema?: APISchema;
}

interface APIParameter {
  name: string;
  in: string;
  description?: string;
  required: boolean;
  schema?: APISchema;
}

interface APIRequestBody {
  required: boolean;
  content_type?: string;
  schema?: APISchema;
}

interface APIOperation {
  method: string;
  path: string;
  operation_id?: string;
  summary?: string;
  description?: string;
  parameters: APIParameter[];
  request_body?: APIRequestBody;
}

interface OperationsResponse {
  module_id: string;
  revision?: string;
  title: string;
  operations: APIOperation[];
}

interface InvokeResult {
  status: number;
  statusText: string;
  headers: Record<string, string>;
  body: string;
  durationMs: number;
  truncated: boolean;
}

export interface WorkbenchModule {
  module_id: string;
  service_name?: string;
}

interface ModuleWorkbenchProps {
  apiBase: string;
  agentId: string;
  module: WorkbenchModule;
  authHeaders: () => Record<string, string>;
  checkAuth: (res: Response) => boolean;
}

const MAX_RENDER_BYTES = 200 * 1024;

const isJSONContentType = (ct: string) => ct === 'application/json' || ct.endsWith('+json');

// readCappedBody streams the response and stops once MAX_RENDER_BYTES is
// collected, so a multi-megabyte artifact never lands in tab memory whole.
// Chunks are decoded incrementally to keep split multi-byte characters intact.
async function readCappedBody(res: Response): Promise<{ text: string; truncated: boolean }> {
  if (!res.body) {
    // 204 No Content and HEAD responses expose no stream.
    return { text: '', truncated: false };
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let text = '';
  let totalBytes = 0;
  let truncated = false;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (!value || value.byteLength === 0) continue;
    totalBytes += value.byteLength;
    if (totalBytes > MAX_RENDER_BYTES) {
      const keep = value.byteLength - (totalBytes - MAX_RENDER_BYTES);
      text += decoder.decode(value.subarray(0, keep), { stream: true });
      truncated = true;
      // The remainder is only ever discarded, so stop pulling it.
      void reader.cancel().catch(() => undefined);
      break;
    }
    text += decoder.decode(value, { stream: true });
  }
  // Flush any multi-byte character left pending by the last chunk.
  text += decoder.decode();
  return { text, truncated };
}

const METHOD_TONE: Record<string, 'success' | 'primary' | 'warning' | 'error' | 'default'> = {
  GET: 'success',
  POST: 'primary',
  PUT: 'warning',
  PATCH: 'warning',
  DELETE: 'error',
};

const operationKey = (op: APIOperation) => `${op.method} ${op.path}`;

// sampleValue builds a request-body template from the schema. It prefers the
// spec's own example/default/enum so the generated JSON documents the contract.
function sampleValue(schema?: APISchema): unknown {
  if (!schema) return null;
  if (schema.default !== undefined) return schema.default;
  if (schema.example !== undefined) return schema.example;
  if (schema.enum && schema.enum.length > 0) return schema.enum[0];

  switch (schema.type) {
    case 'object': {
      if (!schema.properties || schema.properties.length === 0) return {};
      const out: Record<string, unknown> = {};
      for (const prop of schema.properties) out[prop.name] = sampleValue(prop.schema);
      return out;
    }
    case 'array':
      return schema.items ? [sampleValue(schema.items)] : [];
    case 'integer':
    case 'number':
      return 0;
    case 'boolean':
      return false;
    case 'string':
      if (schema.format === 'date-time') return new Date().toISOString();
      if (schema.format === 'date') return new Date().toISOString().slice(0, 10);
      return '';
    default:
      return null;
  }
}

// fieldDefault seeds a parameter input with the most useful value available.
function fieldDefault(schema?: APISchema): string {
  if (!schema) return '';
  if (schema.default !== undefined) return String(schema.default);
  if (schema.example !== undefined) return String(schema.example);
  if (schema.enum && schema.enum.length > 0) return String(schema.enum[0]);
  return '';
}

function schemaHint(schema?: APISchema, fallback?: string): string {
  if (!schema) return fallback || '';
  const parts: string[] = [];
  if (fallback) parts.push(fallback);
  if (schema.type) parts.push(schema.type);
  if (schema.format) parts.push(schema.format);
  if (schema.enum && schema.enum.length > 0) parts.push(`one of: ${schema.enum.map(String).join(', ')}`);
  return parts.join(' · ');
}

function parseHeaderLines(text: string): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const line of text.split('\n')) {
    const idx = line.indexOf(':');
    if (idx <= 0) continue;
    headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return headers;
}

export default function ModuleWorkbench({ apiBase, agentId, module, authHeaders, checkAuth }: ModuleWorkbenchProps) {
  const [operations, setOperations] = useState<APIOperation[]>([]);
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [selectedKey, setSelectedKey] = useState('');
  const [values, setValues] = useState<Record<string, string>>({});
  const [headersText, setHeadersText] = useState('');
  const [bodyText, setBodyText] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState('');
  const [result, setResult] = useState<InvokeResult | null>(null);

  // Keep the auth callbacks in refs: App recreates them on every render, and
  // depending on their identity would refetch the operation list — wiping the
  // operator's form — each time the parent re-rendered.
  const authRef = useRef(authHeaders);
  authRef.current = authHeaders;
  const checkAuthRef = useRef(checkAuth);
  checkAuthRef.current = checkAuth;
  const loadAbortRef = useRef<AbortController | null>(null);
  const sendAbortRef = useRef<AbortController | null>(null);
  // Which module the state below belongs to; a switch clears it before loading.
  const loadedModuleRef = useRef('');

  // Initialise the form for an operation: defaults for every parameter, a body
  // template from the schema, and a content-type header when a body exists.
  const seedForm = useCallback((op: APIOperation) => {
    const seeded: Record<string, string> = {};
    for (const param of op.parameters) {
      seeded[`${param.in}:${param.name}`] = fieldDefault(param.schema);
    }
    setValues(seeded);
    const body = op.request_body;
    if (body && body.content_type) {
      setHeadersText(`Content-Type: ${body.content_type}`);
      // A JSON body is templated from its schema; any other media type would
      // be misrepresented by a JSON skeleton, so the editor starts empty.
      setBodyText(isJSONContentType(body.content_type) && body.schema ? JSON.stringify(sampleValue(body.schema), null, 2) : '');
    } else {
      setHeadersText('');
      setBodyText('');
    }
    setResult(null);
    setSendError('');
  }, []);

  const loadOperations = useCallback(async () => {
    // The component stays mounted across module switches, so a slow request
    // for the previous module must not repaint the new one.
    loadAbortRef.current?.abort();
    const controller = new AbortController();
    loadAbortRef.current = controller;

    if (loadedModuleRef.current !== module.module_id) {
      // A different module: drop the previous list and form before the new
      // request lands, so a failed load cannot leave stale operations live.
      loadedModuleRef.current = module.module_id;
      setOperations([]);
      setTitle('');
      setSelectedKey('');
      setValues({});
      setHeadersText('');
      setBodyText('');
      setResult(null);
      setSendError('');
      // The switch aborted any in-flight response, so its finally clause no
      // longer owns the sending state.
      setSending(false);
    }

    setLoading(true);
    setLoadError('');
    try {
      const res = await fetch(
        `${apiBase}/api/v1/modules/${encodeURIComponent(agentId)}/${encodeURIComponent(module.module_id)}/operations`,
        { headers: authRef.current(), signal: controller.signal },
      );
      if (!checkAuthRef.current(res)) return;
      if (!res.ok) {
        setLoadError((await res.text()) || `Failed to load operations (${res.status})`);
        return;
      }
      const payload: OperationsResponse = await res.json();
      if (controller.signal.aborted) return;
      setTitle(payload.title || '');
      setOperations(payload.operations || []);
      if (payload.operations && payload.operations.length > 0) {
        setSelectedKey(operationKey(payload.operations[0]));
        seedForm(payload.operations[0]);
      } else {
        setSelectedKey('');
        setValues({});
        setHeadersText('');
        setBodyText('');
      }
    } catch (e) {
      if (controller.signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
      setLoadError(e instanceof Error ? e.message : 'Failed to load operations');
    } finally {
      // Only the current request may end the loading state; an aborted one
      // finishes while its successor is still in flight.
      if (loadAbortRef.current === controller) setLoading(false);
    }
  }, [apiBase, agentId, module.module_id, seedForm]);

  useEffect(() => {
    loadOperations();
    return () => {
      loadAbortRef.current?.abort();
      // Dropping the ref also stops the aborted request's finally clause from
      // touching state after unmount.
      loadAbortRef.current = null;
      // A slow response for the module being left must not paint the next one.
      sendAbortRef.current?.abort();
      sendAbortRef.current = null;
    };
  }, [loadOperations]);

  const selected = operations.find((op) => operationKey(op) === selectedKey) || null;

  const selectOperation = (op: APIOperation) => {
    setSelectedKey(operationKey(op));
    seedForm(op);
  };

  const pathParams = selected?.parameters.filter((p) => p.in === 'path') || [];
  const queryParams = selected?.parameters.filter((p) => p.in === 'query') || [];
  const headerParams = selected?.parameters.filter((p) => p.in === 'header') || [];

  // resolveRequest turns the form into the concrete URL and body the console
  // proxy will forward. Empty query values are omitted, which is what most
  // module handlers expect for optional filters.
  const resolveRequest = (): { url: string; body?: string; headers: Record<string, string> } | null => {
    if (!selected) return null;
    let path = selected.path;
    for (const param of pathParams) {
      const raw = values[`path:${param.name}`] ?? '';
      if (!raw) {
        setSendError(`Path parameter "${param.name}" is required`);
        return null;
      }
      path = path.replace(`{${param.name}}`, encodeURIComponent(raw));
    }

    const query = new URLSearchParams();
    for (const param of queryParams) {
      const raw = values[`query:${param.name}`] ?? '';
      if (raw !== '') query.set(param.name, raw);
    }
    const queryString = query.toString();
    const url = `${apiBase}/api/v1/modules/invoke/${encodeURIComponent(agentId)}/${encodeURIComponent(module.module_id)}${path}${queryString ? `?${queryString}` : ''}`;

    const headers: Record<string, string> = { ...authRef.current(), ...parseHeaderLines(headersText) };
    for (const param of headerParams) {
      const raw = values[`header:${param.name}`] ?? '';
      if (raw !== '') headers[param.name] = raw;
    }

    const methodAllowsBody = selected.method !== 'GET' && selected.method !== 'HEAD';
    const body = methodAllowsBody && bodyText.trim() ? bodyText : undefined;
    if (body) {
      const contentType = Object.keys(headers).find((k) => k.toLowerCase() === 'content-type');
      const contentTypeValue = contentType ? headers[contentType] : '';
      if (contentTypeValue.includes('json')) {
        try {
          JSON.parse(body);
        } catch {
          setSendError('Request body is not valid JSON');
          return null;
        }
      }
    }
    return { url, body, headers };
  };

  const send = async () => {
    if (!selected) return;
    setSendError('');
    const request = resolveRequest();
    if (!request) return;

    // One response at a time: a retry replaces the previous request instead of
    // letting both paint the panel.
    sendAbortRef.current?.abort();
    const controller = new AbortController();
    sendAbortRef.current = controller;

    setSending(true);
    setResult(null);
    const started = performance.now();
    try {
      const res = await fetch(request.url, {
        method: selected.method,
        headers: request.headers,
        body: request.body,
        signal: controller.signal,
      });
      if (!checkAuthRef.current(res)) return;
      const headers: Record<string, string> = {};
      res.headers.forEach((v, k) => { headers[k] = v; });
      const { text: rawBody, truncated } = await readCappedBody(res);
      if (controller.signal.aborted) return;
      setResult({
        status: res.status,
        statusText: res.statusText,
        headers,
        body: rawBody,
        durationMs: Math.round(performance.now() - started),
        truncated,
      });
    } catch (e) {
      if (controller.signal.aborted || (e instanceof DOMException && e.name === 'AbortError')) return;
      setSendError(e instanceof Error ? e.message : 'Request failed');
    } finally {
      // Only the current request owns the sending state.
      if (sendAbortRef.current === controller) setSending(false);
    }
  };

  const curlCommand = () => {
    if (!selected) return '';
    let path = selected.path;
    for (const param of pathParams) {
      path = path.replace(`{${param.name}}`, encodeURIComponent(values[`path:${param.name}`] ?? `<${param.name}>`));
    }
    const query = new URLSearchParams();
    for (const param of queryParams) {
      const raw = values[`query:${param.name}`] ?? '';
      if (raw !== '') query.set(param.name, raw);
    }
    const queryString = query.toString();
    const origin = typeof window !== 'undefined' ? window.location.origin : '';
    const lines = [
      `curl -X ${selected.method} '${origin}${apiBase}/api/v1/modules/invoke/${encodeURIComponent(agentId)}/${encodeURIComponent(module.module_id)}${path}${queryString ? `?${queryString}` : ''}'`,
      `  -H 'Authorization: Bearer $SSETUNNEL_TOKEN'`,
    ];
    for (const [key, value] of Object.entries(parseHeaderLines(headersText))) {
      lines.push(`  -H '${key}: ${value}'`);
    }
    for (const param of headerParams) {
      const raw = values[`header:${param.name}`] ?? '';
      if (raw !== '') lines.push(`  -H '${param.name}: ${raw}'`);
    }
    if (selected.method !== 'GET' && selected.method !== 'HEAD' && bodyText.trim()) {
      lines.push(`  --data '${bodyText.replace(/'/g, `'\\''`)}'`);
    }
    return lines.join(' \\\n');
  };

  const copyCurl = () => {
    if (navigator.clipboard) navigator.clipboard.writeText(curlCommand());
  };

  // Only a spec-declared JSON body gets templated and JSON-validated; a multipart
  // or binary body must never be dressed as a JSON document.
  const bodyIsJSON = () => {
    const body = selected?.request_body;
    return Boolean(body && isJSONContentType(body.content_type ?? '') && body.schema);
  };

  const formatBody = () => {
    if (!bodyIsJSON()) return;
    try {
      setBodyText(JSON.stringify(JSON.parse(bodyText), null, 2));
      setSendError('');
    } catch {
      setSendError('Request body is not valid JSON');
    }
  };

  const resetBody = () => {
    if (!selected?.request_body) return;
    const body = selected.request_body;
    setBodyText(bodyIsJSON() ? JSON.stringify(sampleValue(body.schema), null, 2) : '');
  };

  return (
    <Box>
      {loading && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 2 }}>
          <CircularProgress size={18} />
          <Typography variant="body2" color="text.secondary">Loading operations…</Typography>
        </Box>
      )}

      {loadError && (
        <Alert
          severity="error"
          action={<Button size="small" onClick={loadOperations}>Retry</Button>}
          sx={{ mb: 2 }}
        >
          {loadError}
        </Alert>
      )}

      {!loading && !loadError && operations.length === 0 && (
        <Alert severity="info">This module's OpenAPI document declares no operations.</Alert>
      )}

      {operations.length > 0 && (
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'flex-start' }}>
          {/* Operation list */}
          <Paper variant="outlined" sx={{ flex: '0 0 260px', p: 1, maxHeight: 560, overflow: 'auto' }}>
            <Typography variant="caption" color="text.secondary" sx={{ px: 1, display: 'block', mb: 0.5 }}>
              {title || 'Operations'} · {operations.length}
            </Typography>
            <Box role="listbox" aria-label={title ? `${title} operations` : 'Operations'}>
              {operations.map((op) => {
                const active = operationKey(op) === selectedKey;
                return (
                  <Box
                    key={operationKey(op)}
                    role="option"
                    tabIndex={0}
                    aria-selected={active}
                    onClick={() => selectOperation(op)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        selectOperation(op);
                      }
                    }}
                    sx={{
                      display: 'flex',
                      gap: 1,
                      alignItems: 'center',
                      px: 1,
                      py: 0.75,
                      borderRadius: 1,
                      cursor: 'pointer',
                      bgcolor: active ? 'action.selected' : 'transparent',
                      '&:hover, &:focus-visible': { bgcolor: 'action.hover' },
                      '&:focus-visible': { outline: '2px solid', outlineColor: 'primary.main' },
                    }}
                  >
                    <Chip
                      size="small"
                      label={op.method}
                      color={METHOD_TONE[op.method] || 'default'}
                      sx={{ fontFamily: 'monospace', fontSize: 11, height: 20 }}
                    />
                    <Box sx={{ minWidth: 0 }}>
                      <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: 12, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        {op.path}
                      </Typography>
                      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                        {op.summary || op.operation_id || ''}
                      </Typography>
                    </Box>
                  </Box>
                );
              })}
            </Box>
          </Paper>

          {/* Request builder */}
          {selected && (
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 0.5 }}>
                  <Chip size="small" label={selected.method} color={METHOD_TONE[selected.method] || 'default'} sx={{ fontFamily: 'monospace' }} />
                  <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>{selected.path}</Typography>
                </Box>
                {(selected.summary || selected.description) && (
                  <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 1.5 }}>
                    {selected.description || selected.summary}
                  </Typography>
                )}

                {pathParams.map((param) => (
                  <TextField
                    key={`path:${param.name}`}
                    size="small"
                    label={`${param.name} (path)`}
                    value={values[`path:${param.name}`] ?? ''}
                    onChange={(e) => setValues((prev) => ({ ...prev, [`path:${param.name}`]: e.target.value }))}
                    helperText={schemaHint(param.schema, param.description)}
                    required
                    fullWidth
                    sx={{ mb: 1.5 }}
                  />
                ))}

                {queryParams.length > 0 && (
                  <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'wrap', mb: 1.5 }}>
                    {queryParams.map((param) => (
                      <TextField
                        key={`query:${param.name}`}
                        size="small"
                        label={`${param.name} (query)`}
                        value={values[`query:${param.name}`] ?? ''}
                        onChange={(e) => setValues((prev) => ({ ...prev, [`query:${param.name}`]: e.target.value }))}
                        helperText={schemaHint(param.schema, param.description)}
                        sx={{ flex: '1 1 220px' }}
                      />
                    ))}
                  </Box>
                )}

                {headerParams.length > 0 && (
                  <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'wrap', mb: 1.5 }}>
                    {headerParams.map((param) => (
                      <TextField
                        key={`header:${param.name}`}
                        size="small"
                        label={`${param.name} (header)`}
                        value={values[`header:${param.name}`] ?? ''}
                        onChange={(e) => setValues((prev) => ({ ...prev, [`header:${param.name}`]: e.target.value }))}
                        helperText={schemaHint(param.schema, param.description)}
                        sx={{ flex: '1 1 220px' }}
                      />
                    ))}
                  </Box>
                )}

                <TextField
                  size="small"
                  multiline
                  minRows={2}
                  maxRows={5}
                  fullWidth
                  label="Additional headers (Key: Value per line)"
                  value={headersText}
                  onChange={(e) => setHeadersText(e.target.value)}
                  sx={{ mb: 1.5, '& textarea': { fontFamily: 'monospace', fontSize: 12 } }}
                />

                {selected.request_body && (
                  <>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 0.5 }}>
                      <Typography variant="subtitle2">
                        Request body{selected.request_body.required ? ' (required)' : ''}
                      </Typography>
                      {selected.request_body.content_type && (
                        <Chip size="small" variant="outlined" label={selected.request_body.content_type} />
                      )}
                      <Box sx={{ flex: 1 }} />
                      {bodyIsJSON() && (
                        <>
                          <Button size="small" onClick={formatBody}>Format</Button>
                          <Button size="small" onClick={resetBody}>Reset template</Button>
                        </>
                      )}
                    </Box>
                    {(selected.request_body.schema?.truncated || selected.request_body.schema?.recursive) && (
                      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>
                        Schema is nested or recursive; the template shows the first levels only.
                      </Typography>
                    )}
                    <TextField
                      multiline
                      minRows={6}
                      maxRows={18}
                      fullWidth
                      value={bodyText}
                      onChange={(e) => setBodyText(e.target.value)}
                      sx={{ mb: 1.5, '& textarea': { fontFamily: 'monospace', fontSize: 12 } }}
                    />
                  </>
                )}

                {sendError && <Alert severity="error" sx={{ mb: 1.5 }}>{sendError}</Alert>}

                <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
                  <Button
                    variant="contained"
                    size="small"
                    startIcon={sending ? <CircularProgress size={14} /> : <PlayArrowIcon />}
                    onClick={send}
                    disabled={sending}
                  >
                    {sending ? 'Sending…' : 'Send'}
                  </Button>
                  <Button size="small" startIcon={<ContentCopyIcon />} onClick={copyCurl}>
                    Copy as cURL
                  </Button>
                </Box>
              </Paper>

              {/* cURL preview */}
              <Paper variant="outlined" sx={{ p: 1.5, mb: 2 }}>
                <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>cURL</Typography>
                <pre style={{ fontSize: 11, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{curlCommand()}</pre>
              </Paper>

              {/* Response */}
              {result && (
                <Paper variant="outlined" sx={{ p: 1.5 }}>
                  <Box sx={{ display: 'flex', gap: 1, alignItems: 'center', mb: 1 }}>
                    <Chip
                      size="small"
                      label={`${result.status} ${result.statusText}`}
                      color={result.status < 300 ? 'success' : result.status < 500 ? 'warning' : 'error'}
                    />
                    <Typography variant="caption" color="text.secondary">{result.durationMs} ms</Typography>
                    {result.truncated && (
                      <Typography variant="caption" color="warning.main">
                        Showing the first {Math.round(MAX_RENDER_BYTES / 1024)} KiB
                      </Typography>
                    )}
                  </Box>

                  {Object.keys(result.headers).length > 0 && (
                    <Box sx={{ mb: 1 }}>
                      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>Response headers</Typography>
                      <Paper variant="outlined" sx={{ p: 1, maxHeight: 140, overflow: 'auto' }}>
                        <pre style={{ fontSize: 11, margin: 0 }}>
                          {Object.entries(result.headers).map(([k, v]) => `${k}: ${v}`).join('\n')}
                        </pre>
                      </Paper>
                    </Box>
                  )}

                  <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600 }}>Response body</Typography>
                  <Paper variant="outlined" sx={{ p: 1, maxHeight: 360, overflow: 'auto' }}>
                    <pre style={{ fontSize: 12, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                      {(() => {
                        try {
                          return JSON.stringify(JSON.parse(result.body), null, 2);
                        } catch {
                          return result.body || '(empty body)';
                        }
                      })()}
                    </pre>
                  </Paper>
                </Paper>
              )}
            </Box>
          )}
        </Box>
      )}
    </Box>
  );
}
