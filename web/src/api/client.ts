// Talks to the panel API of fdsync-server (same origin; `pnpm dev` proxies /api).

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string
  ) {
    super(message)
  }
}

let csrf = ''
export const setCsrf = (token: string) => {
  csrf = token
}

async function request(method: string, path: string, body?: unknown): Promise<Response> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = ((await res.json()) as { error?: string }).error ?? msg
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg)
  }
  return res
}

/** A GET that hands back the raw response (headers, 204), errors as ApiError. */
export function apiResponse(path: string): Promise<Response> {
  return request('GET', path)
}

export async function api<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await request(method, path, body)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

/** POST raw binary data (e.g. an image upload) with CSRF protection. */
export async function apiUpload<T>(path: string, blob: Blob): Promise<T> {
  const headers: Record<string, string> = {}
  if (blob.type) headers['Content-Type'] = blob.type
  if (csrf) headers['X-CSRF-Token'] = csrf
  const res = await fetch(path, {
    method: 'POST',
    headers,
    body: blob,
    credentials: 'same-origin',
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = ((await res.json()) as { error?: string }).error ?? msg
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg)
  }
  return (await res.json()) as T
}

/** Raw bytes of a file (preview/diff), with the file's full size. */
export async function apiBytes(path: string): Promise<{ bytes: Uint8Array; size: number }> {
  const res = await request('GET', path)
  const bytes = new Uint8Array(await res.arrayBuffer())
  const size = Number(res.headers.get('X-Fdsync-Size') ?? bytes.length)
  return { bytes, size }
}

export type Session = {
  authenticated: boolean
  setup: boolean // no password yet
  setupAllowed: boolean // this browser is on the Windows machine
  csrf?: string
}

export async function getSession(): Promise<Session> {
  const s = await api<Session>('GET', '/api/session')
  if (s.csrf) setCsrf(s.csrf)
  return s
}

export async function login(password: string) {
  const r = await api<{ csrf: string }>('POST', '/api/login', { password })
  setCsrf(r.csrf)
}

export async function setup(password: string) {
  const r = await api<{ csrf: string }>('POST', '/api/setup', { password })
  setCsrf(r.csrf)
}

export async function logout() {
  await api('POST', '/api/logout')
  setCsrf('')
}
