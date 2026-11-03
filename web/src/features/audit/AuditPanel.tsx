import { Button, Tag } from 'tdesign-react';
import type { Audit } from '../../api';
import { ErrorBox, Empty } from '../../shared/Feedback';
import { actionNames, date } from '../../shared/format';
export function AuditPanel({
  items,
  error,
  loading,
  hasMore,
  onMore,
}: {
  items: Audit[];
  error: unknown;
  loading: boolean;
  hasMore: boolean;
  onMore: () => void;
}) {
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <h2>组织变更记录</h2>
          <p>操作者、动作与变更时间</p>
        </div>
        <Tag variant="outline">仅含治理元数据</Tag>
      </div>
      {error ? (
        <ErrorBox error={error} />
      ) : loading ? (
        <div className="loading" role="status">
          正在加载记录…
        </div>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>操作</th>
                <th>操作者</th>
                <th>资源</th>
                <th>时间</th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => (
                <tr key={a.id}>
                  <td>{actionNames[a.action] ?? a.action}</td>
                  <td>{a.actor.subject}</td>
                  <td>
                    <code>{a.resource_id}</code>
                  </td>
                  <td>{date(a.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {items.length === 0 && (
            <Empty
              title="暂无变更记录"
              description="创建项目、密钥或调整配置后，记录会显示在这里。"
            />
          )}
        </div>
      )}
      {hasMore && (
        <div className="load-more">
          <Button
            onClick={() => {
              onMore();
            }}
          >
            加载更多
          </Button>
        </div>
      )}
    </section>
  );
}
