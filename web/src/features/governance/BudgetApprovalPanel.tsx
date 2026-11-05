import { useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import {
  api,
  type BudgetApproval,
  type BudgetApprovalPolicy,
  type BudgetApprovalPolicyChange,
} from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { date, number } from '../../shared/format';

type Props = {
  identity: string[];
  orgPath: string;
  projectPath?: string;
  csrf: string;
  isOwner: boolean;
  policy?: BudgetApprovalPolicy;
  error: unknown;
  refresh: () => void;
};
export function BudgetApprovalPanel(props: Props) {
  const client = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const history = usePage<BudgetApprovalPolicyChange>(
    props.identity,
    'approval-policy-history',
    `${props.orgPath}/budget/approval-policy/changes`,
    true,
  );
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!props.policy || pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    try {
      await api(`${props.orgPath}/budget/approval-policy`, {
        method: 'PUT',
        csrf: props.csrf,
        body: {
          enabled: data.get('enabled') === 'on',
          expected_revision: props.policy.revision,
          reason: String(data.get('reason')).trim(),
        },
      });
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>预算双人审批</h2>
            <p>
              当前策略：{props.policy ? (props.policy.enabled ? '已启用' : '未启用') : '读取中'} ·
              版本 {props.policy?.revision ?? '…'}
            </p>
          </div>
          <Button tag="button" onClick={props.refresh}>
            刷新审批策略
          </Button>
        </div>
        <p>
          启用后，组织和项目调额必须由另一位当前有效的组织管理员或所有者批准。请先配置第二位管理员；新项目需以零预算创建再申请额度。审批不冻结余额，批准时重新核查周期、版本与已用加占额。
        </p>
        <p>
          策略修改会取消所有待审批申请；所有者可关闭策略并留下原因。此策略仅控制上限调整，不替代供应商账单、预算周期核定或外部通知。
        </p>
        {props.error != null && <ErrorBox error={props.error} />}{' '}
        {error != null && <ErrorBox error={error} />}
        {props.isOwner && props.policy && (
          <form
            key={props.policy.revision}
            className="member-lifecycle-form"
            onSubmit={(e) => {
              void save(e);
            }}
          >
            <label>
              <input name="enabled" type="checkbox" defaultChecked={props.policy.enabled} />
              启用预算双人审批
            </label>
            <label>
              审批策略变更原因
              <input name="reason" required maxLength={256} />
            </label>
            <label>
              <input type="checkbox" required />
              我确认策略变更会取消所有待审批申请
            </label>
            <Button tag="button" type="submit" theme="primary" loading={pending}>
              保存审批策略
            </Button>
          </form>
        )}
        <details>
          <summary>审批策略历史</summary>
          {history.error && <ErrorBox error={history.error} />}
          <ul className="department-list">
            {history.data?.pages
              .flatMap((p) => p.data)
              .map((v) => (
                <li key={v.revision}>
                  版本 {v.revision} · {v.enabled ? '启用' : '关闭'} · {v.actor.subject} ·{' '}
                  {date(v.created_at)}
                  <br />
                  {v.reason}
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
              加载更多审批策略历史
            </Button>
          )}
        </details>
      </section>
      <ApprovalList {...props} path={`${props.orgPath}/budget`} title="组织预算申请" />
      {props.projectPath && (
        <ApprovalList {...props} path={`${props.projectPath}/budget`} title="项目预算申请" />
      )}
    </>
  );
}
function ApprovalList({ identity, path, title, csrf }: Props & { path: string; title: string }) {
  const client = useQueryClient();
  const list = usePage<BudgetApproval>(identity, 'budget-approvals', `${path}/approvals`, true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function decide(v: BudgetApproval, action: string, reason: string) {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      await api(`${path}/approvals/${encodeURIComponent(v.id)}/${action}`, {
        method: 'POST',
        csrf,
        body: { reason: reason.trim() },
      });
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  const items = list.data?.pages.flatMap((p) => p.data) ?? [];
  return (
    <section className="panel governance-panel">
      <h2>{title}</h2>
      <p>
        申请有效期 24 小时；每个账户最多一项待审批。过期或版本冲突后请撤回并重新申请。此列表按申请
        ID 分页，新增后可刷新。
      </p>
      <Button
        tag="button"
        onClick={() => {
          void list.refetch();
        }}
      >
        刷新{title}
      </Button>
      {list.error && <ErrorBox error={list.error} />} {error != null && <ErrorBox error={error} />}{' '}
      {items.length === 0 && !list.isPending && <p>暂无{title}。</p>}
      {items.map((v) => (
        <ApprovalItem key={v.id} value={v} identity={identity} pending={pending} decide={decide} />
      ))}
      {list.hasNextPage && (
        <Button
          tag="button"
          onClick={() => {
            void list.fetchNextPage();
          }}
        >
          加载更多{title}
        </Button>
      )}
    </section>
  );
}
function ApprovalItem({
  value: v,
  identity,
  pending,
  decide,
}: {
  value: BudgetApproval;
  identity: string[];
  pending: boolean;
  decide: (v: BudgetApproval, action: string, reason: string) => Promise<void>;
}) {
  const self = v.requester.issuer === identity[0] && v.requester.subject === identity[1];
  const expired = new Date(v.expires_at).getTime() <= Date.now();
  const [action, setAction] = useState(self ? 'cancel' : expired ? 'reject' : 'approve');
  const labels: Record<string, string> = {
    pending: '待审批',
    approved: '已批准',
    rejected: '已驳回',
    canceled: '已撤回/取消',
  };
  return (
    <article className="member-state-event member-identity" aria-label={`预算申请 ${v.id}`}>
      <h3>
        申请 {v.id} · {labels[v.state]}
        {v.state === 'pending' && expired ? '（已过期）' : ''}
      </h3>
      <p>
        周期 {v.period} · {v.currency} · 上限 {number(v.previous_limit)} →{' '}
        {number(v.requested_limit)} · 基础版本 {v.expected_revision}
      </p>
      <p>
        申请人 {v.requester.subject} · {date(v.created_at)} · 截止 {date(v.expires_at)}
      </p>
      <p>{v.reason}</p>
      {v.state !== 'pending' ? (
        <p>
          决定人 {v.decider?.subject ?? '未知'} · {v.decision_reason} · 生效版本{' '}
          {v.applied_revision ?? '—'}
        </p>
      ) : (
        <form
          className="member-lifecycle-form"
          onSubmit={(e) => {
            e.preventDefault();
            const data = new FormData(e.currentTarget);
            void decide(v, action, String(data.get('reason')));
          }}
        >
          <label>
            审批操作
            <select
              aria-label={`审批操作 ${v.id}`}
              value={action}
              onChange={(e) => setAction(e.target.value)}
            >
              <>
                {!self && !expired && <option value="approve">批准并应用预算（禁止自审）</option>}
              </>
              <option value="reject">驳回申请</option>
              {self && <option value="cancel">撤回申请</option>}
            </select>
          </label>
          <label>
            审批决定原因
            <input aria-label={`审批决定原因 ${v.id}`} name="reason" required maxLength={256} />
          </label>
          <Button
            tag="button"
            type="submit"
            loading={pending}
            disabled={action === 'approve' && (self || expired)}
            aria-label={`提交审批决定 ${v.id}`}
          >
            提交审批决定
          </Button>
        </form>
      )}
    </article>
  );
}
