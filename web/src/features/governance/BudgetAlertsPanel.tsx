import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type AlertPolicy, type BudgetAlert } from '../../api';
import { ErrorBox } from '../../shared/Feedback';
import { usePage } from '../../shared/useResourcePage';

function ScopeAlerts({
  identity,
  path,
  title,
  canManage,
  csrf,
}: {
  identity: string[];
  path: string;
  title: string;
  canManage: boolean;
  csrf: string;
}) {
  const client = useQueryClient();
  const policy = useQuery({
    queryKey: [...identity, path, 'alert-policy'],
    queryFn: ({ signal }) => api<AlertPolicy>(`${path}/policy`, { signal }),
  });
  const alerts = usePage<BudgetAlert>(identity, 'budget-alerts', path, true);
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<BudgetAlert | null>(null);
  const [reason, setReason] = useState('');
  const refresh = () => {
    void policy.refetch();
    void alerts.refetch();
  };
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!policy.data) return;
    const form = new FormData(event.currentTarget);
    const thresholds = form
      .getAll('threshold')
      .map(Number)
      .sort((a, b) => a - b);
    if (thresholds.length === 0) {
      setError(new Error('请至少选择一个阈值。'));
      return;
    }
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${path}/policy`, {
        method: 'PUT',
        csrf,
        body: {
          enabled: form.has('enabled'),
          thresholds,
          expected_revision: policy.data.revision,
          reason: form.get('reason'),
        },
      });
      setNotice('预算告警策略已更新');
      await client.invalidateQueries({ queryKey: [...identity, path, 'alert-policy'] });
      void alerts.refetch();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  async function acknowledge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${path}/${selected.id}/acknowledge`, { method: 'POST', csrf, body: { reason } });
      setSelected(null);
      setReason('');
      setNotice('预算告警已确认');
      void alerts.refetch();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  const items = alerts.data?.pages.flatMap((p) => p.data) ?? [];
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>{title}</h2>
          <p>按已用＋占额触发；同一周期每个阈值只提醒一次，确认处理不会释放预算。</p>
        </div>
        <Button onClick={refresh} loading={alerts.isFetching}>
          刷新{title}
        </Button>
      </div>
      {policy.error && <ErrorBox error={policy.error} />}
      {alerts.error && <ErrorBox error={alerts.error} />}
      {error != null && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      {policy.isPending && <p role="status">正在读取告警策略…</p>}
      {policy.data &&
        (canManage ? (
          <form
            key={policy.data.revision}
            className="budget-alert-form"
            onSubmit={(event) => {
              void save(event);
            }}
          >
            <label>
              <input type="checkbox" name="enabled" defaultChecked={policy.data.enabled} />
              启用{title}
            </label>
            <fieldset>
              <legend>{title}触发阈值（已用＋占额）</legend>
              <div className="budget-alert-thresholds">
                {([50, 60, 70, 80, 90, 95, 100] as const).map((value) => (
                  <label key={value}>
                    <input
                      type="checkbox"
                      name="threshold"
                      value={value}
                      defaultChecked={policy.data?.thresholds.includes(value)}
                    />
                    {value}%
                  </label>
                ))}
              </div>
            </fieldset>
            <label>
              {title}调整原因
              <input name="reason" required maxLength={256} disabled={pending} />
            </label>
            <Button type="submit" theme="primary" loading={pending}>
              保存{title}策略
            </Button>
          </form>
        ) : (
          <p>
            告警策略：{policy.data.enabled ? '启用' : '关闭'} · 阈值{' '}
            {policy.data.thresholds.join('%、')}%
          </p>
        ))}
      <p>
        触发时的金额均为最小账本单位。占额可能随后释放或结算，历史告警会保留；预算为零时不触发比例告警。
      </p>
      {alerts.isPending && <p role="status">正在读取告警…</p>}
      <div className="table-scroll" role="region" aria-label={`${title}记录表`} tabIndex={0}>
        <table>
          <thead>
            <tr>
              {[
                '编号 / 时间',
                '周期 / 币种',
                '阈值',
                '触发时预算',
                '触发时已用 / 占额',
                '处理情况',
              ].map((text) => (
                <th key={text}>{text}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr key={item.id}>
                <td>
                  {item.id}
                  <br />
                  {item.created_at}
                </td>
                <td>
                  {item.period} / {item.currency}
                </td>
                <td>{item.threshold}%</td>
                <td>{item.limit}</td>
                <td>
                  {item.spent} / {item.held}
                </td>
                <td>
                  {item.acknowledged_at ? (
                    <>
                      <span>已确认</span>
                      <br />
                      {item.actor?.subject} · {item.acknowledged_at}
                      <br />
                      {item.reason}
                    </>
                  ) : (
                    <>
                      <span>待确认</span>
                      {canManage && (
                        <Button
                          onClick={() => {
                            setSelected(item);
                            setReason('');
                            setError(null);
                          }}
                          aria-label={`处理${title} ${item.id}`}
                        >
                          处理
                        </Button>
                      )}
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!alerts.isPending && !alerts.error && items.length === 0 && <p>暂无预算阈值告警。</p>}
      {alerts.hasNextPage && (
        <Button
          loading={alerts.isFetchingNextPage}
          onClick={() => {
            void alerts.fetchNextPage();
          }}
        >
          加载更多{title}
        </Button>
      )}
      {selected && (
        <form
          className="budget-alert-form"
          aria-label={`确认${title}`}
          onSubmit={(event) => {
            void acknowledge(event);
          }}
        >
          <h3>处理告警 {selected.id}</h3>
          <p>此操作只记录确认，不增加额度、不解除预算拦截。</p>
          <label>
            {title}处理说明
            <textarea
              required
              maxLength={256}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <div className="row-actions">
            <Button theme="primary" type="submit" loading={pending}>
              确认处理{title}
            </Button>
            <Button disabled={pending} onClick={() => setSelected(null)}>
              取消处理
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

export function BudgetAlertsPanel({
  identity,
  orgPath,
  projectPath,
  canManage,
  csrf,
}: {
  identity: string[];
  orgPath: string;
  projectPath?: string;
  canManage: boolean;
  csrf: string;
}) {
  return (
    <>
      <p>站内告警保存在数据库中，请刷新查看新告警；当前阶段不发送邮件或外部消息。</p>
      {canManage && (
        <ScopeAlerts
          key={`${orgPath}:org`}
          identity={identity}
          path={`${orgPath}/budget/alerts`}
          title="组织预算告警"
          canManage
          csrf={csrf}
        />
      )}
      {projectPath && (
        <ScopeAlerts
          key={projectPath}
          identity={identity}
          path={`${projectPath}/budget/alerts`}
          title="项目预算告警"
          canManage={canManage}
          csrf={csrf}
        />
      )}
    </>
  );
}
