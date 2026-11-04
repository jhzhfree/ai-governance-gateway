import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import {
  api,
  type ContentPolicy,
  type ContentMeta,
  type ContentDetail,
  type ContentAuditor,
  type ContentAccess,
} from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox, Empty } from '../../shared/Feedback';

export function ContentAuditPanel({
  identity,
  projectPath,
  csrf,
  canManage = false,
  canGrant = false,
}: {
  identity: string[];
  projectPath: string;
  csrf: string;
  canManage?: boolean;
  canGrant?: boolean;
}) {
  const path = `${projectPath}/content-audit`;
  const policy = useQuery({
    queryKey: [...identity, path, 'policy'],
    enabled: canManage,
    queryFn: ({ signal }) => api<ContentPolicy>(`${path}/policy`, { signal }),
  });
  const records = usePage<ContentMeta>(identity, 'content-metadata', `${path}/records`, true);
  const auditors = usePage<ContentAuditor>(
    identity,
    'content-auditors',
    `${path}/auditors`,
    canManage,
  );
  const access = usePage<ContentAccess>(identity, 'content-access', `${path}/access`, canManage);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const [pending, setPending] = useState(false);
  const [requestID, setRequestID] = useState('');
  const [detail, setDetail] = useState<ContentDetail | null>(null);
  const generation = useRef(0);
  const request = useRef<AbortController | null>(null);
  function clear() {
    generation.current++;
    request.current?.abort();
    setDetail(null);
  }
  useEffect(() => {
    const abortCurrent = () => {
      generation.current++;
      request.current?.abort();
    };
    const hide = () => {
      if (document.hidden) {
        generation.current++;
        request.current?.abort();
        setDetail(null);
      }
    };
    document.addEventListener('visibilitychange', hide);
    return () => {
      abortCurrent();
      document.removeEventListener('visibilitychange', hide);
    };
  }, []);
  useEffect(() => {
    if (!detail) return;
    const timer = window.setTimeout(
      () => setDetail(null),
      Math.max(0, Math.min(60000, new Date(detail.meta.expires_at).getTime() - Date.now())),
    );
    return () => window.clearTimeout(timer);
  }, [detail]);
  async function savePolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!policy.data || pending) return;
    const form = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${path}/policy`, {
        method: 'PUT',
        csrf,
        body: {
          enabled: form.get('enabled') === 'on',
          retention_days: Number(form.get('days')),
          max_bytes: Number(form.get('bytes')),
          consent_ref: String(form.get('consent') ?? ''),
          expected_revision: policy.data.revision,
        },
      });
      setNotice('内容审计策略已保存，仅对后续请求生效。');
      await policy.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  async function grant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${path}/auditors`, {
        method: 'PUT',
        csrf,
        body: {
          issuer: form.get('issuer'),
          subject: form.get('subject'),
          active: form.get('active') === 'on',
        },
      });
      setNotice('独立内容审计授权已保存。');
      await auditors.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  async function reveal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    clear();
    const current = generation.current;
    const controller = new AbortController();
    request.current = controller;
    const form = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      const value = await api<ContentDetail>(
        `${path}/records/${encodeURIComponent(requestID)}/reveal`,
        { method: 'POST', csrf, signal: controller.signal, body: { reason: form.get('reason') } },
      );
      if (current === generation.current) setDetail(value);
      void access.refetch();
    } catch (e) {
      if (current === generation.current) setError(e);
    } finally {
      setPending(false);
    }
  }
  const rows = records.data?.pages.flatMap((page) => page.data) ?? [];
  return (
    <>
      {canManage && (
        <section className="panel">
          <div className="panel-heading">
            <div>
              <h2>内容采集策略</h2>
              <p>
                默认关闭。启用前须完成用途告知、授权及中国大陆处理与存储核验。管理员不会自动获得正文查看权。
              </p>
            </div>
            <Button
              onClick={() => {
                void policy.refetch();
              }}
            >
              刷新采集策略
            </Button>
          </div>
          {policy.error && <ErrorBox error={policy.error} />}
          {policy.data && (
            <form
              key={policy.data.revision}
              onSubmit={(event) => {
                void savePolicy(event);
              }}
            >
              {!policy.data.configured && <p>部署尚未配置内容审计加密密钥，无法启用新采集。</p>}
              <label>
                <input
                  name="enabled"
                  type="checkbox"
                  aria-label="启用内容审计采集"
                  defaultChecked={policy.data.enabled}
                  disabled={!policy.data.configured && !policy.data.enabled}
                />
                启用此项目的输入输出正文采集
              </label>{' '}
              <label>
                正文保留天数{' '}
                <input
                  name="days"
                  aria-label="正文保留天数"
                  type="number"
                  min={1}
                  max={90}
                  required
                  defaultValue={policy.data.retention_days}
                />
              </label>{' '}
              <label>
                每方向字节上限{' '}
                <input
                  name="bytes"
                  aria-label="每方向字节上限"
                  type="number"
                  min={1024}
                  max={262144}
                  required
                  defaultValue={policy.data.max_bytes}
                />
              </label>{' '}
              <label>
                告知与用途证据引用{' '}
                <input
                  name="consent"
                  aria-label="告知与用途证据引用"
                  maxLength={128}
                  defaultValue={policy.data.consent_ref}
                />
              </label>{' '}
              <Button type="submit" theme="primary" loading={pending}>
                保存采集策略
              </Button>
              <p>
                上限之外只保留已观察内容的哈希和截断标记。策略版本 {policy.data.revision}
                ；现有记录保留期不因修改而改变。
              </p>
            </form>
          )}
          {canGrant && (
            <form
              onSubmit={(event) => {
                void grant(event);
              }}
            >
              <h3>项目独立审计员授权</h3>
              <p>
                仅组织所有者可设置；此授权不赋予预算、密钥或项目管理权限。请向审计员提供包含组织和项目编号的专用访问地址。
              </p>
              <label>
                审计员签发方{' '}
                <input
                  name="issuer"
                  aria-label="审计员签发方"
                  defaultValue={identity[0]}
                  required
                  maxLength={2048}
                />
              </label>{' '}
              <label>
                审计员 subject{' '}
                <input name="subject" aria-label="审计员subject" required maxLength={256} />
              </label>{' '}
              <label>
                <input name="active" type="checkbox" aria-label="启用审计员授权" defaultChecked />
                启用授权（取消勾选后保存即撤销）
              </label>{' '}
              <Button type="submit" loading={pending}>
                保存审计员授权
              </Button>
            </form>
          )}
          {auditors.error && <ErrorBox error={auditors.error} />}
          <div className="table-scroll" role="region" aria-label="内容审计授权表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>审计员标识</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {auditors.data?.pages
                  .flatMap((page) => page.data)
                  .map((value) => (
                    <tr key={JSON.stringify([value.issuer, value.subject])}>
                      <td>
                        {value.subject}
                        <br />
                        {value.issuer}
                      </td>
                      <td>{value.active ? '已授权' : '已撤销'}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
          {auditors.hasNextPage && (
            <Button
              onClick={() => {
                void auditors.fetchNextPage();
              }}
            >
              加载更多审计员
            </Button>
          )}
        </section>
      )}
      <section className="panel">
        <div className="panel-heading">
          <div>
            <h2>内容审计记录</h2>
            <p>
              这里显示元数据。正文需显式填写查看理由；查看行为先留痕，页面隐藏或 60
              秒后自动清除当前正文展示。
            </p>
          </div>
          <Button
            onClick={() => {
              clear();
              void records.refetch();
            }}
          >
            刷新内容记录
          </Button>
        </div>
        {records.error && <ErrorBox error={records.error} />}
        {Boolean(error) && <ErrorBox error={error} />}
        {notice && <p role="status">{notice}</p>}
        {records.isPending && <p role="status">正在读取审计元数据…</p>}
        {!records.isPending && rows.length === 0 && (
          <Empty title="暂无内容审计记录" description="默认不采集正文；已过期记录不再显示。" />
        )}
        {rows.length > 0 && (
          <div className="table-scroll" role="region" aria-label="内容审计元数据表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>请求编号 / 模型</th>
                  <th>封存状态 / HTTP</th>
                  <th>策略版本 / 过期时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.request_id}>
                    <td>
                      {row.request_id}
                      <br />
                      {row.model}
                    </td>
                    <td>
                      {row.state} / {row.status}
                    </td>
                    <td>
                      {row.policy_revision} / {new Date(row.expires_at).toLocaleString()}
                    </td>
                    <td>
                      <Button
                        size="small"
                        onClick={() => {
                          clear();
                          setRequestID(row.request_id);
                        }}
                      >
                        选择内容 {row.request_id}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {records.hasNextPage && (
          <Button
            onClick={() => {
              void records.fetchNextPage();
            }}
          >
            加载更多内容记录
          </Button>
        )}
        <form
          onSubmit={(event) => {
            void reveal(event);
          }}
        >
          <h3>单条正文查看</h3>
          <label>
            审计请求编号{' '}
            <input
              aria-label="审计请求编号"
              required
              maxLength={256}
              value={requestID}
              onChange={(event) => {
                clear();
                setRequestID(event.target.value);
              }}
            />
          </label>{' '}
          <label>
            内容查看理由 <input name="reason" aria-label="内容查看理由" required maxLength={256} />
          </label>{' '}
          <Button type="submit" theme="primary" loading={pending}>
            留痕并查看正文
          </Button>
          <Button variant="text" onClick={clear}>
            隐藏正文
          </Button>
        </form>
        {detail && (
          <section role="region" aria-label="审计正文详情">
            <h3>审计正文详情</h3>
            <p>
              {detail.meta.request_id} · {detail.meta.state}
              。封存状态不等同执行成功、响应送达或费用已结算。
            </p>
            {[
              ['请求', detail.request],
              ['响应', detail.response],
            ].map(([label, value]) =>
              typeof value === 'object' && value ? (
                <div key={String(label)}>
                  <h4>{String(label)}内容</h4>
                  <p>
                    已观察 {value.observed_bytes} 字节，保存 {value.captured_bytes} 字节；
                    {value.truncated ? '已截断，仅为前缀' : '未截断'}。
                  </p>
                  <p style={{ overflowWrap: 'anywhere' }}>已观察内容 SHA-256：{value.sha256}</p>
                  <pre
                    style={{
                      whiteSpace: 'pre-wrap',
                      overflowWrap: 'anywhere',
                      maxHeight: 400,
                      overflow: 'auto',
                    }}
                    tabIndex={0}
                    aria-label={`${String(label)}审计正文`}
                  >
                    {value.content}
                  </pre>
                </div>
              ) : (
                <p key={String(label)}>响应尚未封存。</p>
              ),
            )}
          </section>
        )}
      </section>
      {canManage && (
        <section className="panel">
          <h2>正文访问留痕</h2>
          {access.error && <ErrorBox error={access.error} />}
          <div className="table-scroll" role="region" aria-label="正文访问留痕表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>查看人</th>
                  <th>请求编号</th>
                  <th>理由</th>
                  <th>时间</th>
                </tr>
              </thead>
              <tbody>
                {access.data?.pages
                  .flatMap((page) => page.data)
                  .map((row) => (
                    <tr key={row.id}>
                      <td>{row.actor.subject}</td>
                      <td>{row.request_id}</td>
                      <td>{row.reason}</td>
                      <td>{new Date(row.created_at).toLocaleString()}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
          {access.hasNextPage && (
            <Button
              onClick={() => {
                void access.fetchNextPage();
              }}
            >
              加载更多访问留痕
            </Button>
          )}
        </section>
      )}
    </>
  );
}
