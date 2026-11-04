import { useRef, useState, type FormEvent } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Dialog, Tag } from 'tdesign-react';
import {
  api,
  type Audit,
  type IssuedKey,
  type Organization,
  type Project,
  type ProjectDetails,
  type ProjectKey,
  type Session,
  type Usage,
  type ModelInfo,
  type Page,
} from '../../api';
import { Mark, ErrorBox, Empty } from '../../shared/Feedback';
import { usePage } from '../../shared/useResourcePage';
import { roleNames, number } from '../../shared/format';
import { SecretDialog } from '../keys/SecretDialog';
import { KeyPanel } from '../keys/KeyPanel';
import { RequestPanel } from '../requests/RequestPanel';
import { ModelAccessPanel, currencyLabel } from '../models/ModelAccessPanel';
import { OperationsPanel } from '../operations/OperationsPanel';
import { ContentAuditPanel } from '../content/ContentAuditPanel';
import { LocalAccountsPanel } from '../local/LocalAccountsPanel';
import { BudgetPanel } from '../governance/BudgetPanel';
import { KeywordPanel } from '../governance/KeywordPanel';
import { ProviderPanel } from '../providers/ProviderPanel';
import { MemberPanel } from '../members/MemberPanel';
import { AuditPanel } from '../audit/AuditPanel';

export function Console({ session }: { session: Session }) {
  const returnFocus = useRef<HTMLElement | null>(null);
  const client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const identity = [session.user.issuer, session.user.subject];
  const catalog = useQuery({
    queryKey: [...identity, 'models'],
    queryFn: ({ signal }) => api<Page<ModelInfo>>('/api/v1/models', { signal }),
  });
  const currencies = [...new Set(catalog.data?.data.map((model) => model.currency) ?? [])];
  const organizations = usePage<Organization>(
    identity,
    'organizations',
    '/api/v1/organizations',
    true,
  );
  const orgs = organizations.data?.pages.flatMap((page) => page.data) ?? [];
  const orgID = params.get('org') ?? orgs[0]?.id ?? '';
  const organization = orgs.find((item) => item.id === orgID);
  const orgPath = `/api/v1/organizations/${encodeURIComponent(orgID)}`;
  const projects = usePage<Project>(identity, 'projects', `${orgPath}/projects`, Boolean(orgID));
  const projectList = projects.data?.pages.flatMap((page) => page.data) ?? [];
  const projectID = params.get('project') ?? projectList[0]?.id ?? '';
  const projectPath = `${orgPath}/projects/${encodeURIComponent(projectID)}`;
  const project = useQuery({
    queryKey: [...identity, 'project', orgID, projectID],
    enabled: Boolean(orgID && projectID),
    queryFn: ({ signal }) => api<ProjectDetails>(projectPath, { signal }),
  });
  const usage = useQuery({
    queryKey: [...identity, 'usage', orgID, projectID],
    enabled: Boolean(orgID && projectID),
    queryFn: ({ signal }) => api<Usage>(`${projectPath}/usage`, { signal }),
  });
  const keys = usePage<ProjectKey>(
    identity,
    'keys',
    `${projectPath}/keys`,
    Boolean(orgID && projectID),
  );
  const view =
    (
      [
        'local',
        'overview',
        'keys',
        'audit',
        'requests',
        'operations',
        'members',
        'content',
        'providers',
        'budget',
        'keywords',
      ] as const
    ).find((value) => value === params.get('view')) ?? 'overview';
  const viewLabel = {
    local: '本地账号',
    overview: '项目概览',
    keys: '访问密钥',
    audit: '审计记录',
    requests: '调用账本',
    operations: '流量与核查',
    members: '成员管理',
    content: '内容审计',
    providers: '供应商配置',
    budget: '预算管理',
    keywords: '关键字匹配',
  }[view];
  const audit = usePage<Audit>(
    identity,
    'audit',
    `${orgPath}/audit`,
    Boolean(orgID && view === 'audit'),
  );
  const [modal, setModal] = useState<'organization' | 'project' | 'key' | null>(null);
  const [createCurrency, setCreateCurrency] = useState('MOCK');
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [issued, setIssued] = useState<IssuedKey | null>(null);
  const [revoke, setRevoke] = useState<ProjectKey | null>(null);
  const canManage = organization?.role === 'owner' || organization?.role === 'admin';
  const canIssue = project.data && project.data.role !== 'viewer' && project.data.models.length > 0;
  const visibleKeys = keys.data?.pages.flatMap((page) => page.data) ?? [];
  const choose = (values: Record<string, string>) => {
    setParams(values);
    setNotice('');
  };
  const open = (value: typeof modal) => {
    returnFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setFormError(null);
    if (value === 'organization') setCreateCurrency(currencies[0] ?? 'MOCK');
    setModal(value);
  };
  const refresh = () => {
    void client.invalidateQueries({
      predicate: (query) => query.queryKey[0] === identity[0] && query.queryKey[1] === identity[1],
    });
  };
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    const name = String(data.get('name') ?? '').trim();
    const amount = Number(data.get('limit'));
    if (modal !== 'key' && (!Number.isSafeInteger(amount) || amount < 0)) {
      setFormError(new Error('预算必须是有效的非负整数。'));
      return;
    }
    setPending(true);
    setFormError(null);
    try {
      if (modal === 'organization') {
        const item = await api<Organization>('/api/v1/organizations', {
          method: 'POST',
          csrf: session.csrf_token,
          body: {
            name,
            period: data.get('period'),
            currency: data.get('currency'),
            limit_amount: amount,
          },
        });
        choose({ org: item.id });
        setNotice('组织已创建，可以继续添加项目。');
      } else if (modal === 'project') {
        const item = await api<Project>(`${orgPath}/projects`, {
          method: 'POST',
          csrf: session.csrf_token,
          body: { name, limit_amount: amount },
        });
        choose({ org: orgID, project: item.id });
        setNotice('项目已创建。创建访问密钥后即可接入。');
      } else {
        // Keep the one-time secret out of TanStack mutation/query caches and storage.
        const item = await api<IssuedKey>(`${projectPath}/keys`, {
          method: 'POST',
          csrf: session.csrf_token,
          body: {
            name,
            models: data.getAll('models').map(String),
            ttl_seconds: Number(data.get('days')) * 86400,
          },
        });
        setIssued(item);
      }
      setModal(null);
      refresh();
    } catch (error) {
      setFormError(error);
    } finally {
      setPending(false);
    }
  }
  async function revokeKey() {
    if (!revoke || pending) return;
    setPending(true);
    setFormError(null);
    try {
      await api(`${projectPath}/keys/${encodeURIComponent(revoke.id)}`, {
        method: 'DELETE',
        csrf: session.csrf_token,
      });
      setRevoke(null);
      setNotice('密钥已撤销，后续调用将被拒绝。');
      refresh();
    } catch (error) {
      setFormError(error);
    } finally {
      setPending(false);
    }
  }
  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST', csrf: session.csrf_token });
      client.clear();
      window.location.assign('/');
    } catch (error) {
      setFormError(error);
      setNotice('退出未完成，请重试。');
    }
  }
  const nav = (value: string) =>
    choose({
      ...(orgID ? { org: orgID } : {}),
      ...(projectID ? { project: projectID } : {}),
      view: value,
    });
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>
      <aside className="sidebar">
        <a className="brand" href="/">
          <Mark />
          <span>
            序衡<small>AI GOVERNANCE</small>
          </span>
        </a>
        <div className="sidebar-label">工作空间</div>
        <nav aria-label="主要导航">
          {[
            ['overview', '◫', '项目概览'],
            ['keys', '⌘', '访问密钥'],
            ['audit', '≡', '审计记录'],
            ['requests', '↗', '调用账本'],
            ['operations', '⚙', '流量与核查'],
            ['members', '♧', '成员管理'],
            ['content', '▤', '内容审计'],
            ['providers', '⇄', '供应商配置'],
            ['local', '♙', '本地账号'],
            ['budget', '▥', '预算管理'],
            ['keywords', '⌕', '关键字匹配'],
          ]
            .filter(
              ([id]) =>
                (id !== 'local' || session.auth_mode === 'local') &&
                (!['members', 'content', 'keywords'].includes(id) || canManage),
            )
            .map(([id, icon, label]) => (
              <button
                key={id}
                className={view === id ? 'nav-item active' : 'nav-item'}
                aria-current={view === id ? 'page' : undefined}
                onClick={() => nav(id)}
              >
                <span aria-hidden="true">{icon}</span>
                {label}
                {view === id && <i />}
              </button>
            ))}
        </nav>
        <div className="sidebar-bottom">
          <span className="status-dot" />{' '}
          {currencies.includes('CNY_MICRO')
            ? '中国大陆供应商配置'
            : currencies.includes('MOCK')
              ? '测试模型环境'
              : '等待供应商配置'}
          <small>权限与预算使用真实治理链路</small>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <span className="breadcrumb">
            控制台 <span>/</span> {viewLabel}
          </span>
          <div className="account">
            <span className="avatar" aria-hidden="true">
              {(session.display_name ?? session.user.subject).slice(0, 1).toUpperCase()}
            </span>
            <span className="account-name">{session.display_name ?? session.user.subject}</span>
            <Button
              variant="text"
              onClick={() => {
                void logout();
              }}
            >
              退出登录
            </Button>
          </div>
        </header>
        <main id="main" className="main-content">
          {catalog.error && (
            <ErrorBox
              error={catalog.error}
              retry={() => {
                void catalog.refetch();
              }}
            />
          )}
          <div className="page-heading">
            <div>
              <div className="eyebrow">WORKSPACE / GOVERNANCE</div>
              <h1>
                {viewLabel}
                <span className="heading-dot">.</span>
              </h1>
              <p>
                {view === 'audit'
                  ? '追踪组织配置与访问凭证的每一次变更。'
                  : '从访问权限到预算用量，掌握项目的模型调用边界。'}
              </p>
            </div>
            <Button
              theme="primary"
              variant="outline"
              disabled={!catalog.isSuccess || currencies.length === 0}
              onClick={() => open('organization')}
            >
              ＋ 新建组织
            </Button>
          </div>
          <section className="context-bar" aria-label="当前工作空间">
            <label>
              组织
              <select
                aria-label="组织"
                value={orgID}
                onChange={(e) => choose({ org: e.target.value })}
              >
                <option value="" disabled>
                  选择组织
                </option>
                {orgID && !organization && <option value={orgID}>正在读取所选组织</option>}
                {orgs.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.name}
                  </option>
                ))}
              </select>
            </label>
            <span className="context-divider" />
            <label>
              项目
              <select
                aria-label="项目"
                value={projectID}
                disabled={!orgID}
                onChange={(e) => choose({ org: orgID, project: e.target.value })}
              >
                <option value="" disabled>
                  选择项目
                </option>
                {projectID && !projectList.some((p) => p.id === projectID) && (
                  <option value={projectID}>正在读取所选项目</option>
                )}
                {projectList.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
            {organization && <Tag variant="light">{roleNames[organization.role]}</Tag>}
            <div className="context-actions">
              <Button size="small" variant="text" onClick={refresh}>
                刷新
              </Button>
              {organizations.hasNextPage && (
                <Button
                  size="small"
                  variant="text"
                  onClick={() => {
                    void organizations.fetchNextPage();
                  }}
                >
                  更多组织
                </Button>
              )}
              {projects.hasNextPage && (
                <Button
                  size="small"
                  variant="text"
                  onClick={() => {
                    void projects.fetchNextPage();
                  }}
                >
                  更多项目
                </Button>
              )}
              <Button disabled={!canManage} variant="text" onClick={() => open('project')}>
                ＋ 新建项目
              </Button>
            </div>
          </section>
          {notice && (
            <div className="notice" role="status">
              ✓ {notice}
            </div>
          )}
          {Boolean(formError) && !modal && !revoke && <ErrorBox error={formError} />}
          {organizations.error && (
            <ErrorBox
              error={organizations.error}
              retry={() => {
                void organizations.refetch();
              }}
            />
          )}
          {view === 'local' && session.auth_mode === 'local' ? (
            <LocalAccountsPanel session={session} />
          ) : view === 'providers' ? (
            <ProviderPanel identity={identity} csrf={session.csrf_token} />
          ) : organizations.isPending ? (
            <div className="loading" role="status">
              正在加载组织…
            </div>
          ) : orgs.length === 0 ? (
            <section className="panel">
              <Empty
                title="从第一个组织开始"
                description="创建组织，设置预算，再为团队的应用添加项目。"
              />
              <div className="empty-action">
                <Button
                  theme="primary"
                  disabled={!catalog.isSuccess || currencies.length === 0}
                  onClick={() => open('organization')}
                >
                  创建组织
                </Button>
              </div>
            </section>
          ) : (
            <>
              {projects.error && (
                <ErrorBox
                  error={projects.error}
                  retry={() => {
                    void projects.refetch();
                  }}
                />
              )}
              {!projectID &&
                !projects.isPending &&
                view !== 'audit' &&
                view !== 'members' &&
                view !== 'budget' && (
                  <section className="panel">
                    <Empty
                      title="还没有可访问的项目"
                      description={
                        canManage
                          ? '新建项目，配置预算并生成模型访问密钥。'
                          : '请联系组织管理员为你分配项目。'
                      }
                    />
                    {canManage && (
                      <div className="empty-action">
                        <Button theme="primary" onClick={() => open('project')}>
                          创建项目
                        </Button>
                      </div>
                    )}
                  </section>
                )}
              {project.error && view !== 'audit' && view !== 'members' && view !== 'budget' && (
                <ErrorBox error={project.error} />
              )}
              {view === 'budget' ? (
                <BudgetPanel
                  identity={identity}
                  orgPath={orgPath}
                  projectPath={projectID ? projectPath : undefined}
                  canManage={canManage}
                  csrf={session.csrf_token}
                />
              ) : view === 'keywords' && projectID ? (
                <KeywordPanel
                  identity={identity}
                  projectPath={projectPath}
                  csrf={session.csrf_token}
                />
              ) : view === 'content' && projectID ? (
                <>
                  <p className="content-audit-entry">
                    独立审计员访问地址：
                    <a
                      href={`/?audit_only=1&org=${encodeURIComponent(orgID)}&project=${encodeURIComponent(projectID)}`}
                    >
                      打开当前项目内容审计专用入口
                    </a>
                  </p>
                  <ContentAuditPanel
                    key={projectPath}
                    identity={identity}
                    projectPath={projectPath}
                    csrf={session.csrf_token}
                    canManage={canManage}
                    canGrant={organization?.role === 'owner'}
                  />
                </>
              ) : view === 'members' ? (
                <MemberPanel
                  key={orgPath + '/' + projectID}
                  identity={identity}
                  orgPath={orgPath}
                  projectPath={projectID ? projectPath : undefined}
                  canRead={canManage}
                  isOwner={organization?.role === 'owner'}
                  csrf={session.csrf_token}
                />
              ) : view === 'audit' ? (
                <AuditPanel
                  items={audit.data?.pages.flatMap((p) => p.data) ?? []}
                  error={audit.error}
                  loading={audit.isPending}
                  hasMore={audit.hasNextPage}
                  onMore={() => {
                    void audit.fetchNextPage();
                  }}
                />
              ) : (
                project.data && (
                  <>
                    {view === 'operations' && (
                      <OperationsPanel
                        currency={organization?.currency ?? usage.data?.currency ?? ''}
                        key={projectPath}
                        identity={identity}
                        projectPath={projectPath}
                        canManage={canManage}
                        csrf={session.csrf_token}
                      />
                    )}
                    {view === 'requests' && (
                      <RequestPanel
                        key={projectPath}
                        identity={identity}
                        projectPath={projectPath}
                      />
                    )}
                    {view === 'overview' && (
                      <>
                        <section className="project-title">
                          <div>
                            <h2>{project.data.name}</h2>
                            <span>项目用量与访问概况</span>
                          </div>
                          <Tag variant="light" theme="success">
                            {roleNames[project.data.role]}
                          </Tag>
                        </section>
                        {usage.error ? (
                          <ErrorBox
                            error={usage.error}
                            retry={() => {
                              void usage.refetch();
                            }}
                          />
                        ) : usage.data ? (
                          <>
                            <div className="metric-grid">
                              {[
                                ['项目预算', usage.data.limit_amount, '当前配置周期的费用上限'],
                                ['已使用', usage.data.spent, '已完成结算的模型调用'],
                                ['预留中', usage.data.held, '进行中或等待核对的调用'],
                                [
                                  '可用预算',
                                  usage.data.limit_amount - usage.data.spent - usage.data.held,
                                  '可继续用于新调用的额度',
                                ],
                              ].map(([label, value, hint], index) => (
                                <article
                                  className={`metric-card metric-${index}`}
                                  key={String(label)}
                                >
                                  <div className="metric-label">
                                    {label}
                                    <span aria-hidden="true">↗</span>
                                  </div>
                                  <div className="metric-value">
                                    {number(Number(value))}
                                    <small>单位</small>
                                  </div>
                                  <p>{hint}</p>
                                </article>
                              ))}
                            </div>
                            <section className="budget-strip">
                              <div>
                                <strong>预算使用情况</strong>
                                <span>
                                  {usage.data.period} · {currencyLabel(usage.data.currency)}
                                </span>
                              </div>
                              <progress
                                aria-label="预算占用比例"
                                value={usage.data.spent + usage.data.held}
                                max={usage.data.limit_amount || 1}
                              />
                              <span>
                                {usage.data.limit_amount
                                  ? Math.round(
                                      ((usage.data.spent + usage.data.held) /
                                        usage.data.limit_amount) *
                                        100,
                                    )
                                  : 0}
                                % 已使用或预留
                              </span>
                            </section>
                          </>
                        ) : (
                          <div className="loading" role="status">
                            正在加载用量…
                          </div>
                        )}
                        <ModelAccessPanel
                          key={projectPath}
                          project={project.data}
                          path={projectPath}
                          currency={organization?.currency ?? usage.data?.currency ?? ''}
                          models={catalog.data?.data ?? []}
                          error={catalog.error}
                          canManage={Boolean(canManage)}
                          csrf={session.csrf_token}
                          onSaved={refresh}
                        />
                      </>
                    )}
                    {(view === 'overview' || view === 'keys') && (
                      <KeyPanel
                        items={visibleKeys}
                        error={keys.error}
                        loading={keys.isPending}
                        hasMore={keys.hasNextPage}
                        canIssue={Boolean(canIssue)}
                        onCreate={() => open('key')}
                        onRetry={() => {
                          void keys.refetch();
                        }}
                        onMore={() => {
                          void keys.fetchNextPage();
                        }}
                        onRevoke={(key) => {
                          setFormError(null);
                          setRevoke(key);
                        }}
                      />
                    )}
                  </>
                )
              )}
            </>
          )}
          <footer className="workspace-footer">
            <span>序衡 AI 治理网关</span>
            <span>访问有边界 · 用量可追踪</span>
          </footer>
        </main>
      </div>
      <Dialog
        header={
          modal === 'organization' ? '新建组织' : modal === 'project' ? '新建项目' : '创建访问密钥'
        }
        visible={modal !== null}
        footer={false}
        destroyOnClose
        onClosed={() => {
          if (!modal && !issued) returnFocus.current?.focus();
        }}
        onClose={() => {
          if (!pending) setModal(null);
        }}
      >
        <form
          onSubmit={(e) => {
            void submit(e);
          }}
          className="dialog-form"
        >
          <p>
            {modal === 'key'
              ? `为「${project.data?.name ?? ''}」创建独立凭证。关闭后将无法再次查看完整密钥。`
              : '使用清晰的名称，方便团队识别和管理。'}
          </p>
          <label>
            {modal === 'organization' ? '组织名称' : modal === 'project' ? '项目名称' : '密钥名称'}
            <input autoFocus name="name" required maxLength={100} />
          </label>
          {modal !== 'key' ? (
            <>
              <label>
                预算上限
                <input
                  name="limit"
                  type="number"
                  min={0}
                  max={Number.MAX_SAFE_INTEGER}
                  step={1}
                  required
                  key={`${modal}-${createCurrency}`}
                  defaultValue={
                    (modal === 'organization' ? createCurrency : organization?.currency) ===
                    'CNY_MICRO'
                      ? 1000000
                      : 1000
                  }
                />
              </label>
              {modal === 'organization' && (
                <label>
                  预算单位
                  <select
                    name="currency"
                    aria-label="预算单位"
                    value={createCurrency}
                    onChange={(event) => setCreateCurrency(event.target.value)}
                  >
                    {currencies.map((currency) => (
                      <option key={currency} value={currency}>
                        {currencyLabel(currency)}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              {modal === 'organization' && (
                <label>
                  预算周期
                  <input
                    name="period"
                    type="month"
                    required
                    defaultValue={new Date().toISOString().slice(0, 7)}
                  />
                </label>
              )}
              <small>预算按所选最小计价单位填写，不会自动跨周期重置；创建后不能更换币种。</small>
            </>
          ) : (
            <>
              <label>
                有效期
                <select name="days" defaultValue="7">
                  <option value="1">1 天</option>
                  <option value="7">7 天</option>
                  <option value="30">30 天</option>
                  <option value="90">90 天</option>
                </select>
              </label>
              <fieldset className="model-selection">
                <legend>授权模型</legend>
                {project.data?.models.map((model) => (
                  <label key={model}>
                    <input type="checkbox" name="models" value={model} defaultChecked />
                    {model}
                  </label>
                ))}
              </fieldset>
            </>
          )}
          {Boolean(formError) && <ErrorBox error={formError} />}
          <div className="dialog-actions">
            <Button variant="outline" disabled={pending} onClick={() => setModal(null)}>
              取消
            </Button>
            <Button theme="primary" type="submit" loading={pending}>
              确认创建
            </Button>
          </div>
        </form>
      </Dialog>
      <Dialog
        header="撤销访问密钥"
        visible={revoke !== null}
        confirmBtn={{ content: '确认撤销', theme: 'danger', loading: pending }}
        cancelBtn="取消"
        onConfirm={() => {
          void revokeKey();
        }}
        onClose={() => {
          if (!pending) setRevoke(null);
        }}
      >
        <p>撤销「{revoke?.name}」后，使用它发起的新调用将被拒绝。此操作无法恢复。</p>
        {Boolean(formError) && <ErrorBox error={formError} />}
      </Dialog>
      {issued && (
        <SecretDialog
          issued={issued}
          close={() => {
            setIssued(null);
            requestAnimationFrame(() => returnFocus.current?.focus());
          }}
        />
      )}
    </div>
  );
}
