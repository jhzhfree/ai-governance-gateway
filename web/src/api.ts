import type { components } from './generated/api';

export type ContentPolicy = components['schemas']['ContentPolicy'];
export type ContentMeta = components['schemas']['ContentMeta'];
export type ContentDetail = components['schemas']['ContentDetail'];
export type ContentAuditor = components['schemas']['ContentAuditor'];
export type ContentAccess = components['schemas']['ContentAccess'];
export type MemberRecord = components['schemas']['MemberRecord'];
export type BillingRecord = components['schemas']['BillingRecord'];
export type BillingPage = components['schemas']['BillingPage'];
export type ImportReceipt = components['schemas']['ImportReceipt'];
export type MemberLifecycleEvent = components['schemas']['MemberLifecycleEvent'];
export type OwnershipTransfer = NonNullable<components['schemas']['OwnershipTransfer']>;
export type AlertPolicy = components['schemas']['AlertPolicy'];
export type BudgetAlert = components['schemas']['BudgetAlert'];
export type CostReport = components['schemas']['CostReport'];
export type Runtime = components['schemas']['Runtime'];
export type ProjectLimits = components['schemas']['ProjectLimits'];
export type Inspection = components['schemas']['Inspection'];

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
            : status === 409
              ? '记录已变更或当前状态不允许此操作，请刷新核对。'
              : status === 429
                ? '请求过于频繁，请稍后重试。'
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

export type ProviderConfiguration = components['schemas']['ProviderConfiguration'];
export type ProviderConfigurationStatus = components['schemas']['ProviderConfigurationStatus'];
export type NewAPIChannel = components['schemas']['NewAPIChannel'];
export type NewAPIChannelPage = components['schemas']['NewAPIChannelPage'];
export type NewAPIChannelOperation = components['schemas']['NewAPIChannelOperation'];
export type NewAPIChannelOperations = components['schemas']['NewAPIChannelOperations'];

export type BudgetAccount = components['schemas']['BudgetAccount'];
export type BudgetChange = components['schemas']['BudgetChange'];
export type BudgetCycle = components['schemas']['BudgetCycle'];
export type KeywordPolicy = components['schemas']['KeywordPolicy'];
export type KeywordMatch = components['schemas']['KeywordMatch'];
export type KeywordRule = components['schemas']['KeywordRule'];

export type LocalUser = components['schemas']['LocalUser'];

export type LocalSecurityEvent = components['schemas']['LocalSecurityEvent'];

export type DepartmentConfiguration = components['schemas']['DepartmentConfiguration'];
export type DepartmentVersion = components['schemas']['DepartmentVersion'];

export type BudgetApproval = components['schemas']['BudgetApproval'];
export type BudgetApprovalPolicy = components['schemas']['BudgetApprovalPolicy'];
export type BudgetApprovalPolicyChange = components['schemas']['BudgetApprovalPolicyChange'];

export type Device = components['schemas']['Device'];
export type DevicePolicy = components['schemas']['DevicePolicy'];
export type DeviceSession = components['schemas']['DeviceSession'];
export type DeviceEvent = components['schemas']['DeviceEvent'];
export type DeviceRequest = components['schemas']['DeviceRequest'];

export type HostSnapshot = components['schemas']['HostSnapshot'];
export type HostNetwork = components['schemas']['HostNetwork'];
export type HostIPv4 = components['schemas']['HostIPv4'];
export type HostChangeResult = components['schemas']['HostChangeResult'];
export type SystemEvent = components['schemas']['SystemEvent'];
