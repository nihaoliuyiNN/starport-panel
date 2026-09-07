// 面板 API 客户端：令牌注入、错误封套解析、401 统一登出。

const TOKEN_KEY = 'starport.token';

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? '';
}

export function setToken(t: string) {
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
  window.dispatchEvent(new Event('starport-auth'));
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public body?: unknown,
  ) {
    super(message);
  }
}

interface RequestOptions {
  method?: string;
  body?: unknown; // 对象 → JSON；string → 原样（配合 contentType）
  contentType?: string;
  signal?: AbortSignal;
  raw?: boolean; // 返回 Response 而不解析
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    if (typeof opts.body === 'string') {
      body = opts.body;
      headers['Content-Type'] = opts.contentType ?? 'text/plain';
    } else {
      body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }
  }
  const res = await fetch(`/api/v1${path}`, { method: opts.method ?? 'GET', headers, body, signal: opts.signal });
  if (opts.raw) return res as unknown as T;
  if (res.status === 204) return undefined as T;
  const ct = res.headers.get('Content-Type') ?? '';
  const payload: unknown = ct.includes('application/json') ? await res.json().catch(() => null) : await res.text();
  if (!res.ok) {
    const err = (payload as { error?: { code?: string; message?: string } } | null)?.error;
    if (res.status === 401) setToken('');
    throw new ApiError(res.status, err?.code ?? `HTTP_${res.status}`, err?.message ?? (typeof payload === 'string' ? payload : res.statusText), payload);
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body }),
  postYaml: <T>(path: string, yaml: string) => request<T>(path, { method: 'POST', body: yaml, contentType: 'application/yaml' }),
  del: <T = void>(path: string) => request<T>(path, { method: 'DELETE' }),
};

/** WebSocket 地址：同源，带 ?token=（浏览器 WS 无法自定义 Header）。 */
export function wsUrl(path: string, params: Record<string, string | number | undefined> = {}): string {
  const u = new URL(`/api/v1${path}`, window.location.href);
  u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') u.searchParams.set(k, String(v));
  const token = getToken();
  if (token) u.searchParams.set('token', token);
  return u.toString();
}

/** 带令牌的原始 URL（用于 fetch 流式日志）。 */
export function apiUrl(path: string, params: Record<string, string | number | boolean | undefined> = {}): string {
  const u = new URL(`/api/v1${path}`, window.location.href);
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') u.searchParams.set(k, String(v));
  return u.toString();
}

export function errMsg(e: unknown): string {
  if (e instanceof ApiError) return `${e.message}（${e.code}）`;
  if (e instanceof Error) return e.message;
  return String(e);
}
