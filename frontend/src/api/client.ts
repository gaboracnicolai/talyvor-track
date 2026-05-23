// Thin fetch wrapper. Adds the Authorization header from localStorage
// (Phase 8 doesn't ship a login flow — Phase 9 will), throws a typed
// error on non-2xx, and parses JSON. Generic over the response shape
// so callers don't have to `as` cast.

const BASE = import.meta.env.VITE_API_URL ?? "";

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code?: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

interface RequestOptions extends Omit<RequestInit, "body"> {
  body?: unknown;
}

export async function apiRequest<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { body, headers, ...rest } = options;

  const token = localStorage.getItem("track_api_key") ?? "";
  const memberId = localStorage.getItem("track_member_id") ?? "";

  const init: RequestInit = {
    ...rest,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(memberId ? { "X-Member-Id": memberId } : {}),
      ...(headers ?? {}),
    },
  };
  if (body !== undefined) {
    init.body = typeof body === "string" ? body : JSON.stringify(body);
  }

  const res = await fetch(BASE + path, init);
  if (res.status === 401) {
    // No login flow yet — bounce a custom event the UI can listen to
    // and surface a "set your API key" panel. Phase 9 will wire a real
    // login screen.
    window.dispatchEvent(new CustomEvent("track:unauthorized"));
    throw new APIError("Unauthorized", 401);
  }
  if (!res.ok) {
    let msg = res.statusText;
    let code: string | undefined;
    try {
      const data = (await res.json()) as { error?: string; code?: string };
      msg = data.error ?? msg;
      code = data.code;
    } catch {
      // body wasn't JSON — fall back to status text
    }
    throw new APIError(msg, res.status, code);
  }
  // 204 / empty body — return undefined cast to T. Callers asking for
  // void responses (DELETE / POST-ack) shouldn't be reading the body.
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// Build a query string from an object, dropping nullish values.
// Useful for the dozen analytics endpoints that take optional filters.
export function qs(params: Record<string, string | number | undefined>): string {
  const usp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    usp.set(k, String(v));
  }
  const s = usp.toString();
  return s ? `?${s}` : "";
}
