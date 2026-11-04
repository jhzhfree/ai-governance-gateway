import { useState, type FormEvent } from 'react';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type ProjectLimits, type Inspection } from '../../api';
import { BillingPanel } from '../billing/BillingPanel';
import { RuntimePanel } from './RuntimePanel';
import { ErrorBox, Empty } from '../../shared/Feedback';

export function OperationsPanel({
  identity,
  projectPath,
  canManage,
  csrf,
  currency,
}: {
  identity: string[];
  projectPath: string;
  canManage: boolean;
  csrf: string;
  currency: string;
}) {
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [older, setOlder] = useState('300');
  const limits = useQuery({
    queryKey: [...identity, projectPath, 'limits'],
    queryFn: ({ signal }) => api<ProjectLimits>(`${projectPath}/limits`, { signal }),
  });
  const inspection = useInfiniteQuery({
    queryKey: [...identity, projectPath, 'inspection', older],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) =>
      api<Inspection>(
        `${projectPath}/inspection?older_than_seconds=${older}&limit=50&after=${encodeURIComponent(pageParam)}`,
        { signal },
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving || !limits.data) return;
    const form = new FormData(event.currentTarget);
    setSaving(true);
    setError(null);
    setNotice('');
    try {
      await api(`${projectPath}/limits`, {
        method: 'PUT',
        csrf,
        body: {
          requests_per_minute: Number(form.get('rpm')),
          max_concurrent: Number(form.get('concurrent')),
          expected_revision: limits.data.revision,
        },
      });
      setNotice('策略已保存；已有请求继续执行，频率窗口保留。');
      await limits.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }
  const snapshot = inspection.data?.pages[0];
  const items = inspection.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <>
      <RuntimePanel identity={identity} projectPath={projectPath} />
      {currency === 'CNY_MICRO' && (
        <BillingPanel
          identity={identity}
          projectPath={projectPath}
          canManage={canManage}
          csrf={csrf}
        />
      )}
      <section className="panel">
        <div className="panel-heading">
          <div>
            <h2>项目流量控制</h2>
            <p>滚动 60 秒计数；并发租约由所有网关实例共享。</p>
          </div>
          <Button
            onClick={() => {
              void limits.refetch();
              void inspection.refetch();
            }}
          >
            刷新核查
          </Button>
        </div>
        {limits.error && <ErrorBox error={limits.error} />}
        {limits.data && (
          <>
            <p>
              有效并发租约 {limits.data.active_requests} / {limits.data.max_concurrent} ·
              窗口内已接纳 {limits.data.recent_requests} / {limits.data.requests_per_minute} · 版本{' '}
              {limits.data.revision}
            </p>
            <form
              key={limits.data.revision}
              onSubmit={(event) => {
                void save(event);
              }}
            >
              <label>
                每 60 秒请求上限{' '}
                <input
                  name="rpm"
                  aria-label="每60秒请求上限"
                  type="number"
                  min="1"
                  max="10000"
                  required
                  defaultValue={limits.data.requests_per_minute}
                  disabled={!canManage || saving}
                />
              </label>{' '}
              <label>
                并发上限{' '}
                <input
                  name="concurrent"
                  aria-label="并发上限"
                  type="number"
                  min="1"
                  max="64"
                  required
                  defaultValue={limits.data.max_concurrent}
                  disabled={!canManage || saving}
                />
              </label>{' '}
              {canManage && (
                <Button type="submit" theme="primary" loading={saving}>
                  保存流量策略
                </Button>
              )}
            </form>
          </>
        )}
        {!canManage && <p>仅组织所有者或管理员可修改流量策略。</p>}
        {Boolean(error) && <ErrorBox error={error} />}
        {notice && <p role="status">{notice}</p>}
      </section>
      <section className="panel">
        <div className="panel-heading">
          <div>
            <h2>执行与预算核查</h2>
            <p>只读快照。租约到期不代表供应商已停止或费用为零；需核对供应商账单后另行处置。</p>
          </div>
          <label>
            未更新时间{' '}
            <select
              aria-label="核查时间阈值"
              value={older}
              onChange={(e) => setOlder(e.target.value)}
            >
              <option value="300">超过 5 分钟</option>
              <option value="3600">超过 1 小时</option>
              <option value="86400">超过 1 天</option>
            </select>
          </label>
        </div>
        {inspection.error && <ErrorBox error={inspection.error} />}
        {inspection.isPending && <p role="status">正在核查…</p>}
        {snapshot && (
          <>
            <p>
              快照时间：{new Date(snapshot.observed_at).toLocaleString()}
              。金额为账本最小单位，存储值与重算值直接比较。
            </p>
            <div className="table-scroll" role="region" aria-label="预算账户核查表" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th>范围 / 周期 / 币种</th>
                    <th>占额（存储 / 重算）</th>
                    <th>已用（存储 / 重算）</th>
                    <th>核查</th>
                  </tr>
                </thead>
                <tbody>
                  {snapshot.accounts.map((account) => (
                    <tr key={`${account.scope}/${account.period}/${account.currency}`}>
                      <td>
                        {account.scope} / {account.period} / {account.currency}
                      </td>
                      <td>
                        {account.stored_held} / {account.computed_held}
                      </td>
                      <td>
                        {account.stored_spent} / {account.computed_spent}
                      </td>
                      <td>{account.consistent ? '一致' : '存在差异，需人工核对'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {snapshot.accounts_truncated && <p role="status">账户超过 100 个，账户列表已截断。</p>}
            {items.length === 0 ? (
              <Empty
                title="暂无待核查请求"
                description="当前时间阈值内未发现未结束执行或预算占额。"
              />
            ) : (
              <div className="table-scroll" role="region" aria-label="异常请求核查表" tabIndex={0}>
                <table>
                  <thead>
                    <tr>
                      <th>请求编号</th>
                      <th>核查原因</th>
                      <th>账本状态</th>
                      <th>有效租约</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((item) => (
                      <tr key={item.request.request_id}>
                        <td>{item.request.request_id}</td>
                        <td>
                          {item.reasons
                            .map(
                              (reason) =>
                                ({
                                  open_hold: '预算仍占额',
                                  execution_unfinished: '执行未结束',
                                  metadata_unrecorded: '执行元数据未记录',
                                })[reason],
                            )
                            .join('、')}
                        </td>
                        <td>{item.request.state}</td>
                        <td>{item.lease_active ? '是' : '否'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {inspection.hasNextPage && (
              <Button
                loading={inspection.isFetchingNextPage}
                onClick={() => {
                  void inspection.fetchNextPage();
                }}
              >
                加载更多核查请求
              </Button>
            )}
          </>
        )}
      </section>
    </>
  );
}
