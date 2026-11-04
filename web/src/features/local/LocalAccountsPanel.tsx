import { useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button, Dialog } from 'tdesign-react';
import { api, type Session, type LocalUser, type LocalSecurityEvent } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { date } from '../../shared/format';
import { LocalPasswordForm } from './LocalLogin';
export function LocalAccountsPanel({ session }: { session: Session }) {
  const client = useQueryClient();
  const identity = [session.user.issuer, session.user.subject];
  const users = usePage<LocalUser>(
    identity,
    'local-users',
    '/auth/local/users',
    Boolean(session.local_administrator),
  );
  const events = usePage<LocalSecurityEvent>(
    identity,
    'local-security-events',
    '/auth/local/events',
    Boolean(session.local_administrator),
  );
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');
  const [target, setTarget] = useState<LocalUser | null>(null);
  const [deactivating, setDeactivating] = useState<LocalUser | null>(null);
  const execute = async (path: string, method: string, body?: unknown) => {
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(path, { method, csrf: session.csrf_token, body });
      setNotice('本地账号设置已更新');
      setTarget(null);
      await client.invalidateQueries();
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  const create = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const values = new FormData(event.currentTarget);
    const password = String(values.get('password') ?? '');
    (event.currentTarget.elements.namedItem('password') as HTMLInputElement).value = '';
    void execute('/auth/local/users', 'POST', {
      username: values.get('username'),
      password,
      administrator: values.get('administrator') === 'on',
    });
  };
  const reset = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!target) return;
    const values = new FormData(event.currentTarget);
    const password = String(values.get('password') ?? '');
    event.currentTarget.reset();
    void execute(`/auth/local/users/${encodeURIComponent(target.id)}/password`, 'POST', {
      password,
      expected_revision: target.revision,
    });
  };
  return (
    <>
      <LocalPasswordForm />
      {session.local_administrator && (
        <section className="panel governance-panel">
          <div className="panel-heading">
            <div>
              <h2>本地账号管理</h2>
              <p>
                本地管理员可创建账号、重置密码、停用和撤销会话。此身份不自动赋予组织或供应商配置权限。
              </p>
            </div>
            <Button
              onClick={() => {
                void users.refetch();
              }}
            >
              刷新本地账号
            </Button>
          </div>
          {users.error && <ErrorBox error={users.error} />}{' '}
          {Boolean(error) && <ErrorBox error={error} />} {notice && <p role="status">{notice}</p>}
          <form onSubmit={create}>
            <h3>创建本地账号</h3>
            <label>
              用户名
              <input
                aria-label="新本地用户名"
                name="username"
                required
                pattern="[a-z][a-z0-9._-]{2,63}"
                maxLength={64}
              />
            </label>
            <label>
              初始密码
              <input
                aria-label="新本地账号初始密码"
                name="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={12}
                maxLength={256}
              />
            </label>
            <label>
              <input type="checkbox" name="administrator" aria-label="授予本地账号管理权" />
              授予本地账号管理权（可重置所有本地账号密码）
            </label>
            <Button theme="primary" type="submit" loading={pending}>
              创建本地账号
            </Button>
            <p>
              新账号首次登录必须改密，组织/项目权限另行授权。用户名为 3–64
              位小写字母、数字、点、下划线或短横线，首位为字母。
            </p>
          </form>
          <div className="table-scroll" role="region" aria-label="本地账号列表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>用户名 / 身份编号</th>
                  <th>状态</th>
                  <th>账号管理权</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {users.data?.pages
                  .flatMap((page) => page.data)
                  .map((user) => (
                    <tr key={user.id}>
                      <td>
                        {user.username}
                        <code>{user.id}</code>
                      </td>
                      <td>
                        {user.enabled ? '启用' : '停用'}
                        {user.must_change_password ? ' / 待改密' : ''}
                      </td>
                      <td>{user.administrator ? '本地管理员' : '普通账号'}</td>
                      <td>
                        <Button
                          size="small"
                          disabled={pending || user.id === session.user.subject}
                          onClick={() => {
                            if (user.enabled) {
                              setDeactivating(user);
                              return;
                            }
                            void execute(
                              `/auth/local/users/${encodeURIComponent(user.id)}`,
                              'PUT',
                              {
                                expected_revision: user.revision,
                                enabled: true,
                                administrator: user.administrator,
                              },
                            );
                          }}
                        >
                          {user.enabled ? '停用' : '启用'} {user.username}
                        </Button>
                        <Button
                          size="small"
                          variant="text"
                          disabled={pending || user.id === session.user.subject}
                          onClick={() => {
                            void execute(
                              `/auth/local/users/${encodeURIComponent(user.id)}`,
                              'PUT',
                              {
                                expected_revision: user.revision,
                                enabled: user.enabled,
                                administrator: !user.administrator,
                              },
                            );
                          }}
                        >
                          {user.administrator ? '取消管理权' : '授予管理权'} {user.username}
                        </Button>
                        <Button
                          size="small"
                          variant="text"
                          disabled={pending}
                          onClick={() => setTarget(user)}
                        >
                          重置密码 {user.username}
                        </Button>
                        <Button
                          size="small"
                          variant="text"
                          disabled={pending}
                          onClick={() => {
                            void execute(
                              `/auth/local/users/${encodeURIComponent(user.id)}/revoke`,
                              'POST',
                            );
                          }}
                        >
                          撤销会话 {user.username}
                        </Button>
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
          {users.hasNextPage && (
            <Button
              loading={users.isFetchingNextPage}
              onClick={() => {
                void users.fetchNextPage();
              }}
            >
              加载更多本地账号
            </Button>
          )}
          {target && (
            <form onSubmit={reset}>
              <h3>重置 {target.username} 的密码</h3>
              <p>保存后撤销该账号所有会话和短期令牌，下次登录必须再次修改密码。</p>
              <label>
                新临时密码
                <input
                  aria-label="重置后的本地临时密码"
                  name="password"
                  type="password"
                  required
                  autoComplete="new-password"
                  minLength={12}
                  maxLength={256}
                />
              </label>
              <Button theme="danger" type="submit" loading={pending}>
                确认重置本地密码
              </Button>
              <Button variant="text" onClick={() => setTarget(null)}>
                取消重置
              </Button>
            </form>
          )}
        </section>
      )}
      {session.local_administrator && (
        <section className="panel governance-panel">
          <div className="panel-heading">
            <div>
              <h2>本地账号安全事件</h2>
              <p>仅记录操作、主体和时间，不保存密码或令牌。</p>
            </div>
            <Button
              onClick={() => {
                void events.refetch();
              }}
            >
              刷新账号安全事件
            </Button>
          </div>
          {events.error && <ErrorBox error={events.error} />}
          <div className="table-scroll" role="region" aria-label="本地账号安全事件表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>操作</th>
                  <th>操作人</th>
                  <th>目标身份</th>
                  <th>时间</th>
                </tr>
              </thead>
              <tbody>
                {events.data?.pages
                  .flatMap((page) => page.data)
                  .map((event) => (
                    <tr key={event.id}>
                      <td>
                        {(
                          {
                            'local.user_created': '创建账号',
                            'local.browser_login': '浏览器登录',
                            'local.token_issued': '签发短期令牌',
                            'local.token_revoked': '撤销短期令牌',
                            'local.password_changed': '修改密码',
                            'local.password_reset': '重置密码',
                            'local.user_changed': '调整账号',
                            'local.sessions_revoked': '撤销全部会话',
                            'local.admin_recovered': '离线恢复管理员',
                          } as Record<string, string>
                        )[event.action] ?? event.action}
                      </td>
                      <td>{event.actor.subject}</td>
                      <td>{event.target_id}</td>
                      <td>{date(event.created_at)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
          {events.hasNextPage && (
            <Button
              loading={events.isFetchingNextPage}
              onClick={() => {
                void events.fetchNextPage();
              }}
            >
              加载更多账号安全事件
            </Button>
          )}
        </section>
      )}
      <Dialog
        visible={Boolean(deactivating)}
        header="停用本地账号"
        onClose={() => setDeactivating(null)}
        footer={
          <>
            <Button variant="outline" onClick={() => setDeactivating(null)}>
              取消停用
            </Button>
            <Button
              theme="danger"
              loading={pending}
              onClick={() => {
                if (!deactivating) return;
                const user = deactivating;
                setDeactivating(null);
                void execute(`/auth/local/users/${encodeURIComponent(user.id)}`, 'PUT', {
                  expected_revision: user.revision,
                  enabled: false,
                  administrator: user.administrator,
                });
              }}
            >
              确认停用账号及撤销密钥
            </Button>
          </>
        }
      >
        <p>
          停用 {deactivating?.username}{' '}
          将撤销其所有登录会话、短期令牌及该身份创建的项目密钥。重新启用账号不会恢复已撤销的密钥；在途调用可能仍产生费用。
        </p>
      </Dialog>
    </>
  );
}
