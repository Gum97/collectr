/** Typed access to the Go API.
 *
 * Cookies, not tokens: the session is an httpOnly cookie the browser attaches
 * itself, so nothing here touches localStorage and an XSS cannot read the
 * session out of the page. `credentials: same-origin` is the whole auth story.
 */

export interface ApiError {
  title: string
  status: number
  code: string
  fields?: Record<string, string>
  trace_id?: string
}

export class RequestFailed extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiError,
  ) {
    super(body.title || `HTTP ${status}`)
  }

  /** Field-level messages, for rendering next to the inputs that caused them
   *  rather than as one opaque banner. */
  get fields(): Record<string, string> {
    return this.body.fields ?? {}
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  if (res.status === 204) return undefined as T

  const text = await res.text()
  const parsed: unknown = text ? JSON.parse(text) : null

  if (!res.ok) {
    const envelope = (parsed ?? {}) as Partial<ApiError>
    throw new RequestFailed(res.status, {
      title: envelope.title ?? 'Không thực hiện được yêu cầu',
      status: res.status,
      code: envelope.code ?? 'unknown',
      fields: envelope.fields,
      trace_id: envelope.trace_id,
    })
  }
  return parsed as T
}

export const api = {
  get: <T,>(path: string) => request<T>('GET', path),
  post: <T,>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T,>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T,>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T,>(path: string) => request<T>('DELETE', path),
}

/** Envelope every list endpoint returns. */
export interface List<T> {
  data: T[]
}
