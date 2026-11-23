// Shared module surface contract for both browser frontends.
//
// The console reaches this shape through the tunnel
// (`<console base>/api/v1/modules/...`) and `cli ui` reaches it through its
// own authenticated loopback server, which proxies the same paths to the
// agent. The pages therefore address paths relative to a surface-provided
// fetcher and never learn which surface they run on.

export interface ModuleFetchInit {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
}

/** Fetches a path relative to the module surface (`/api/v1/modules/...`). */
export type ModuleFetch = (path: string, init?: ModuleFetchInit) => Promise<Response>;

/**
 * Header that turns a POST into the operation method it names. Browsers refuse
 * to send TRACE (and several other verbs) from fetch, so the workbench sends
 * TRACE operations as POST carrying this header; every surface resolves it
 * back before the module sees the request.
 */
export const METHOD_OVERRIDE_HEADER = 'X-SSET-Method-Override';

/** Methods a browser fetch may send directly. */
const BROWSER_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']);

/** resolveMethod maps a spec method onto what the browser may actually send. */
export function resolveMethod(method: string): { method: string; override: boolean } {
  const upper = method.toUpperCase();
  if (BROWSER_METHODS.has(upper)) return { method: upper, override: false };
  return { method: 'POST', override: true };
}

export interface ModuleSummary {
  module_id: string;
  service_name?: string;
  service_version?: string;
  revision?: string;
  health?: string;
  has_ui?: boolean;
}
