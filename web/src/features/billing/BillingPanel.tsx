import { useState, type FormEvent } from 'react';
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type BillingPage, type BillingRecord, type ImportReceipt } from '../../api';
import { ErrorBox, Empty } from '../../shared/Feedback';

const issues: Record<string, string> = {
  missing_request: '未关联请求',
  duplicate_request: '同一请求存在多条账单',
  provider_unrecorded: '缺少供应商元数据',
  provider_mismatch: '供应商不符',
  scope_mismatch: '周期或币种不符',
  usage_mismatch: '用量有差异',
  amount_mismatch: '金额有差异',
  over_bound: '超出预留上界',
  unsettled: '待结算',
  active_execution: '执行租约有效',
  recent_execution: '更新时间不足5分钟',
  unsupported_state: '账本状态不支持',
};
export function BillingPanel({
  identity,
  projectPath,
  canManage,
  csrf,
}: {
  identity: string[];
  projectPath: string;
  canManage: boolean;
  csrf: string;
}) {
  const client = useQueryClient();
  const [decision, setDecision] = useState('');
  const [csv, setCSV] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<BillingRecord | null>(null);
  const [action, setAction] = useState('approve');
  const list = useInfiniteQuery({
    queryKey: [...identity, projectPath, 'billing', decision],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) =>
      api<BillingPage>(
        `${projectPath}/billing/records?limit=20&after=${encodeURIComponent(pageParam)}${decision ? `&decision=${decision}` : ''}`,
        { signal },
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const records = list.data?.pages.flatMap((page) => page.data) ?? [];
  async function importCSV(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      const v = await api<ImportReceipt>(`${projectPath}/billing/imports`, {
        method: 'POST',
        csrf,
        body: {
          provider: data.get('provider'),
          account_ref: data.get('account'),
          evidence_ref: data.get('evidence'),
          csv,
        },
      });
      setNotice(
        v.duplicate ? '相同账单已导入，未重复写入。' : `已导入 ${v.count} 条账单，预算未变更。`,
      );
      await list.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  async function review(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected || pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api(`${projectPath}/billing/records/${encodeURIComponent(selected.id)}/review`, {
        method: 'POST',
        csrf,
        body: {
          action,
          request_id: String(data.get('request_id') ?? ''),
          expected_revision: selected.revision,
          confirmed_final: data.get('final') === 'on',
          reason: data.get('reason'),
        },
      });
      setNotice(
        action === 'approve' ? '审核通过，财务处理与审计已提交。' : '账单记录已驳回，预算未变更。',
      );
      setSelected(null);
      await client.invalidateQueries({
        predicate: (q) => q.queryKey[0] === identity[0] && q.queryKey[1] === identity[1],
      });
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <h2>账单对账工作台</h2>
          <p>
            标准化 CSV
            供人工核对，不代表官方账单已验真。导入不改变余额；通过财务审核须另一位组织管理员确认最终证据。
          </p>
        </div>
        <Button
          loading={list.isFetching}
          onClick={() => {
            void list.refetch();
          }}
        >
          刷新账单
        </Button>
      </div>
      {canManage && (
        <form
          onSubmit={(event) => {
            void importCSV(event);
          }}
        >
          <label>
            账单供应商{' '}
            <select name="provider" aria-label="账单供应商">
              <option value="aliyun">阿里云百炼</option>
              <option value="volcengine">火山方舟</option>
              <option value="zhipu">智谱</option>
              <option value="deepseek">DeepSeek</option>
            </select>
          </label>{' '}
          <label>
            供应商账户引用{' '}
            <input name="account" aria-label="供应商账户引用" required maxLength={128} />
          </label>{' '}
          <label>
            证据引用 <input name="evidence" aria-label="证据引用" required maxLength={128} />
          </label>
          <p>
            账户和证据使用内部引用编号，不填写密钥或正文。最多 100 行 / 24 KiB；amount 为 CNY_MICRO
            整数，1 元 = 1000000 单位。
          </p>
          <label>
            选择标准 CSV{' '}
            <input
              aria-label="选择标准CSV"
              type="file"
              accept=".csv,text/csv"
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (!file) return;
                if (file.size > 24576) {
                  setError(new Error('CSV 不能超过 24 KiB。'));
                  return;
                }
                void file
                  .text()
                  .then((text) => setCSV(text))
                  .catch(() => setError(new Error('文件读取失败。')));
              }}
            />
          </label>
          <label>
            标准账单 CSV{' '}
            <textarea
              aria-label="标准账单CSV"
              rows={5}
              required
              maxLength={24576}
              value={csv}
              onChange={(event) => setCSV(event.target.value)}
              style={{ width: '100%', maxWidth: '100%' }}
              placeholder="record_id,request_id,period,currency,amount,input_tokens,output_tokens"
            />
          </label>
          <Button type="submit" loading={pending}>
            导入标准账单
          </Button>
        </form>
      )}
      {Boolean(error) && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      <label>
        审核状态{' '}
        <select
          aria-label="账单审核状态"
          value={decision}
          onChange={(event) => setDecision(event.target.value)}
        >
          <option value="">全部</option>
          <option value="pending">待审核</option>
          <option value="approved">已通过</option>
          <option value="rejected">已驳回</option>
        </select>
      </label>
      {list.error && <ErrorBox error={list.error} />}
      {list.isPending && <p role="status">正在读取账单…</p>}
      {!list.isPending && records.length === 0 && (
        <Empty
          title="暂无账单记录"
          description="导入标准化账单后，可查看关联差异并发起人工审核。"
        />
      )}
      {records.length > 0 && (
        <div className="table-scroll" role="region" aria-label="项目账单对账表" tabIndex={0}>
          <table>
            <thead>
              <tr>
                <th>供应商 / 记录</th>
                <th>建议 / 已关联请求</th>
                <th>周期 / 金额</th>
                <th>账本实际 / 预留</th>
                <th>差异 / 审核</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {records.map((record) => (
                <tr key={record.id}>
                  <td>
                    {record.provider} / {record.record_id}
                  </td>
                  <td>{record.resolved_request_id ?? record.request_id ?? '未关联'}</td>
                  <td>
                    {record.period} / {record.amount} {record.currency}
                  </td>
                  <td>
                    {record.actual ?? '待结算'} / {record.reserved ?? '未关联'}
                  </td>
                  <td>
                    {record.issues.map((issue) => issues[issue]).join('、') || '金额匹配'} ·{' '}
                    {{ pending: '待审核', approved: '已通过', rejected: '已驳回' }[record.decision]}
                  </td>
                  <td>
                    <Button
                      size="small"
                      onClick={() => {
                        setSelected(record);
                        setAction(
                          record.importer.issuer === identity[0] &&
                            record.importer.subject === identity[1]
                            ? 'reject'
                            : 'approve',
                        );
                        setError(null);
                      }}
                    >
                      查看或审核 {record.record_id}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {list.hasNextPage && (
        <Button
          loading={list.isFetchingNextPage}
          onClick={() => {
            void list.fetchNextPage();
          }}
        >
          加载更多账单
        </Button>
      )}
      {selected && (
        <section role="region" aria-label="账单审核详情">
          <h3>账单审核详情：{selected.record_id}</h3>
          <p>
            导入人：{selected.importer.subject} · 证据引用：{selected.evidence_ref} · 用量：
            {selected.input_tokens} / {selected.output_tokens}
          </p>
          <p>
            审核人：{selected.reviewer?.subject ?? '待审核'} · 原因：{selected.reason ?? '尚未填写'}{' '}
            · 版本：{selected.revision}
          </p>
          {canManage && selected.decision === 'pending' && (
            <form
              key={selected.id}
              onSubmit={(event) => {
                void review(event);
              }}
            >
              <label>
                审核动作{' '}
                <select
                  aria-label="审核动作"
                  value={action}
                  onChange={(event) => setAction(event.target.value)}
                >
                  <option
                    value="approve"
                    disabled={
                      selected.importer.issuer === identity[0] &&
                      selected.importer.subject === identity[1]
                    }
                  >
                    通过并核对财务
                  </option>
                  <option value="reject">驳回，不改预算</option>
                </select>
              </label>
              <p>
                导入人不能通过自己的账单。未结请求须更新时间超过 5
                分钟且无有效租约；超过预留或已结算金额不符的记录暂不支持调整。
              </p>
              <label>
                核对后的网关请求编号{' '}
                <input
                  name="request_id"
                  aria-label="核对后的网关请求编号"
                  defaultValue={selected.request_id}
                  required={action === 'approve'}
                  maxLength={128}
                />
              </label>{' '}
              <label>
                审核原因 <input name="reason" aria-label="审核原因" required maxLength={256} />
              </label>{' '}
              <label>
                <input name="final" type="checkbox" required={action === 'approve'} />
                我已核对供应商最终账单与请求归属，确认金额和最终费用证据
              </label>{' '}
              <Button type="submit" theme="primary" loading={pending}>
                提交账单审核
              </Button>
            </form>
          )}
          <Button variant="text" onClick={() => setSelected(null)}>
            关闭账单详情
          </Button>
        </section>
      )}
    </section>
  );
}
