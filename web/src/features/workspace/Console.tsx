import { lazy, useRef, useState, type FormEvent } from 'react';
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
import { FeatureBoundary } from '../../shared/FeatureBoundary';
import { roleNames, number, budgetAmount } from '../../shared/format';
import { navigation, navigationItems, NavigationIcon } from './navigation';
import { SecretDialog } from '../keys/SecretDialog';
import { KeyPanel } from '../keys/KeyPanel';
import { ModelAccessPanel, currencyLabel } from '../models/ModelAccessPanel';
import type { BudgetApprovalPolicy } from '../../api';

const RequestPanel = lazy(() =>
  import('../requests/RequestPanel').then((module) => ({ default: module.RequestPanel })),
);
const CostReportPanel = lazy(() =>
  import('../operations/CostReportPanel').then((module) => ({ default: module.CostReportPanel })),
);
const OperationsPanel = lazy(() =>
  import('../operations/OperationsPanel').then((module) => ({ default: module.OperationsPanel })),
);
const ContentAuditPanel = lazy(() =>
  import('../content/ContentAuditPanel').then((module) => ({ default: module.ContentAuditPanel })),
);
const LocalAccountsPanel = lazy(() =>
  import('../local/LocalAccountsPanel').then((module) => ({ default: module.LocalAccountsPanel })),
);
const BudgetAlertsPanel = lazy(() =>
  import('../governance/BudgetAlertsPanel').then((module) => ({
    default: module.BudgetAlertsPanel,
  })),
);
const BudgetPanel = lazy(() =>
  import('../governance/BudgetPanel').then((module) => ({ default: module.BudgetPanel })),
);
const KeywordPanel = lazy(() =>
  import('../governance/KeywordPanel').then((module) => ({ default: module.KeywordPanel })),
);
const ProviderPanel = lazy(() =>
  import('../providers/ProviderPanel').then((module) => ({ default: module.ProviderPanel })),
);
const OwnershipPanel = lazy(() =>
  import('../members/OwnershipPanel').then((module) => ({ default: module.OwnershipPanel })),
);
const DevicePanel = lazy(() =>
  import('../devices/DevicePanel').then((module) => ({ default: module.DevicePanel })),
);
const DepartmentPanel = lazy(() =>
  import('../departments/DepartmentPanel').then((module) => ({ default: module.DepartmentPanel })),
);
const MemberPanel = lazy(() =>
  import('../members/MemberPanel').then((module) => ({ default: module.MemberPanel })),
);
const SystemPanel = lazy(() =>
  import('../system/SystemPanel').then((module) => ({ default: module.SystemPanel })),
);
const AuditPanel = lazy(() =>
  import('../audit/AuditPanel').then((module) => ({ default: module.AuditPanel })),
);

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
  const selectedView =
    navigationItems.find((item) => item.id === params.get('view')) ?? navigationItems[0];
  const view = selectedView.id;
  const viewLabel = selectedView.label;
  const [navigationOpen, setNavigationOpen] = useState(false);
  const navigationToggle = useRef<HTMLButtonElement>(null);
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
  const approvalPolicy = useQuery({
    queryKey: [...identity, 'budget-approval-policy', orgPath],
    queryFn: ({ signal }) =>
      api<BudgetApprovalPolicy>(`${orgPath}/budget/approval-policy`, { signal }),
    enabled: Boolean(orgID && canManage && modal === 'project'),
  });
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
  const nav = (value: string) => {
    choose({
      ...(orgID ? { org: orgID } : {}),
      ...(projectID ? { project: projectID } : {}),
      view: value,
    });
    setNavigationOpen(false);
    requestAnimationFrame(() => document.getElementById('main')?.focus());
  };
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
        <button
          ref={navigationToggle}
          className="navigation-toggle"
          aria-label="切换导航菜单"
          aria-expanded={navigationOpen}
          aria-controls="console-navigation"
          onClick={() => setNavigationOpen(!navigationOpen)}
        >
          <NavigationIcon
            path={navigationOpen ? 'M5 5l14 14 M5 19L19 5' : 'M4 6h16 M4 12h16 M4 18h16'}
          />
          {navigationOpen ? '收起' : '导航'}
        </button>
        <nav
          id="console-navigation"
          aria-label="主要导航"
          className={navigationOpen ? 'navigation-open' : undefined}
          onKeyDown={(event) => {
            if (event.key === 'Escape' && navigationOpen) {
              setNavigationOpen(false);
              navigationToggle.current?.focus();
            }
          }}
        >
          {navigation.map((group) => {
            const items = group.items.filter(
              ({ id }) =>
                (id !== 'local' || session.auth_mode === 'local') &&
                (!['members', 'departments', 'content', 'keywords'].includes(id) || canManage),
            );
            return (
              items.length > 0 && (
                <div className="navigation-group" key={group.label}>
                  <div className="sidebar-label">{group.label}</div>
                  {items.map(({ id, label, path }) => (
                    <button
                      key={id}
                      className={view === id ? 'nav-item active' : 'nav-item'}
                      aria-current={view === id ? 'page' : undefined}
                      onClick={() => nav(id)}
                    >
                      <NavigationIcon path={path} />
                      {label}
                    </button>
                  ))}
                </div>
              )
            );
          })}
        </nav>
        <div className="sidebar-bottom">
          <strong>模型目录</strong>
          <span>
            {catalog.isPending
              ? '读取中'
              : catalog.isError
                ? '暂不可用'
                : `${catalog.data?.data.length ?? 0} 个模型`}
          </span>
          <small>供应商与模型授权请在配置中核查</small>
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
        <main id="main" className="main-content" tabIndex={-1}>
          {view !== 'system' && catalog.error && (
            <ErrorBox
              error={catalog.error}
              retry={() => {
                void catalog.refetch();
              }}
            />
          )}
          <div className="page-heading">
            <div>
              <div className="eyebrow">企业 AI 管理控制台</div>
              <h1>
                {viewLabel}
                <span className="heading-dot">.</span>
              </h1>
              <p>{selectedView.description}</p>
            </div>
            {view !== 'system' && (
              <Button
                theme="primary"
                variant="outline"
                disabled={!catalog.isSuccess || currencies.length === 0}
                onClick={() => open('organization')}
              >
                ＋ 新建组织
              </Button>
            )}
          </div>
          {view !== 'system' && (
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
          )}
          {notice && (
            <div className="notice" role="status">
              ✓ {notice}
            </div>
          )}
          {Boolean(formError) && !modal && !revoke && <ErrorBox error={formError} />}
          {view !== 'system' && organizations.error && (
            <ErrorBox
              error={organizations.error}
              retry={() => {
                void organizations.refetch();
              }}
            />
          )}
          <FeatureBoundary key={view}>
            {view === 'system' ? (
              <SystemPanel identity={identity} csrf={session.csrf_token} />
            ) : view === 'local' && session.auth_mode === 'local' ? (
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
                  view !== 'departments' &&
                  view !== 'devices' &&
                  view !== 'budget' &&
                  view !== 'reports' &&
                  view !== 'alerts' &&
                  view !== 'ownership' && (
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
                {project.error &&
                  view !== 'audit' &&
                  view !== 'members' &&
                  view !== 'departments' &&
                  view !== 'devices' &&
                  view !== 'budget' &&
                  view !== 'reports' && <ErrorBox error={project.error} />}
                {view === 'devices' ? (
                  <DevicePanel
                    key={orgPath + '/' + projectID}
                    identity={identity}
                    orgPath={orgPath}
                    projectPath={projectID ? projectPath : undefined}
                    csrf={session.csrf_token}
                    canManage={canManage}
                  />
                ) : view === 'departments' ? (
                  <DepartmentPanel
                    key={orgPath}
                    identity={identity}
                    orgPath={orgPath}
                    csrf={session.csrf_token}
                    canManage={canManage}
                  />
                ) : view === 'ownership' ? (
                  <OwnershipPanel
                    key={orgPath}
                    identity={identity}
                    orgPath={orgPath}
                    isOwner={organization?.role === 'owner'}
                    session={session}
                  />
                ) : view === 'alerts' ? (
                  <BudgetAlertsPanel
                    identity={identity}
                    orgPath={orgPath}
                    projectPath={projectID ? projectPath : undefined}
                    canManage={canManage}
                    csrf={session.csrf_token}
                  />
                ) : view === 'reports' ? (
                  <CostReportPanel
                    key={projectPath}
                    identity={identity}
                    orgPath={orgPath}
                    projectPath={projectID ? projectPath : undefined}
                    canManage={canManage}
                    csrf={session.csrf_token}
                  />
                ) : view === 'budget' ? (
                  <BudgetPanel
                    isOwner={organization?.role === 'owner'}
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
                                    <div className="metric-label">{label}</div>
                                    <div
                                      className="metric-value"
                                      title={`${number(Number(value))} ${currencyLabel(usage.data!.currency)}`}
                                    >
                                      {budgetAmount(Number(value), usage.data!.currency)}
                                      <small>
                                        {usage.data!.currency === 'CNY_MICRO' ? '元' : '单位'}
                                      </small>
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
          </FeatureBoundary>
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
                  disabled={
                    modal === 'project' &&
                    (approvalPolicy.isPending || Boolean(approvalPolicy.error))
                  }
                  type="number"
                  min={0}
                  max={Number.MAX_SAFE_INTEGER}
                  step={1}
                  required
                  key={`${modal}-${createCurrency}-${Boolean(modal === 'project' && approvalPolicy.data?.enabled)}`}
                  defaultValue={
                    modal === 'project' && approvalPolicy.data?.enabled
                      ? 0
                      : (modal === 'organization' ? createCurrency : organization?.currency) ===
                          'CNY_MICRO'
                        ? 1000000
                        : 1000
                  }
                />
              </label>
              {modal === 'project' && approvalPolicy.data?.enabled && (
                <p>本组织启用双人审批：请以零上限创建项目，再到预算管理申请额度。</p>
              )}
              {modal === 'project' && approvalPolicy.error && (
                <ErrorBox
                  error={approvalPolicy.error}
                  retry={() => {
                    void approvalPolicy.refetch();
                  }}
                />
              )}
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
            <Button
              tag="button"
              theme="primary"
              type="submit"
              loading={pending}
              disabled={
                modal === 'project' && (approvalPolicy.isPending || Boolean(approvalPolicy.error))
              }
            >
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
