import {
  AuthenticationError,
  type ConnectionConfig,
  InvalidArgumentError,
  NotFoundError,
  RateLimitError,
  SandboxError,
} from "e2b";

async function toError(res: Response): Promise<Error> {
  const text = await res.text();
  let message = text;
  try {
    const body = JSON.parse(text) as { message?: string };
    if (body.message) message = body.message;
  } catch {
    // not JSON; keep the raw text
  }
  const msg = `${res.status}: ${message}`;
  if (res.status === 400) return new InvalidArgumentError(msg);
  if (res.status === 401 || res.status === 403) return new AuthenticationError(msg);
  if (res.status === 404) return new NotFoundError(msg);
  if (res.status === 429) return new RateLimitError(msg);
  return new SandboxError(msg);
}

async function request<T>(
  config: ConnectionConfig,
  method: string,
  path: string,
  body?: unknown,
): Promise<T | undefined> {
  const headers: Record<string, string> = { ...config.headers };
  if (config.apiKey) headers["X-API-Key"] = config.apiKey;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(config.apiUrl.replace(/\/+$/, "") + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(config.requestTimeoutMs || 60_000),
  });
  if (!res.ok) throw await toError(res);
  if (res.status === 204) return undefined;
  const text = await res.text();
  return text ? (JSON.parse(text) as T) : undefined;
}

/** POST to `<apiUrl><path>` with the sandbox's API key and return the JSON body. */
export async function postJson<T>(
  config: ConnectionConfig,
  path: string,
  body?: unknown,
): Promise<T | undefined> {
  return request<T>(config, "POST", path, body);
}

/** PUT to `<apiUrl><path>` with the sandbox's API key and return the JSON body. */
export async function putJson<T>(
  config: ConnectionConfig,
  path: string,
  body?: unknown,
): Promise<T | undefined> {
  return request<T>(config, "PUT", path, body);
}

/** GET `<apiUrl><path>` with the sandbox's API key and return the JSON body. */
export async function getJson<T>(config: ConnectionConfig, path: string): Promise<T | undefined> {
  return request<T>(config, "GET", path);
}
