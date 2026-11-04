import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type MemberRecord, type OwnershipTransfer, type Session } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';

export function OwnershipPanel({
  identity,
  orgPath,
  isOwner,
  session,
}: {
  identity: string[];
  orgPath: string;
  isOwner: boolean;
  session: Session;
}) {
  const client = useQueryClient();
  const path = `${orgPath}/ownership-transfer`;
  const query = useQuery({
    queryKey: [...identity, path],
    queryFn: ({ signal }) => api<{ transfer: OwnershipTransfer | null }>(path, { signal }),
  });
  const members = usePage<MemberRecord>(
    identity,
    'ownership-candidates',
    `${orgPath}/members`,
    isOwner,
  );
  const choices =
    members.data?.pages.flatMap((p) => p.data).filter((m) => m.role !== 'owner') ?? [];
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [consentID, setConsentID] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const transfer = query.data?.transfer;
  const recipient =
    transfer?.target.issuer === session.user.issuer &&
    transfer.target.subject === session.user.subject;
  const refresh = () => {
    setError(null);
    setNotice('');
    setConfirmed(false);
    setConsentID('');
    void client.invalidateQueries({
      predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
    });
  };
  const invalidate = async () => {
    await client.invalidateQueries({
      predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
    });
  };
  async function request(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const member = choices.find((m) => JSON.stringify([m.issuer, m.subject]) === target);
    if (!member || !confirmed || pending) return;
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(path, {
        method: 'POST',
        csrf: session.csrf_token,
        body: {
          target: { issuer: member.issuer, subject: member.subject },
          expected_role: member.role,
          reason,
        },
      });
      setReason('');
      setConfirmed(false);
      setNotice('交接请求已创建，等待接收成员登录确认。');
      await invalidate();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  async function decide(action: 'accept' | 'cancel') {
    if (!transfer || pending || (action === 'accept' && consentID !== transfer.id)) return;
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${path}/${encodeURIComponent(transfer.id)}/${action}`, {
        method: 'POST',
        csrf: session.csrf_token,
      });
      setConfirmed(false);
      setConsentID('');
      setNotice(action === 'accept' ? '组织所有权交接已完成。' : '组织交接请求已取消。');
      await invalidate();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>组织所有权交接</h2>
          <p>仅现任所有者可发起，接收成员必须本人登录确认，请求 24 小时后失效。</p>
        </div>
        <Button tag="button" onClick={refresh} loading={query.isFetching}>
          刷新组织交接
        </Button>
      </div>
      <p>
        交接完成后原所有者成为管理员。既有项目密钥和独立审计员授权不会因交接自动撤销或新增；成员离职请另行核查凭据与授权。
      </p>
      {query.error && <ErrorBox error={query.error} />}
      {isOwner && members.error && <ErrorBox error={members.error} />}
      {error != null && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      {query.isPending && <p role="status">正在读取组织交接…</p>}
      {transfer ? (
        <div className="ownership-details">
          <p>
            发起人：{transfer.owner.subject}
            <br />
            {transfer.owner.issuer}
          </p>
          <p>
            接收成员：{transfer.target.subject}
            <br />
            {transfer.target.issuer}
          </p>
          <p>交接原因：{transfer.reason}</p>
          <p>有效期至：{new Date(transfer.expires_at).toLocaleString()}</p>
          {recipient ? (
            <>
              <label>
                <input
                  type="checkbox"
                  checked={consentID === transfer.id}
                  onChange={(event) => setConsentID(event.target.checked ? transfer.id : '')}
                />
                我确认接任组织所有者，并了解原所有者将成为管理员
              </label>
              <Button
                tag="button"
                theme="primary"
                disabled={consentID !== transfer.id}
                loading={pending}
                onClick={() => {
                  void decide('accept');
                }}
              >
                确认接任所有者
              </Button>
            </>
          ) : (
            <Button
              tag="button"
              disabled={!isOwner}
              loading={pending}
              onClick={() => {
                void decide('cancel');
              }}
            >
              取消组织交接
            </Button>
          )}
        </div>
      ) : (
        !query.isPending &&
        !query.error &&
        (isOwner ? (
          <form
            className="ownership-form"
            onSubmit={(event) => {
              void request(event);
            }}
          >
            <label>
              接收成员
              <select
                required
                value={target}
                onChange={(event) => {
                  setTarget(event.target.value);
                  setConfirmed(false);
                }}
              >
                <option value="">请选择已有组织成员</option>
                {choices.map((m) => (
                  <option
                    key={JSON.stringify([m.issuer, m.subject])}
                    value={JSON.stringify([m.issuer, m.subject])}
                  >
                    {m.subject} · {m.issuer}
                  </option>
                ))}
              </select>
            </label>
            {members.hasNextPage && (
              <Button
                tag="button"
                loading={members.isFetchingNextPage}
                onClick={() => {
                  void members.fetchNextPage();
                }}
              >
                加载更多接收成员
              </Button>
            )}
            {choices.length === 0 && !members.isPending && (
              <p>请先在成员管理添加接收成员；本操作不会邀请或创建账号。</p>
            )}
            <label>
              组织交接原因
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
                required
                checked={confirmed}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              我已核对接收身份，确认后我将成为管理员
            </label>
            <Button
              tag="button"
              theme="primary"
              type="submit"
              loading={pending}
              disabled={!confirmed || !target}
            >
              发起组织交接
            </Button>
          </form>
        ) : (
          <p>暂无需要你确认的组织交接。其他成员的交接详情不会在此显示。</p>
        ))
      )}
    </section>
  );
}
