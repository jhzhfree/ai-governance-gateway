import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import {
  api,
  type DepartmentConfiguration,
  type DepartmentVersion,
  type MemberRecord,
  type Project,
} from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { roleNames } from '../../shared/format';

export function DepartmentPanel({
  identity,
  orgPath,
  csrf,
  canManage,
}: {
  identity: string[];
  orgPath: string;
  csrf: string;
  canManage: boolean;
}) {
  const client = useQueryClient();
  const configuration = useQuery({
    queryKey: [...identity, 'departments', orgPath],
    queryFn: ({ signal }) => api<DepartmentConfiguration>(`${orgPath}/departments`, { signal }),
    enabled: canManage,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const members = usePage<MemberRecord>(
    identity,
    'department-members',
    `${orgPath}/members`,
    canManage,
  );
  const projects = usePage<Project>(
    identity,
    'department-projects',
    `${orgPath}/projects`,
    canManage,
  );
  const history = usePage<DepartmentVersion>(
    identity,
    'department-history',
    `${orgPath}/departments/history`,
    canManage,
  );
  const [reason, setReason] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [editing, setEditing] = useState('');
  const [name, setName] = useState('');
  const [parent, setParent] = useState('');
  const value = configuration.data;
  const nodes = value?.departments ?? [];
  const people = members.data?.pages.flatMap((p) => p.data).filter((p) => p.enabled) ?? [];
  const projectOptions = projects.data?.pages.flatMap((p) => p.data) ?? [];
  function nodeName(id: string) {
    const path: string[] = [];
    let next = id;
    for (let i = 0; next && i < 8; i++) {
      const node = nodes.find((d) => d.id === next);
      if (!node) break;
      path.unshift(node.name);
      next = node.parent_id;
    }
    return path.join(' / ') || id;
  }
  async function save(next: DepartmentConfiguration) {
    if (pending) return;
    if (!confirmed || !reason.trim()) {
      setError(new Error('请填写变更原因并确认权限影响。'));
      return;
    }
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${orgPath}/departments`, {
        method: 'PUT',
        csrf,
        body: { configuration: next, reason: reason.trim() },
      });
      setReason('');
      setConfirmed(false);
      setNotice('部门配置已保存，新管理请求按最新授权生效；既有密钥请单独核查。');
      await client.invalidateQueries({
        predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
      });
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  function form(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    return new FormData(event.currentTarget);
  }
  const disabled = pending || !confirmed || !reason.trim();
  if (!canManage)
    return (
      <section className="panel governance-panel">
        <h2>部门与项目授权</h2>
        <p>仅组织所有者和管理员可管理部门配置。</p>
      </section>
    );
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>部门与项目授权</h2>
          <p>组织内的部门树与项目归属 · 配置版本 {value?.revision ?? '…'}</p>
        </div>
        <Button
          tag="button"
          disabled={pending}
          onClick={() => {
            setEditing('');
            setName('');
            setParent('');
            setReason('');
            setConfirmed(false);
            setError(null);
            setNotice('已刷新配置，请重新核查变更内容。');
            void configuration.refetch();
          }}
        >
          刷新部门配置
        </Button>
      </div>
      <p>
        上级部门角色覆盖下级部门项目；与项目单独授权取较高权限。组织只读角色仍保持只读，所有者和管理员保持组织权限。部门授权不会授予明文审计权限。
      </p>
      <p>
        调整归属、移动部门或删除授权会改变后续管理权限；既有项目密钥需单独撤销。成员停用会同时回收部门授权，恢复成员不会自动恢复授权。
      </p>
      {configuration.error && <ErrorBox error={configuration.error} />}
      {error != null && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      {configuration.isPending && <p role="status">正在读取部门配置…</p>}
      {value && (
        <>
          <div className="member-lifecycle-form">
            <label>
              部门配置变更原因
              <textarea
                maxLength={256}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <label>
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
              />
              我已核查部门继承影响，既有密钥将单独处理
            </label>
          </div>
          <div className="department-forms">
            <form
              className="member-lifecycle-form"
              onSubmit={(event) => {
                form(event);
                const id = editing || crypto.randomUUID();
                void save({
                  ...value,
                  departments: [
                    ...nodes.filter((d) => d.id !== id),
                    { id, name: name.trim(), parent_id: parent },
                  ],
                });
              }}
            >
              <h3>部门层级</h3>
              <label>
                编辑部门
                <select
                  aria-label="编辑部门"
                  value={editing}
                  onChange={(e) => {
                    const id = e.target.value;
                    setEditing(id);
                    const node = nodes.find((d) => d.id === id);
                    setName(node?.name ?? '');
                    setParent(node?.parent_id ?? '');
                  }}
                >
                  <option value="">新增部门</option>
                  {nodes.map((d) => (
                    <option key={d.id} value={d.id}>
                      {nodeName(d.id)}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                部门名称
                <input
                  required
                  maxLength={80}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <label>
                上级部门
                <select
                  aria-label="上级部门"
                  value={parent}
                  onChange={(e) => setParent(e.target.value)}
                >
                  <option value="">组织根目录</option>
                  {nodes
                    .filter((d) => d.id !== editing)
                    .map((d) => (
                      <option key={d.id} value={d.id}>
                        {nodeName(d.id)}
                      </option>
                    ))}
                </select>
              </label>
              <Button
                tag="button"
                type="submit"
                theme="primary"
                disabled={disabled}
                loading={pending}
              >
                保存部门层级
              </Button>
            </form>
            <form
              className="member-lifecycle-form"
              onSubmit={(event) => {
                const data = form(event);
                const [issuer, subject] = JSON.parse(String(data.get('member'))) as string[];
                const department_id = String(data.get('department'));
                void save({
                  ...value,
                  grants: [
                    ...value.grants.filter(
                      (g) =>
                        !(
                          g.department_id === department_id &&
                          g.issuer === issuer &&
                          g.subject === subject
                        ),
                    ),
                    {
                      department_id,
                      issuer,
                      subject,
                      role: String(data.get('role')) as 'admin' | 'developer' | 'viewer',
                    },
                  ],
                });
              }}
            >
              <h3>部门成员授权</h3>
              <label>
                授权部门
                <select aria-label="授权部门" name="department" required>
                  <option value="">请选择部门</option>
                  {nodes.map((d) => (
                    <option key={d.id} value={d.id}>
                      {nodeName(d.id)}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                组织成员
                <select aria-label="授权组织成员" name="member" required>
                  <option value="">请选择启用成员</option>
                  {people.map((m) => (
                    <option
                      key={JSON.stringify([m.issuer, m.subject])}
                      value={JSON.stringify([m.issuer, m.subject])}
                    >
                      {m.subject} · {m.issuer}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                部门角色
                <select name="role" aria-label="部门角色" defaultValue="viewer">
                  <option value="viewer">只读成员</option>
                  <option value="developer">开发者</option>
                  <option value="admin">项目管理员</option>
                </select>
              </label>
              {members.error && <ErrorBox error={members.error} />}
              {members.hasNextPage && (
                <Button
                  tag="button"
                  onClick={() => {
                    void members.fetchNextPage();
                  }}
                >
                  加载更多组织成员
                </Button>
              )}
              <Button
                tag="button"
                type="submit"
                theme="primary"
                disabled={disabled}
                loading={pending}
              >
                保存部门成员授权
              </Button>
            </form>
            <form
              className="member-lifecycle-form"
              onSubmit={(event) => {
                const data = form(event);
                const project_id = String(data.get('project'));
                const department_id = String(data.get('department'));
                void save({
                  ...value,
                  projects: [
                    ...value.projects.filter((p) => p.project_id !== project_id),
                    ...(department_id ? [{ project_id, department_id }] : []),
                  ],
                });
              }}
            >
              <h3>项目归属</h3>
              <label>
                归属项目
                <select aria-label="归属项目" name="project" required>
                  <option value="">请选择项目</option>
                  {projectOptions.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                归属部门
                <select aria-label="归属部门" name="department">
                  <option value="">无部门归属</option>
                  {nodes.map((d) => (
                    <option key={d.id} value={d.id}>
                      {nodeName(d.id)}
                    </option>
                  ))}
                </select>
              </label>
              {projects.error && <ErrorBox error={projects.error} />}
              {projects.hasNextPage && (
                <Button
                  tag="button"
                  onClick={() => {
                    void projects.fetchNextPage();
                  }}
                >
                  加载更多项目
                </Button>
              )}
              <Button
                tag="button"
                type="submit"
                theme="primary"
                disabled={disabled}
                loading={pending}
              >
                保存项目归属
              </Button>
            </form>
          </div>
          <h3>当前部门</h3>
          {nodes.length === 0 ? (
            <p>暂无部门，请先新增根部门。</p>
          ) : (
            <ul className="department-list">
              {nodes.map((d) => (
                <li key={d.id}>
                  <span>{nodeName(d.id)}</span>
                  <Button
                    tag="button"
                    variant="text"
                    disabled={
                      disabled ||
                      nodes.some((n) => n.parent_id === d.id) ||
                      value.grants.some((g) => g.department_id === d.id) ||
                      value.projects.some((p) => p.department_id === d.id)
                    }
                    aria-label={`删除空部门 ${d.name}`}
                    onClick={() => {
                      void save({ ...value, departments: nodes.filter((n) => n.id !== d.id) });
                    }}
                  >
                    删除空部门
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <h3>当前部门授权</h3>
          {value.grants.length === 0 ? (
            <p>暂无部门成员授权。</p>
          ) : (
            <div className="table-scroll" role="region" aria-label="部门授权表" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th>部门</th>
                    <th>身份</th>
                    <th>角色</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {value.grants.map((g) => (
                    <tr key={JSON.stringify([g.department_id, g.issuer, g.subject])}>
                      <td>{nodeName(g.department_id)}</td>
                      <td className="member-identity">
                        {g.subject}
                        <br />
                        {g.issuer}
                      </td>
                      <td>{roleNames[g.role]}</td>
                      <td>
                        <Button
                          tag="button"
                          variant="text"
                          disabled={disabled}
                          aria-label={`移除部门授权 ${g.subject} ${nodeName(g.department_id)}`}
                          onClick={() => {
                            void save({ ...value, grants: value.grants.filter((x) => x !== g) });
                          }}
                        >
                          移除部门授权
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <h3>当前项目归属</h3>
          {value.projects.length === 0 ? (
            <p>暂无项目归属。</p>
          ) : (
            <ul className="department-list">
              {value.projects.map((p) => (
                <li key={p.project_id}>
                  <span>
                    {projectOptions.find((x) => x.id === p.project_id)?.name ?? p.project_id} →{' '}
                    {nodeName(p.department_id)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <h3>配置变更历史</h3>
      {history.error && <ErrorBox error={history.error} />}
      <ul className="department-list">
        {history.data?.pages
          .flatMap((p) => p.data)
          .map((v) => (
            <li key={v.revision}>
              <span>
                版本 {v.revision} · {v.actor.subject} · {v.created_at}
                <br />
                {v.reason}
              </span>
            </li>
          ))}
      </ul>
      {history.hasNextPage && (
        <Button
          tag="button"
          onClick={() => {
            void history.fetchNextPage();
          }}
        >
          加载更多配置历史
        </Button>
      )}
    </section>
  );
}
