import type { FormEvent } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Button, Tag } from 'tdesign-react';
import { api, type RequestRecord } from '../../api';
import { ErrorBox, Empty } from '../../shared/Feedback';
import { date, number } from '../../shared/format';
import { usePage } from '../../shared/useResourcePage';

const states: Record<RequestRecord['state'], string> = {
  reserved: '已预留',
  dispatched: '已派发',
  uncertain: '待核对',
  settled: '已结算',
  released: '已释放',
  estimated_final: '估算结算',
};
const outcomes: Record<string, string> = {
  unfinished: '未结束',
  completed: '执行完成',
  failed: '执行失败',
  canceled: '已取消',
};
const duration = (value: number | null) => (value === null ? '未结束' : `${number(value)} ms`);
const actual = (record: RequestRecord) =>
  record.actual === null ? '待结算' : number(record.actual);

export function RequestPanel({
  identity,
  projectPath,
}: {
  identity: string[];
  projectPath: string;
}) {
  const [params, setParams] = useSearchParams();
  const state = params.get('state') ?? '';
  const outcome = params.get('outcome') ?? '';
  const id = params.get('request') ?? '';
  const records = usePage<RequestRecord>(
    identity,
    'requests',
    `${projectPath}/requests?state=${encodeURIComponent(state)}&outcome=${encodeURIComponent(outcome)}`,
    true,
  );
  const detail = useQuery({
    queryKey: [...identity, 'request', projectPath, id],
    enabled: Boolean(id),
    queryFn: ({ signal }) =>
      api<RequestRecord>(`${projectPath}/requests/${encodeURIComponent(id)}`, { signal }),
  });
  const update = (name: string, value: string) => {
    setParams((previous) => {
      const next = new URLSearchParams(previous);
      if (value) next.set(name, value);
      else next.delete(name);
      return next;
    });
  };
  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    update('request', String(new FormData(event.currentTarget).get('request') ?? '').trim());
  }
  const items = records.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <section className="panel request-panel">
      <div className="panel-heading">
        <div>
          <h2>项目调用账本</h2>
          <p>仅包含已建立预算预留的请求；跨周期查询，创建时间倒序。</p>
        </div>
        <Tag variant="outline">仅含治理元数据</Tag>
      </div>
      <p className="request-note">
        账本状态用于核对费用，不代表响应已送达客户端。待核对请求继续占用预留，不自动退款。执行结果与账本状态独立；未结束也可能表示进程中断。
      </p>
      <div className="request-filters">
        <label>
          账本状态
          <select
            aria-label="账本状态"
            value={state}
            onChange={(event) => update('state', event.target.value)}
          >
            <option value="">全部状态</option>
            {Object.entries(states).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label>
          执行结果
          <select
            aria-label="执行结果"
            value={outcome}
            onChange={(event) => update('outcome', event.target.value)}
          >
            <option value="">全部结果</option>
            {Object.entries(outcomes).map(([key, label]) => (
              <option key={key} value={key}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <form key={id} onSubmit={search}>
          <label htmlFor="request-search">
            请求编号
            <input
              id="request-search"
              name="request"
              defaultValue={id}
              maxLength={256}
              placeholder="输入完整 request_id"
            />
          </label>
          <Button type="submit" variant="outline">
            查询详情
          </Button>
        </form>
      </div>
      {id && (
        <section className="request-detail" aria-label="调用账本详情">
          <div className="panel-heading">
            <h3>调用账本详情</h3>
            <Button
              variant="text"
              onClick={() => {
                update('request', '');
              }}
            >
              关闭详情
            </Button>
          </div>
          {detail.error ? (
            <ErrorBox
              error={detail.error}
              retry={() => {
                void detail.refetch();
              }}
            />
          ) : detail.isPending ? (
            <p role="status">正在读取详情…</p>
          ) : (
            detail.data && (
              <dl>
                <dt>请求编号</dt>
                <dd>
                  <code>{detail.data.request_id}</code>
                </dd>
                {detail.data.attempt ? (
                  <>
                    <dt>Attempt ID</dt>
                    <dd>
                      <code>{detail.data.attempt.attempt_id}</code>
                    </dd>
                    <dt>模型 / 供应商</dt>
                    <dd>
                      {detail.data.attempt.model} / {detail.data.attempt.provider}
                    </dd>
                    <dt>配置区域 / 响应方式</dt>
                    <dd>
                      {detail.data.attempt.region} / {detail.data.attempt.stream ? 'SSE' : 'JSON'}
                    </dd>
                    <dt>执行结果</dt>
                    <dd>{outcomes[detail.data.attempt.outcome]}</dd>
                    <dt>错误码</dt>
                    <dd>
                      <code>{detail.data.attempt.error_code || '无'}</code>
                    </dd>
                    <dt>上游 HTTP 状态</dt>
                    <dd>{detail.data.attempt.upstream_status ?? '未取得'}</dd>
                    <dt>输入 / 输出 token</dt>
                    <dd>
                      {detail.data.attempt.input_tokens ?? '未采集'} /{' '}
                      {detail.data.attempt.output_tokens ?? '未采集'}
                    </dd>
                    <dt>执行开始时间</dt>
                    <dd>{new Date(detail.data.attempt.started_at).toLocaleString('zh-CN')}</dd>
                    <dt>执行结束时间</dt>
                    <dd>
                      {detail.data.attempt.finished_at
                        ? new Date(detail.data.attempt.finished_at).toLocaleString('zh-CN')
                        : '未结束'}
                    </dd>
                    <dt>网关执行耗时</dt>
                    <dd>{duration(detail.data.attempt.duration_ms)}</dd>
                  </>
                ) : (
                  <>
                    <dt>执行元数据</dt>
                    <dd>未记录；不推测历史结果</dd>
                  </>
                )}
                {detail.data.cache_billing && (
                  <>
                    <dt>冻结输入 / 输出费率（微人民币/百万 token）</dt>
                    <dd>
                      {detail.data.cache_billing.input_per_million} /{' '}
                      {detail.data.cache_billing.output_per_million}
                    </dd>
                    <dt>审核缓存费率（微人民币/百万 token）</dt>
                    <dd>
                      读 {detail.data.cache_billing.read_per_million} / 写{' '}
                      {detail.data.cache_billing.write_per_million}
                    </dd>
                    <dt>回执输入总量 / 输出 token</dt>
                    <dd>
                      {detail.data.cache_billing.input_tokens ?? '未取得'} /{' '}
                      {detail.data.cache_billing.output_tokens ?? '未取得'}
                    </dd>
                    <dt>原始缓存读 / 写 token</dt>
                    <dd>
                      {detail.data.cache_billing.read_tokens ?? '未取得'} /{' '}
                      {detail.data.cache_billing.write_tokens ?? '未取得'}
                    </dd>
                  </>
                )}
                <dt>账本状态</dt>
                <dd>{states[detail.data.state]}</dd>
                <dt>预算周期 / 单位</dt>
                <dd>
                  {detail.data.period} / {detail.data.currency}
                </dd>
                <dt>价格版本</dt>
                <dd>{detail.data.price_version}</dd>
                <dt>预留 / 实际</dt>
                <dd>
                  {number(detail.data.reserved)} / {actual(detail.data)}
                </dd>
                <dt>创建时间</dt>
                <dd>
                  <time dateTime={detail.data.created_at}>
                    {new Date(detail.data.created_at).toLocaleString('zh-CN')}
                  </time>
                </dd>
                <dt>更新时间</dt>
                <dd>
                  <time dateTime={detail.data.updated_at}>
                    {new Date(detail.data.updated_at).toLocaleString('zh-CN')}
                  </time>
                </dd>
              </dl>
            )
          )}
        </section>
      )}
      {records.error && (
        <ErrorBox
          error={records.error}
          retry={() => {
            void records.refetch();
          }}
        />
      )}
      {records.isPending ? (
        <p role="status">正在加载调用账本…</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>创建时间</th>
                <th>请求编号</th>
                <th>模型 / 供应商</th>
                <th>执行结果</th>
                <th>执行耗时</th>
                <th>账本状态</th>
                <th>周期 / 单位</th>
                <th>预留 / 实际</th>
              </tr>
            </thead>
            <tbody>
              {items.map((record) => (
                <tr key={record.request_id}>
                  <td>{date(record.created_at)}</td>
                  <td>
                    <Button
                      variant="text"
                      onClick={() => {
                        update('request', record.request_id);
                      }}
                    >
                      {record.request_id}
                    </Button>
                  </td>
                  <td>
                    {record.attempt ? (
                      <>
                        {record.attempt.model}
                        <br />
                        <small>{record.attempt.provider}</small>
                      </>
                    ) : (
                      '未记录'
                    )}
                  </td>
                  <td>{record.attempt ? outcomes[record.attempt.outcome] : '未记录'}</td>
                  <td>{record.attempt ? duration(record.attempt.duration_ms) : '未记录'}</td>
                  <td>
                    <Tag
                      variant="light"
                      theme={record.state === 'uncertain' ? 'warning' : 'default'}
                    >
                      {states[record.state]}
                    </Tag>
                  </td>
                  <td>
                    {record.period} / {record.currency}
                  </td>
                  <td>
                    {number(record.reserved)} / {actual(record)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {items.length === 0 && !records.error && (
            <Empty
              title="暂无调用账本"
              description="请求建立预算预留后会显示在这里；鉴权或预算拒绝不会生成账本记录。"
            />
          )}
        </div>
      )}
      {records.hasNextPage && (
        <div className="load-more">
          <Button
            disabled={records.isFetchingNextPage}
            onClick={() => {
              void records.fetchNextPage();
            }}
          >
            加载更多调用
          </Button>
        </div>
      )}
    </section>
  );
}
