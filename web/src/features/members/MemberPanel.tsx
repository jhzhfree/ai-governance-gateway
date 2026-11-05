import { useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type MemberRecord } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { MemberLifecycleDialog } from './MemberLifecycleDialog';
import { ErrorBox, Empty } from '../../shared/Feedback';

const roles: Record<string, string> = {
  owner: '所有者',
  admin: '管理员',
  developer: '开发者',
  viewer: '只读成员',
  none: '未授权',
};
export function MemberPanel({
  identity,
  orgPath,
  projectPath,
  canRead,
  isOwner,
  csrf,
}: {
  identity: string[];
  orgPath: string;
  projectPath?: string;
  canRead: boolean;
  isOwner: boolean;
  csrf: string;
}) {
  const client = useQueryClient();
  const [scope, setScope] = useState('organization');
  const [lifecycle, setLifecycle] = useState<{ member: MemberRecord; historyOnly: boolean } | null>(
    null,
  );
  const [selected, setSelected] = useState<MemberRecord | null>(null);
  const [removing, setRemoving] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const path = scope === 'project' && projectPath ? `${projectPath}/members` : `${orgPath}/members`;
  const members = usePage<MemberRecord>(identity, 'members', path, canRead);
  const records = members.data?.pages.flatMap((page) => page.data) ?? [];
  const editable = scope === 'project' ? canRead : isOwner;
  function changed() {
    setSelected(null);
    setLifecycle(null);
    setRemoving(false);
    setError(null);
    setNotice('');
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const expected = selected
      ? scope === 'project'
        ? (selected.project_role ?? '')
        : selected.role
      : '';
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(path, {
        method: 'PUT',
        csrf,
        body: {
          issuer: form.get('issuer'),
          subject: form.get('subject'),
          role: form.get('role'),
          expected_role: expected,
        },
      });
      if (!selected) formElement.reset();
      setRemoving(false);
      setNotice('成员权限已保存，对后续管理请求生效。既有项目密钥请单独核查。');
      setSelected(null);
      await client.invalidateQueries({
        predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
      });
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  async function remove(member: MemberRecord) {
    if (pending || !member.project_role) return;
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(path, {
        method: 'DELETE',
        csrf,
        body: {
          issuer: member.issuer,
          subject: member.subject,
          expected_role: member.project_role,
        },
      });
      setNotice('项目单独授权已移除，组织/部门继承权限与既有项目密钥保持独立。');
      setSelected(null);
      setRemoving(false);
      await client.invalidateQueries({
        predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
      });
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  if (!canRead)
    return (
      <section className="panel governance-panel">
        <h2>成员与授权</h2>
        <p>仅组织所有者或管理员可查看成员标识和管理授权。</p>
      </section>
    );
  return (
    <section className="panel governance-panel">
      {lifecycle && (
        <MemberLifecycleDialog
          key={JSON.stringify([
            lifecycle.member.issuer,
            lifecycle.member.subject,
            lifecycle.historyOnly,
            lifecycle.member.lifecycle_revision,
          ])}
          identity={identity}
          member={lifecycle.member}
          historyOnly={lifecycle.historyOnly}
          orgPath={orgPath}
          csrf={csrf}
          onClose={() => setLifecycle(null)}
          onChanged={() => {
            setSelected(null);
            setNotice('组织成员状态已更新，旧密钥和已移除授权不会自动恢复。');
            void client.invalidateQueries({
              predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
            });
          }}
        />
      )}
      <div className="panel-heading">
        <div>
          <h2>成员与授权</h2>
          <p>使用企业 IdP 的准确 issuer 与 subject；此处赋权不会邀请或创建企业账号。</p>
        </div>
        <Button
          onClick={() => {
            void members.refetch();
          }}
        >
          刷新成员
        </Button>
      </div>
      <label>
        成员范围{' '}
        <select
          aria-label="成员范围"
          value={scope}
          onChange={(event) => {
            setScope(event.target.value);
            changed();
          }}
        >
          <option value="organization">组织成员</option>
          <option value="project" disabled={!projectPath}>
            当前项目授权
          </option>
        </select>
      </label>
      <p>
        组织 owner/admin 在所有项目继承管理员权限；组织 viewer 继承只读权限；组织 developer
        需要项目单独授权或部门继承授权。有效权限取较高角色，项目角色不能降低组织继承权限。
      </p>
      {scope === 'organization' && !isOwner && (
        <p>只有组织所有者可新增或调整组织成员；管理员可管理当前项目授权。</p>
      )}
      {members.error && <ErrorBox error={members.error} />}
      {Boolean(error) && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      {members.isPending && <p role="status">正在加载成员…</p>}
      {records.length === 0 && !members.isPending && (
        <Empty title="暂无可显示成员" description="请检查当前组织和查询权限。" />
      )}
      {records.length > 0 && (
        <div className="table-scroll" role="region" aria-label="成员权限表" tabIndex={0}>
          <table>
            <thead>
              <tr>
                <th>身份标识</th>
                <th>组织角色 / 状态</th>
                <th>项目单独授权</th>
                <th>当前范围有效权限</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {records.map((member) => (
                <tr key={JSON.stringify([member.issuer, member.subject])}>
                  <td>
                    {member.subject}
                    <br />
                    <small>{member.issuer}</small>
                  </td>
                  <td>
                    {roles[member.role]}
                    <br />
                    {member.enabled ? '启用' : '已停用'}
                  </td>
                  <td>{member.project_role ? roles[member.project_role] : '—'}</td>
                  <td>{roles[member.effective_role]}</td>
                  <td>
                    {editable &&
                      member.enabled &&
                      (scope === 'organization'
                        ? member.role !== 'owner'
                        : member.role === 'developer') && (
                        <Button
                          size="small"
                          onClick={() => {
                            setSelected(member);
                            setRemoving(false);
                            setError(null);
                            setNotice('');
                          }}
                        >
                          配置权限 {member.subject}
                        </Button>
                      )}
                    {scope === 'organization' && (
                      <>
                        {isOwner && member.role !== 'owner' && (
                          <Button
                            tag="button"
                            size="small"
                            onClick={() => {
                              changed();
                              setLifecycle({ member, historyOnly: false });
                            }}
                          >
                            {member.enabled ? '停用成员' : '恢复成员'} {member.subject}
                          </Button>
                        )}
                        <Button
                          tag="button"
                          size="small"
                          variant="text"
                          onClick={() => {
                            setLifecycle({ member, historyOnly: true });
                          }}
                        >
                          状态历史 {member.subject}
                        </Button>
                      </>
                    )}
                    {scope === 'project' && member.project_role && (
                      <Button
                        size="small"
                        variant="text"
                        disabled={pending}
                        onClick={() => {
                          setSelected(member);
                          setError(null);
                          setRemoving(true);
                          setNotice('请确认要移除项目单独授权；此操作不会撤销项目密钥。');
                        }}
                      >
                        移除授权 {member.subject}
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {members.hasNextPage && (
        <Button
          loading={members.isFetchingNextPage}
          onClick={() => {
            void members.fetchNextPage();
          }}
        >
          加载更多成员
        </Button>
      )}
      {editable && !removing && (selected || scope === 'organization') && (
        <form
          key={`${scope}/${selected ? JSON.stringify([selected.issuer, selected.subject]) : 'new'}`}
          onSubmit={(event) => {
            void save(event);
          }}
        >
          <h3>{selected ? '调整成员权限' : '新增组织成员'}</h3>
          <label>
            身份签发方{' '}
            <input
              name="issuer"
              aria-label="成员身份签发方"
              defaultValue={selected?.issuer ?? identity[0]}
              readOnly={Boolean(selected)}
              required
              maxLength={2048}
            />
          </label>{' '}
          <label>
            身份 subject{' '}
            <input
              name="subject"
              aria-label="成员身份subject"
              defaultValue={selected?.subject ?? ''}
              readOnly={Boolean(selected)}
              required
              maxLength={256}
            />
          </label>{' '}
          <label>
            分配角色{' '}
            <select
              name="role"
              aria-label="分配角色"
              defaultValue={
                scope === 'project'
                  ? (selected?.project_role ?? 'developer')
                  : (selected?.role ?? 'developer')
              }
            >
              <option value="admin">管理员</option>
              <option value="developer">开发者</option>
              <option value="viewer">只读成员</option>
            </select>
          </label>{' '}
          <Button type="submit" theme="primary" loading={pending}>
            保存成员权限
          </Button>
          {selected && (
            <Button
              variant="text"
              onClick={() => {
                setSelected(null);
                setNotice('');
              }}
            >
              取消编辑成员
            </Button>
          )}
        </form>
      )}
      {scope === 'project' && removing && selected?.project_role && (
        <Button
          theme="danger"
          variant="outline"
          disabled={pending}
          onClick={() => {
            void remove(selected);
          }}
        >
          确认移除项目授权
        </Button>
      )}
      {removing && (
        <Button
          variant="text"
          onClick={() => {
            setRemoving(false);
            setSelected(null);
            setNotice('');
          }}
        >
          取消移除授权
        </Button>
      )}
      <p>
        本阶段不删除组织成员或转移所有者。停用企业账号由 IdP
        管理；降低成员权限或移除项目授权不自动撤销项目密钥，请在访问密钥页单独撤销。
      </p>
    </section>
  );
}
