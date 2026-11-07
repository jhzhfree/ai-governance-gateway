import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button, Dialog } from 'tdesign-react';
import {
  api,
  type HostSnapshot,
  type HostNetwork,
  type HostIPv4,
  type HostChangeResult,
  type SystemEvent,
  type Page,
} from '../../api';
import { ErrorBox } from '../../shared/Feedback';
const bytes = (n: number) => `${(n / 1024 / 1024).toFixed(1)} MiB`;

export function SystemPanel({ identity, csrf }: { identity: string[]; csrf: string }) {
  const query = useQuery({
    queryKey: [...identity, 'system-host'],
    queryFn: ({ signal }) => api<HostSnapshot>('/api/v1/system', { signal }),
    refetchInterval: 5000,
  });
  const events = useQuery({
    queryKey: [...identity, 'system-events'],
    queryFn: ({ signal }) => api<Page<SystemEvent>>('/api/v1/system/events', { signal }),
    enabled: Boolean(query.data),
    refetchInterval: 10000,
  });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [reason, setReason] = useState('');
  const refresh = () => {
    void query.refetch();
    void events.refetch();
  };
  async function finish(confirm: boolean) {
    const current = query.data;
    if (!current?.pending || !reason.trim() || busy) return;
    setBusy(true);
    setError(null);
    setNotice('');
    try {
      const result = await api<HostChangeResult>(
        `/api/v1/system/network/${encodeURIComponent(current.pending.id)}/${confirm ? 'confirm' : 'rollback'}`,
        { method: 'POST', csrf, body: { node: current.node, reason } },
      );
      setNotice(result.state === 'confirmed' ? '网络配置已确认保存。' : '已核验回退完成。');
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
      refresh();
    }
  }
  if (query.error) return <ErrorBox error={query.error} retry={() => void query.refetch()} />;
  if (!query.data) return <p role="status">正在读取系统状态…</p>;
  const host = query.data;
  const m = host.metrics;
  return (
    <section className="panel system-panel">
      <h2>主机状态</h2>
      <p>
        节点：{host.node} · 每 5 秒刷新 · {new Date(m.sampled_at).toLocaleString()}
      </p>
      <p>操作系统当前命名空间的资源。容器部署时，网卡与 DNS 不代表宿主机配置。</p>
      <div className="metric-grid">
        <article className="metric-card">
          <h3>CPU 使用率</h3>
          <strong>{m.cpu_percent == null ? '首次采样中' : `${m.cpu_percent.toFixed(1)}%`}</strong>
          <p>
            {m.cpu_count} 个可见逻辑 CPU · 1 分钟负载 {m.load1.toFixed(2)}
          </p>
        </article>
        <article className="metric-card">
          <h3>内存</h3>
          <strong>
            {bytes(m.memory_total - m.memory_available)} / {bytes(m.memory_total)}
          </strong>
          <p>可用 {bytes(m.memory_available)}</p>
        </article>
        <article className="metric-card">
          <h3>容器限制</h3>
          <p>CPU 配额：{m.cgroup_cpu_quota ?? '未提供'} 核时间</p>
          <p>
            内存：{m.cgroup_memory_current == null ? '未知' : bytes(m.cgroup_memory_current)} /{' '}
            {m.cgroup_memory_limit == null ? '无限制或未提供' : bytes(m.cgroup_memory_limit)}
          </p>
        </article>
      </div>
      <h3>网卡状态</h3>
      <div className="table-scroll" role="region" aria-label="网卡状态表" tabIndex={0}>
        <table>
          <thead>
            <tr>
              <th>网卡</th>
              <th>状态 / MTU</th>
              <th>地址</th>
              <th>接收 / 发送累计</th>
            </tr>
          </thead>
          <tbody>
            {m.interfaces.map((link) => (
              <tr key={link.name}>
                <td>{link.name}</td>
                <td>
                  {link.up ? '启用' : '停用'} / {link.mtu}
                </td>
                <td>{link.addresses.join(', ') || '无地址'}</td>
                <td>
                  {bytes(link.rx_bytes)} / {bytes(link.tx_bytes)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p>当前解析器 DNS：{m.dns.join(', ') || '未读取到'}。本机缓存解析器地址不一定是上游 DNS。</p>
      <h3>网络设置</h3>
      {!host.network_enabled && (
        <p>网络修改未启用或暂不可用。请管理员检查本机代理、NetworkManager 和网卡允许列表。</p>
      )}
      <p>
        支持已连接的普通以太网接口：IPv4 DHCP／静态地址、网关与 DNS。不会修改 IPv6、Wi-Fi、桥接或
        Bond。临时应用后需在 90 秒内确认；未确认由 NetworkManager
        尝试回退。确认保存前请验证新地址连通性。
      </p>
      {host.pending ? (
        <div role="status">
          <h4>网络变更待处理</h4>
          <p>
            {host.pending.interface} · {host.pending.state} · 截止{' '}
            {new Date(host.pending.deadline).toLocaleString()}
          </p>
          <label>
            确认或回退原因
            <input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={256} />
          </label>
          <Button
            disabled={
              busy ||
              !reason.trim() ||
              host.pending.state !== 'pending' ||
              Date.now() > Date.parse(host.pending.deadline) - 15000
            }
            onClick={() => void finish(true)}
          >
            确认连通并保存
          </Button>
          <Button disabled={busy || !reason.trim()} onClick={() => void finish(false)}>
            回退并核验
          </Button>
        </div>
      ) : (
        host.network_enabled &&
        host.networks.map((network) => (
          <NetworkForm
            key={`${host.node}/${network.interface}/${network.revision}`}
            network={network}
            node={host.node}
            csrf={csrf}
            onComplete={refresh}
          />
        ))
      )}
      {notice && <p role="status">{notice}</p>}
      {error != null && (
        <>
          <ErrorBox error={error} />
          <p>操作结果可能未知，请刷新状态并核查操作记录，不要直接重复应用。</p>
        </>
      )}
      <h3>最近 100 条系统操作记录</h3>
      {events.error && <ErrorBox error={events.error} />}
      <div className="table-scroll" role="region" aria-label="系统操作记录表" tabIndex={0}>
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>节点</th>
              <th>操作</th>
              <th>结果</th>
              <th>操作者 / 原因</th>
            </tr>
          </thead>
          <tbody>
            {events.data?.data.map((event) => (
              <tr key={event.id}>
                <td>{new Date(event.created_at).toLocaleString()}</td>
                <td>{event.node}</td>
                <td>{event.action}</td>
                <td>{event.phase}</td>
                <td>
                  {event.actor.subject} / {event.reason}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
function NetworkForm({
  network,
  node,
  csrf,
  onComplete,
}: {
  network: HostNetwork;
  node: string;
  csrf: string;
  onComplete: () => void;
}) {
  const [method, setMethod] = useState(network.ipv4.method);
  const [addresses, setAddresses] = useState(network.ipv4.addresses.join(', '));
  const [gateway, setGateway] = useState(network.ipv4.gateway);
  const [dns, setDNS] = useState(network.ipv4.dns.join(', '));
  const [autoDNS, setAutoDNS] = useState(network.ipv4.automatic_dns);
  const [reason, setReason] = useState('');
  const [preview, setPreview] = useState<HostIPv4 | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const split = (s: string) => s.split(/[\s,]+/).filter(Boolean);
  function review(e: FormEvent) {
    e.preventDefault();
    setPreview({
      method,
      addresses: method === 'manual' ? split(addresses) : [],
      gateway: method === 'manual' ? gateway.trim() : '',
      dns: split(dns),
      automatic_dns: autoDNS,
    });
  }
  async function apply() {
    if (!preview || busy) return;
    setBusy(true);
    setError(null);
    try {
      await api<HostChangeResult>('/api/v1/system/network', {
        method: 'POST',
        csrf,
        body: {
          node,
          reason,
          change: {
            interface: network.interface,
            expected_revision: network.revision,
            ipv4: preview,
          },
        },
      });
      setPreview(null);
      onComplete();
    } catch (e) {
      setError(e);
      setPreview(null);
      onComplete();
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={review}>
      <h4>{network.interface}</h4>
      <label>
        IPv4 模式
        <select value={method} onChange={(e) => setMethod(e.target.value as 'auto' | 'manual')}>
          <option value="auto">DHCP 自动获取</option>
          <option value="manual">静态地址</option>
        </select>
      </label>
      {method === 'manual' && (
        <>
          <label>
            IPv4 地址／前缀
            <input
              required
              value={addresses}
              onChange={(e) => setAddresses(e.target.value)}
              placeholder="192.0.2.10/24"
            />
          </label>
          <label>
            IPv4 网关
            <input value={gateway} onChange={(e) => setGateway(e.target.value)} />
          </label>
        </>
      )}
      <label>
        IPv4 DNS（逗号分隔）
        <input value={dns} required={!autoDNS} onChange={(e) => setDNS(e.target.value)} />
      </label>
      <label className="system-checkbox">
        <input type="checkbox" checked={autoDNS} onChange={(e) => setAutoDNS(e.target.checked)} />
        接收 DHCP 提供的 DNS
      </label>
      <label>
        变更原因
        <input
          required
          maxLength={256}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
      <Button type="submit" disabled={busy}>
        预览网络变更
      </Button>
      {error != null && <ErrorBox error={error} />}
      <Dialog
        visible={preview != null}
        header={`临时应用 ${network.interface} 网络配置`}
        confirmBtn={{ content: '临时应用，等待连通确认', loading: busy }}
        onConfirm={() => void apply()}
        onClose={() => {
          if (!busy) setPreview(null);
        }}
      >
        <p>网络地址或 DNS 改动可能使当前连接中断。未在时限内确认时会尝试回退。</p>
        <h4>当前配置</h4>
        <pre>{JSON.stringify(network.ipv4, null, 2)}</pre>
        <h4>拟应用配置</h4>
        <pre>{JSON.stringify(preview, null, 2)}</pre>
      </Dialog>
    </form>
  );
}
