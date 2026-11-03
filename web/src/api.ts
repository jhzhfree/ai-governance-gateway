import type { components } from './generated/api';

export type ModelInfo = components['schemas']['ModelInfo'];
export type Organization = components['schemas']['Organization'];
export type Project = components['schemas']['Project'];
export type ProjectDetails = components['schemas']['ProjectDetails'];
export type ProjectKey = components['schemas']['Key'];
export type IssuedKey = components['schemas']['IssuedKey'];
export type RequestRecord = components['schemas']['RequestRecord'];
export type Usage = components['schemas']['Usage'];
export type Audit = components['schemas']['Audit'];
export type Page<T> = { data: T[]; next_cursor: string };
export type Session = components['schemas']['BrowserSession'];

export class ApiError extends Error {
  constructor(
    public status: number,
    public requestID: string,
  ) {
    super(
      status === 401
        ? '登录已过期，请重新登录。'
        : status === 403
          ? '当前身份没有执行此操作的权限。'
          : status === 404
            ? '资源不存在，或你没有访问权限。'
            : status === 400
              ? '请检查填写内容后重试。'
              : '服务暂时不可用，请稍后重试。',
    );
  }
}
export async function api<T>(
  path: string,
  options: { method?: string; body?: unknown; csrf?: string; signal?: AbortSignal } = {},
): Promise<T> {
  const response = await fetch(path, {
    method: options.method ?? 'GET',
    credentials: 'same-origin',
    cache: 'no-store',
    signal: options.signal,
    headers: {
      ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(options.csrf ? { 'X-CSRF-Token': options.csrf } : {}),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (!response.ok) {
    if (response.status === 401 && path.startsWith('/api/'))
      window.dispatchEvent(new Event('aigw:session-expired'));
    throw new ApiError(response.status, response.headers.get('X-Request-ID') ?? '');
  }
  return response.status === 204 ? (undefined as T) : ((await response.json()) as T);
}
