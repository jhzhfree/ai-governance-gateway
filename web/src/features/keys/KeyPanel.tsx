import { Button, Tag } from 'tdesign-react';
import type { ProjectKey } from '../../api';
import { ErrorBox, Empty } from '../../shared/Feedback';
import { date } from '../../shared/format';
export function KeyPanel({
  items,
  error,
  loading,
  hasMore,
  canIssue,
  onCreate,
  onRetry,
  onMore,
  onRevoke,
}: {
  items: ProjectKey[];
  error: unknown;
  loading: boolean;
  hasMore: boolean;
  canIssue: boolean;
  onCreate: () => void;
  onRetry: () => void;
  onMore: () => void;
  onRevoke: (key: ProjectKey) => void;
}) {
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <h2>
            访问密钥 <span className="count">{items.length}</span>
          </h2>
          <p>凭证仅用于当前项目的模型调用，完整密钥只展示一次。</p>
        </div>
        <Button theme="primary" disabled={!canIssue} onClick={() => onCreate()}>
          ＋ 创建密钥
        </Button>
      </div>
      {error ? (
        <ErrorBox
          error={error}
          retry={() => {
            onRetry();
          }}
        />
      ) : loading ? (
        <div className="loading" role="status">
          正在加载密钥…
        </div>
      ) : items.length === 0 ? (
        <Empty title="还没有访问密钥" description="创建项目密钥，让你的应用安全接入已授权模型。" />
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>名称 / 密钥前缀</th>
                <th>模型</th>
                <th>状态</th>
                <th>有效期至</th>
                <th className="align-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {items.map((k) => {
                const expired = new Date(k.expires_at).getTime() <= Date.now();
                return (
                  <tr key={k.id}>
                    <td>
                      <strong>{k.name}</strong>
                      <code>{k.prefix}…</code>
                    </td>
                    <td>{k.models.join(', ')}</td>
                    <td>
                      <Tag
                        theme={k.revoked_at ? 'default' : expired ? 'warning' : 'success'}
                        variant="light"
                      >
                        {k.revoked_at ? '已撤销' : expired ? '已过期' : '有效'}
                      </Tag>
                    </td>
                    <td>{date(k.expires_at)}</td>
                    <td className="align-right">
                      {!k.revoked_at && k.can_revoke ? (
                        <Button
                          variant="text"
                          theme="danger"
                          size="small"
                          onClick={() => {
                            onRevoke(k);
                          }}
                        >
                          撤销<span className="sr-only"> {k.name}</span>
                        </Button>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {hasMore && (
        <div className="load-more">
          <Button
            onClick={() => {
              onMore();
            }}
          >
            加载更多密钥
          </Button>
        </div>
      )}
    </section>
  );
}
