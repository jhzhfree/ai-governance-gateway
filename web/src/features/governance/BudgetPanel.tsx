import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type BudgetAccount, type BudgetChange, type BudgetCycle } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { date, number } from '../../shared/format';

type Props = {
  identity: string[];
  orgPath: string;
  projectPath?: string;
  canManage: boolean;
  csrf: string;
};
export function BudgetPanel(props: Props) {
  return (
    <>
      {props.canManage && (
        <AccountEditor {...props} path={`${props.orgPath}/budget`} title="组织预算上限" />
      )}
      {props.projectPath && (
        <AccountEditor {...props} path={`${props.projectPath}/budget`} title="项目预算上限" />
      )}
      {props.canManage && <CycleEditor {...props} />}
    </>
  );
}
function AccountEditor({
  identity,
  path,
  title,
  canManage,
  csrf,
}: Props & { path: string; title: string }) {
  const client = useQueryClient();
  const query = useQuery({
    queryKey: [...identity, path],
    queryFn: ({ signal }) => api<BudgetAccount>(path, { signal }),
  });
  const history = usePage<BudgetChange>(
    identity,
    'budget-changes',
    `${path}/changes?period=${encodeURIComponent(query.data?.period ?? '')}`,
    Boolean(query.data),
  );
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!query.data) return;
    const form = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(path, {
        method: 'PUT',
        csrf,
        body: {
          period: query.data.period,
          expected_revision: query.data.revision,
          limit: Number(form.get('limit')),
          reason: form.get('reason'),
        },
      });
      setNotice('预算已更新');
      await client.invalidateQueries();
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>{title}</h2>
          <p>下调不能低于已用加占额；调整不退款、不修改历史费用。组织和项目上限同时约束请求。</p>
        </div>
        <Button
          onClick={() => {
            void query.refetch();
            void history.refetch();
          }}
        >
          刷新{title}
        </Button>
      </div>
      {query.error && <ErrorBox error={query.error} />}
      {Boolean(error) && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      {query.data && (
        <>
          <p>
            周期 {query.data.period} · 单位{' '}
            {query.data.currency === 'CNY_MICRO'
              ? '微元（1元=1,000,000微元）'
              : query.data.currency}{' '}
            · 上限 {number(query.data.limit)} · 已用 {number(query.data.spent)} · 占额{' '}
            {number(query.data.held)} · 配置版本 {query.data.revision}
          </p>
          {canManage && (
            <form
              key={`${query.data.period}/${query.data.revision}`}
              onSubmit={(event) => {
                void save(event);
              }}
            >
              <label>
                新预算上限
                <input
                  aria-label={`${title}新上限`}
                  name="limit"
                  type="number"
                  min={0}
                  max={9007199254740991}
                  step={1}
                  required
                  defaultValue={query.data.limit}
                />
              </label>
              <label>
                调整原因
                <input aria-label={`${title}调整原因`} name="reason" required maxLength={256} />
              </label>
              <Button type="submit" theme="primary" loading={pending}>
                保存{title}
              </Button>
            </form>
          )}
        </>
      )}
      {history.error && <ErrorBox error={history.error} />}
      <div className="table-scroll" role="region" aria-label={`${title}调整历史`} tabIndex={0}>
        <table>
          <thead>
            <tr>
              <th>版本</th>
              <th>原上限 → 新上限</th>
              <th>原因 / 操作人</th>
              <th>时间</th>
            </tr>
          </thead>
          <tbody>
            {history.data?.pages
              .flatMap((page) => page.data)
              .map((item) => (
                <tr key={item.revision}>
                  <td>{item.revision}</td>
                  <td>
                    {number(item.previous_limit)} → {number(item.new_limit)}
                  </td>
                  <td>
                    {item.reason}
                    <br />
                    {item.actor.subject}
                  </td>
                  <td>{date(item.created_at)}</td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>
      {history.hasNextPage && (
        <Button
          loading={history.isFetchingNextPage}
          onClick={() => {
            void history.fetchNextPage();
          }}
        >
          加载更多预算变更
        </Button>
      )}
    </section>
  );
}
function CycleEditor({ identity, orgPath, csrf }: Props) {
  const client = useQueryClient();
  const path = `${orgPath}/budget/cycle`;
  const cycle = useQuery({
    queryKey: [...identity, path],
    queryFn: ({ signal }) => api<BudgetCycle>(path, { signal }),
  });
  const account = useQuery({
    queryKey: [...identity, `${orgPath}/budget`],
    queryFn: ({ signal }) => api<BudgetAccount>(`${orgPath}/budget`, { signal }),
  });
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const save = async (event: FormEvent<HTMLFormElement>, rotate: boolean) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      if (rotate) {
        await api(`${orgPath}/budget/period`, {
          method: 'POST',
          csrf,
          body: { expected_period: account.data?.period, next_period: form.get('next') },
        });
      } else {
        await api(path, {
          method: 'PUT',
          csrf,
          body: { expected_revision: cycle.data?.revision, enabled: form.get('enabled') === 'on' },
        });
      }
      setNotice(rotate ? '预算周期已切换' : '月度轮转设置已更新');
      await client.invalidateQueries();
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>预算周期管理</h2>
          <p>
            自动轮转采用 UTC 月份，继承上一周期上限。旧周期占额和迟到结算保留原归属，不清零旧账。
          </p>
        </div>
      </div>
      {cycle.error && <ErrorBox error={cycle.error} />}{' '}
      {Boolean(error) && <ErrorBox error={error} />} {notice && <p role="status">{notice}</p>}
      {cycle.data && (
        <form
          key={cycle.data.revision}
          onSubmit={(event) => {
            void save(event, false);
          }}
        >
          <label>
            <input
              aria-label="启用预算月度轮转"
              name="enabled"
              type="checkbox"
              defaultChecked={cycle.data.enabled}
            />
            启用 UTC 月度自动轮转
          </label>
          <Button type="submit" loading={pending}>
            保存月度轮转设置
          </Button>
          <p>
            后台每分钟检查，月初首次授权推理也会触发。停机跨月时切到当前月份，不补建没有流量的空月份。
          </p>
        </form>
      )}
      {account.data && (
        <form
          onSubmit={(event) => {
            void save(event, true);
          }}
        >
          <h3>手动切换周期</h3>
          <p>
            当前 {account.data.period}
            。切换将立即影响新请求；只能前进，目标周期若已有账户则拒绝，不覆盖历史数据。
          </p>
          <label>
            新周期
            <input
              aria-label="新预算周期"
              name="next"
              type="month"
              required
              min={account.data.period}
            />
          </label>
          <label>
            <input type="checkbox" required aria-label="确认立即切换预算周期" />
            确认立即切换组织及所有项目预算周期
          </label>
          <Button type="submit" theme="primary" loading={pending}>
            切换预算周期
          </Button>
        </form>
      )}
    </section>
  );
}
