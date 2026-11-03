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
  const id = params.get('request') ?? '';
  const records = usePage<RequestRecord>(
    identity,
    'requests',
    `${projectPath}/requests?state=${encodeURIComponent(state)}`,
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
        账本状态用于核对费用，不代表响应已送达客户端。待核对请求继续占用预留，不自动退款。
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
        <form key={id} onSubmit={search}>
          <label htmlFor="request-search">请求编号</label>
          <input
            id="request-search"
            name="request"
            defaultValue={id}
            maxLength={256}
            placeholder="输入完整 request_id"
          />
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
