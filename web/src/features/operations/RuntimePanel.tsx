import { useQuery } from '@tanstack/react-query';
import { Button, Tag } from 'tdesign-react';
import { api, type Runtime } from '../../api';
import { ErrorBox } from '../../shared/Feedback';

const stateNames = {
  not_applicable: '模拟供应商',
  unobserved: '尚无记录',
  closed: '正常放行',
  open: '暂停派发',
  half_open: '单次探测中',
};
export function RuntimePanel({
  identity,
  projectPath,
}: {
  identity: string[];
  projectPath: string;
}) {
  const query = useQuery({
    queryKey: [...identity, projectPath, 'runtime'],
    queryFn: ({ signal }) => api<Runtime>(`${projectPath}/runtime`, { signal }),
  });
  const data = query.data;
  const metrics = data?.instance;
  const counters = metrics?.counters;
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <h2>供应商运行状态</h2>
          <p>连续 5 次供应商故障后暂停 30 秒，再由下一条新请求进行单次探测。刷新不会调用供应商。</p>
        </div>
        <Button
          loading={query.isFetching}
          onClick={() => {
            void query.refetch();
          }}
        >
          刷新运行状态
        </Button>
      </div>
      {query.error && <ErrorBox error={query.error} />}
      {query.isPending && <p role="status">正在读取运行状态…</p>}
      {data && (
        <>
          <div className="table-scroll" role="region" aria-label="供应商熔断状态表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>授权模型</th>
                  <th>供应商</th>
                  <th>共享熔断状态</th>
                  <th>等待提示</th>
                </tr>
              </thead>
              <tbody>
                {data.models.map((model) => (
                  <tr key={model.model}>
                    <td>{model.model}</td>
                    <td>{model.provider}</td>
                    <td>
                      <Tag
                        variant="light"
                        theme={
                          model.state === 'open'
                            ? 'danger'
                            : model.state === 'closed'
                              ? 'success'
                              : 'default'
                        }
                      >
                        {stateNames[model.state]}
                      </Tag>
                    </td>
                    <td>
                      {model.retry_after > 0
                        ? `${model.retry_after} 秒`
                        : model.state === 'open' || model.state === 'half_open'
                          ? '可接纳下一次探测'
                          : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {data.models.length === 0 && <p>当前项目没有可用的授权模型。</p>}
          <p>
            全周期开放占额：{data.open_holds} 笔 / {data.held_amount} 最小账本单位 · 执行未结束：
            {data.unfinished_executions} 笔。熔断恢复不会释放这些占额。
          </p>
          <h3>当前实例 HTTP 观测</h3>
          {!metrics ? (
            <p>当前实例监控不可用。</p>
          ) : (
            <>
              <p>
                实例：{metrics.instance_id} · 采集起点：
                {new Date(metrics.started_at).toLocaleString()}
                。负载均衡时实例可能变化，重启后清零。
              </p>
              {!metrics.tracked ? (
                <p>当前实例暂无本项目观测，或已达采集上限。</p>
              ) : (
                counters && (
                  <>
                    <p>
                      请求量：{counters.requests} · HTTP 429/503：{counters.rejected}（
                      {counters.requests
                        ? ((counters.rejected / counters.requests) * 100).toFixed(1)
                        : '0.0'}
                      %） · HTTP 5xx：
                      {counters.server_errors} · 平均耗时：
                      {counters.requests
                        ? (counters.duration_ms / counters.requests).toFixed(1)
                        : '—'}{' '}
                      ms
                    </p>
                    <p>
                      耗时包含网关处理和清理；SSE 的 HTTP 200 不保证执行成功，请结合调用账本核查。
                    </p>
                    <div
                      className="table-scroll"
                      role="region"
                      aria-label="请求耗时分布表"
                      tabIndex={0}
                    >
                      <table>
                        <thead>
                          <tr>
                            <th>耗时上界</th>
                            <th>累计请求数</th>
                          </tr>
                        </thead>
                        <tbody>
                          {[100, 500, 1000, 5000, 30000, 120000].map((bound, i) => (
                            <tr key={bound}>
                              <td>≤ {bound} ms</td>
                              <td>{counters.buckets[i]}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </>
                )
              )}
            </>
          )}
          <p>
            共享状态快照：{new Date(data.observed_at).toLocaleString()}
            。状态不代表供应商驻留或合同验收。
          </p>
        </>
      )}
    </section>
  );
}
