export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export async function api<T = unknown>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch(path, {
    method: init.method ?? "GET",
    credentials: "same-origin",
    headers: init.body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  let data: any = null;
  try {
    data = await res.json();
  } catch {
    /* empty body */
  }
  if (res.status === 401 && !path.startsWith("/api/auth/") && !path.startsWith("/api/me")) {
    window.location.href = "/login?next=" + encodeURIComponent(location.pathname + location.search);
    throw new ApiError("Please sign in", 401);
  }
  if (!res.ok) throw new ApiError(data?.error ?? `Request failed (${res.status})`, res.status);
  return data as T;
}

export interface User {
  id: string;
  email: string;
  name: string;
  is_admin: boolean;
  disabled: boolean;
  created_at: string;
  last_login_at: string;
}

export interface Me {
  user: User;
  mcp_url: string;
  public_mcp_url: string;
  allow_anonymous: boolean;
}

export interface Status {
  ok: boolean;
  error?: string;
  needs_auth: boolean;
  tools: number;
  prompts: number;
  resources: number;
  checked_at: string;
}

export interface Upstream {
  id: string;
  slug: string;
  name: string;
  url: string;
  transport: "http" | "sse";
  auth_type: "none" | "header" | "oauth";
  header_name: string;
  is_public: boolean;
  enabled: boolean;
  has_secret: boolean;
  oauth_connected: boolean;
  status: Status | null;
}

export interface Token {
  id: string;
  name: string;
  prefix: string;
  upstream_ids: string[] | null;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
}

export interface Grant {
  client_id: string;
  client_name: string;
  created_at: string;
}
