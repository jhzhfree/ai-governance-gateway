import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, ApiError, type CostReport } from '../../api';
import { ErrorBox } from '../../shared/Feedback';

function day(offset: number) {
  const value = new Date();
  value.setUTCDate(value.getUTCDate() + offset);
  return value.toISOString().slice(0, 10);
}

export function CostReportPanel({
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
  const [from, setFrom] = useState(() => day(-6));
  const [to, setTo] = useState(() => day(1));
  const [organizationScope, setOrganizationScope] = useState(false);
  const [departmentID, setDepartmentID] = useState('');
  const [departmentState, setDepartmentState] = useState('');
  const [filter, setFilter] = useState({
    from,
    to,
    organizationScope,
    departmentID,
    departmentState,
  });
  const [pending, setPending] = useState(false);
  const [exportError, setExportError] = useState<unknown>(null);
  const path = filter.organizationScope && canManage ? orgPath : projectPath;
  const search = new URLSearchParams({ from: filter.from, to: filter.to });
  if (filter.departmentID) search.set('department_id', filter.departmentID);
  if (filter.departmentState) search.set('department_state', filter.departmentState);
  const endpoint = `${path}/cost-report?${search}`;
  const query = useQuery({
    queryKey: [
      ...identity,
      path,
      'cost-report',
      filter.from,
      filter.to,
      filter.departmentID,
      filter.departmentState,
    ],
    enabled: Boolean(path),
    queryFn: ({ signal }) => api<CostReport>(endpoint, { signal }),
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setExportError(null);
    setFilter({ from, to, organizationScope, departmentID: departmentID.trim(), departmentState });
  }
  async function download() {
    if (!path) return;
    setPending(true);
    setExportError(null);
    try {
      const response = await fetch(`${path}/cost-report/export?${search}`, {
        method: 'POST',
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        headers: { 'X-CSRF-Token': csrf },
      });
      if (!response.ok) {
        if (response.status === 401) window.dispatchEvent(new Event('aigw:session-expired'));
        throw new ApiError(response.status, response.headers.get('X-Request-ID') ?? '');
      }
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement('a');
      link.href = url;
      link.download = 'aigw-cost-report.csv';
      document.body.append(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      setExportError(error);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>成本报表</h2>
          <p>按请求进入账本的 UTC 日期汇总。晚到结算会更新历史结果，不等同于供应商官方账单。</p>
        </div>
      </div>
      <form className="cost-report-filters" onSubmit={submit}>
        <label>
          起始日期（UTC，含）
          <input type="date" required value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label>
          结束日期（UTC，不含）
          <input type="date" required value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label>
          部门 ID（含历史下级）
          <input
            maxLength={80}
            list="cost-department-snapshots"
            value={departmentID}
            onChange={(e) => setDepartmentID(e.target.value)}
          />
        </label>
        <datalist id="cost-department-snapshots">
          {Array.from(
            new Map(
              query.data?.rows.flatMap((r) => r.department_path).map((d) => [d.id, d]) ?? [],
            ).values(),
          ).map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </datalist>
        <label>
          部门归属状态
          <select
            aria-label="部门归属状态"
            value={departmentState}
            onChange={(e) => setDepartmentState(e.target.value)}
          >
            <option value="">全部归属状态</option>
            <option value="assigned">入账时已分配</option>
            <option value="unassigned">入账时未分配</option>
            <option value="historical">历史未记录</option>
          </select>
        </label>
        {canManage && (
          <label>
            <input
              type="checkbox"
              checked={organizationScope}
              onChange={(e) => setOrganizationScope(e.target.checked)}
            />
            汇总整个组织
          </label>
        )}
        <Button tag="button" type="submit" disabled={!projectPath && !organizationScope}>
          查询成本报表
        </Button>
        <Button
          tag="button"
          disabled={!query.data || query.isFetching}
          loading={pending}
          onClick={() => {
            void download();
          }}
        >
          导出成本 CSV
        </Button>
      </form>
      <p>
        每次最多查询 90 天、1000
        个分组，超限会拒绝，请缩短日期或选择单个项目。金额为最小账本单位，CNY_MICRO 的 1,000,000
        单位为人民币 1 元。币种分别统计。
      </p>
      {query.error && <ErrorBox error={query.error} />}
      {exportError != null && <ErrorBox error={exportError} />}
      {query.isFetching && <p role="status">正在读取账本快照…</p>}
      {query.data && (
        <>
          <p>
            观察时间：{query.data.observed_at} · {filter.from} 至 {filter.to}（不含） ·{' '}
            {filter.organizationScope && canManage ? '整个组织' : '当前项目'}
          </p>
          <p>
            部门按入账时路径快照展示，后续移动/重命名不改旧账；历史未记录与入账时未分配分别统计。部门
            ID 包含当时的下级项目，不扩大当前项目访问权限。 已结算、估算终结和开放占额分列。缺少完整
            usage 的请求不计入已知 token
            合计；未知归属表示未留下执行记录。鉴权或预算拒绝且未占额的请求不在本报表中。
          </p>
          <div className="table-scroll" role="region" aria-label="成本明细表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  {[
                    '日期',
                    '项目',
                    '入账时部门',
                    '模型 / 供应商',
                    '币种',
                    '入账请求',
                    '已结算',
                    '估算终结',
                    '开放占额',
                    '已释放笔数',
                    '已知用量覆盖',
                    '已知输入 token',
                    '已知输出 token',
                  ].map((label) => (
                    <th key={label}>{label}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {query.data.rows.map((row, i) => (
                  <tr key={i}>
                    <td>{row.day}</td>
                    <td>{row.project_id}</td>
                    <td>
                      {row.department_state === 'historical'
                        ? '历史未记录'
                        : row.department_state === 'unassigned'
                          ? '入账时未分配'
                          : row.department_path.map((d) => `${d.name} (${d.id})`).join(' / ')}
                    </td>
                    <td>
                      {row.model ?? '未知归属'} / {row.provider ?? '未知归属'}
                    </td>
                    <td>{row.currency}</td>
                    <td>{row.admitted}</td>
                    <td>{row.settled_amount}</td>
                    <td>{row.estimated_amount}</td>
                    <td>{row.open_reserved}</td>
                    <td>{row.released}</td>
                    <td>
                      {row.usage_known} / {row.admitted}
                    </td>
                    <td>{row.usage_known === 0 ? '未知' : row.input_tokens}</td>
                    <td>{row.usage_known === 0 ? '未知' : row.output_tokens}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {query.data.rows.length === 0 && <p>所选时间范围没有账本记录。</p>}
        </>
      )}
    </section>
  );
}
