import { useState, type FormEvent } from 'react';
import { Button, Dialog } from 'tdesign-react';
import { api, type MemberRecord, type MemberLifecycleEvent } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { roleNames } from '../../shared/format';

export function MemberLifecycleDialog({
  identity,
  member,
  historyOnly,
  orgPath,
  csrf,
  onClose,
  onChanged,
}: {
  identity: string[];
  member: MemberRecord;
  historyOnly: boolean;
  orgPath: string;
  csrf: string;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [reason, setReason] = useState('');
  const [opened, setOpened] = useState(false);
  const [restoreRole, setRestoreRole] = useState('developer');
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const path = `${orgPath}/members/lifecycle?${new URLSearchParams({ issuer: member.issuer, subject: member.subject })}`;
  const history = usePage<MemberLifecycleEvent>(identity, 'member-lifecycle', path, historyOnly);
  const items = history.data?.pages.flatMap((p) => p.data) ?? [];
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!confirmed || pending) return;
    setPending(true);
    setError(null);
    try {
      await api(`${orgPath}/members/enabled`, {
        method: 'PUT',
        csrf,
        body: {
          target: { issuer: member.issuer, subject: member.subject },
          expected_revision: member.lifecycle_revision,
          enabled: !member.enabled,
          ...(member.enabled ? {} : { restore_role: restoreRole }),
          reason,
        },
      });
      onChanged();
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <Dialog
      visible
      onOpened={() => setOpened(true)}
      header={historyOnly ? '成员状态历史' : member.enabled ? '停用组织成员' : '恢复组织成员'}
      footer={false}
      closeOnOverlayClick={false}
      onClose={() => {
        if (!pending) onClose();
      }}
    >
      <div
        className="governance-panel"
        role="region"
        aria-label={historyOnly ? '成员状态历史内容' : '成员状态变更内容'}
        tabIndex={0}
        data-dialog-ready={opened ? 'true' : 'false'}
      >
        <p className="member-identity">
          {member.subject}
          <br />
          {member.issuer}
        </p>
        {historyOnly ? (
          <>
            {history.error && <ErrorBox error={history.error} />}
            {history.isPending && <p role="status">正在读取状态历史…</p>}
            {items.map((v) => (
              <div className="member-state-event" key={v.revision}>
                <h3>
                  第 {v.revision} 次变更 · {v.enabled ? '恢复' : '停用'}
                </h3>
                <p>
                  {v.created_at} · {v.actor.subject}
                </p>
                <p>
                  {roleNames[v.previous_role]} → {roleNames[v.new_role]}
                </p>
                <p>{v.reason}</p>
                <p>
                  撤销设备 {v.revoked_devices} · 撤销密钥 {v.revoked_keys} · 移除项目授权{' '}
                  {v.removed_project_grants} · 回收部门授权 {v.removed_department_grants} ·
                  撤销审计员 {v.revoked_auditors} · 取消交接 {v.canceled_transfers}
                </p>
              </div>
            ))}
            {!history.isPending && !history.error && items.length === 0 && (
              <p>暂无成员状态变更记录。</p>
            )}
            {history.hasNextPage && (
              <Button
                tag="button"
                loading={history.isFetchingNextPage}
                onClick={() => {
                  void history.fetchNextPage();
                }}
              >
                加载更多状态历史
              </Button>
            )}
          </>
        ) : (
          <form
            className="member-lifecycle-form"
            onSubmit={(event) => {
              void submit(event);
            }}
          >
            {member.enabled ? (
              <p>
                仅停用当前组织身份：撤销此人在本组织签发的项目密钥，移除部门授权、项目单独授权和审计员权限，并取消待接收交接。在途已授权调用可能继续结算；其他组织与全局登录保持独立。
              </p>
            ) : (
              <>
                <p>
                  恢复必须明确选择组织角色。旧密钥、部门授权、项目单独授权、审计员权限和已取消交接不会自动恢复。
                </p>
                <label>
                  恢复后的组织角色
                  <select
                    value={restoreRole}
                    onChange={(event) => setRestoreRole(event.target.value)}
                    aria-label="恢复后的组织角色"
                  >
                    <option value="developer">开发者（需重新授予项目权限）</option>
                    <option value="viewer">只读成员（组织继承只读权限）</option>
                    <option value="admin">管理员（组织继承管理权限）</option>
                  </select>
                </label>
              </>
            )}
            <label>
              成员状态变更原因
              <textarea
                required
                maxLength={256}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
            <label>
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              {member.enabled
                ? '我确认停用该组织成员并永久撤销旧密钥'
                : '我确认按所选角色恢复组织身份并重新配置授权'}
            </label>
            {error != null && <ErrorBox error={error} />}
            <Button
              tag="button"
              type="submit"
              theme="primary"
              disabled={!confirmed}
              loading={pending}
            >
              {member.enabled ? '确认停用成员并回收授权' : '确认恢复组织成员'}
            </Button>
          </form>
        )}
      </div>
    </Dialog>
  );
}
