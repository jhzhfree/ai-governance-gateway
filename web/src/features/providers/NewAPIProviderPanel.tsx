import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import {
  api,
  type NewAPIChannel,
  type NewAPIChannelPage,
  type NewAPIChannelOperation,
  type NewAPIChannelOperations,
  type ProviderConfigurationStatus,
} from '../../api';
import { ErrorBox } from '../../shared/Feedback';
import { ProviderEditor } from './ProviderPanel';

const kinds: Record<number, string> = { 1: 'OpenAI 兼容', 43: 'DeepSeek', 14: 'Anthropic' };
const states = { pending: '结果待核查', applied: '已保存', rejected: '被拒绝，请核对配置与版本' };

export function NewAPIProviderPanel({
  status,
  identity,
  csrf,
}: {
  status: ProviderConfigurationStatus;
  identity: string[];
  csrf: string;
}) {
  const client = useQueryClient();
  const [cursor, setCursor] = useState('');
  const [editing, setEditing] = useState<NewAPIChannel | null | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const channels = useQuery({
    queryKey: [...identity, 'newapi-channels', cursor],
    enabled: !!status.can_manage_channels,
    queryFn: ({ signal }) =>
      api<NewAPIChannelPage>(`/api/v1/newapi/channels${cursor ? `?after=${cursor}` : ''}`, {
        signal,
      }),
  });
  const operations = useQuery({
    queryKey: [...identity, 'newapi-channel-operations'],
    enabled: !!status.can_manage_channels,
    queryFn: ({ signal }) =>
      api<NewAPIChannelOperations>('/api/v1/newapi/channel-operations', { signal }),
  });
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: identity });
  };
  const recover = async (id: string) => {
    setError(null);
    try {
      await api(`/api/v1/newapi/channel-operations/${id}/recover`, { method: 'POST', csrf });
      await refresh();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>供应商渠道</h2>
            <p>统一管理 new-api 渠道。供应商密钥只写入数据面，不回显；保存不会调用模型。</p>
          </div>
          <Button
            onClick={() => {
              void refresh();
            }}
          >
            刷新渠道
          </Button>
        </div>
        {!status.can_manage_channels ? (
          <p>需要指定的平台配置管理员及独立的后台渠道管理凭据。请联系部署管理员完成配置。</p>
        ) : (
          <>
            <Button onClick={() => setEditing(null)}>新增渠道</Button>
            {channels.error && <ErrorBox error={channels.error} />}
            {channels.isPending && <p role="status">正在读取渠道…</p>}
            <div className="table-scroll" role="region" aria-label="供应商渠道表" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th>渠道</th>
                    <th>协议与端点</th>
                    <th>模型</th>
                    <th>状态与审核</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {channels.data?.data.map((channel) => {
                    const bound =
                      status.configuration?.models.filter(
                        (m) => m.newapi_channel_id === channel.id,
                      ) ?? [];
                    const drift = bound.some((m) => m.newapi_channel_revision !== channel.revision);
                    return (
                      <tr key={channel.id}>
                        <td>
                          {channel.name}
                          <br />#{channel.id}
                        </td>
                        <td>
                          {kinds[channel.type] ?? `类型 ${channel.type}`}
                          <br />
                          {channel.base_url || '端点不可展示'}
                        </td>
                        <td>{channel.models.join(', ')}</td>
                        <td>
                          {channel.enabled ? '已启用' : '已停用'} ·{' '}
                          {channel.credential_configured
                            ? `${channel.key_count} 个凭据`
                            : '未配置凭据'}
                          <br />
                          {drift
                            ? '配置已变化，须重新审核'
                            : bound.length
                              ? '与已发布目录一致'
                              : '尚未授权到企业模型目录'}
                        </td>
                        <td>
                          <Button disabled={!channel.editable} onClick={() => setEditing(channel)}>
                            编辑渠道
                          </Button>
                          {!channel.editable && <p>高级配置或协议暂不支持在此编辑。</p>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {cursor && (
              <Button variant="outline" onClick={() => setCursor('')}>
                回到首批渠道
              </Button>
            )}
            {channels.data?.next_cursor && (
              <Button variant="outline" onClick={() => setCursor(channels.data!.next_cursor)}>
                下一批渠道
              </Button>
            )}
            <p>
              修改正在使用的渠道会使旧审核指纹失效。建议新建替代渠道，审核并切换模型目录后，再停用旧渠道。
            </p>
          </>
        )}
      </section>
      {editing !== undefined && (
        <ChannelEditor
          key={editing?.edit_revision ?? 'new'}
          channel={editing}
          csrf={csrf}
          onClose={() => setEditing(undefined)}
          onSaved={refresh}
        />
      )}
      {status.can_manage && (
        <ProviderEditor
          key={status.revision}
          status={status}
          csrf={csrf}
          channels={channels.data?.data ?? []}
        />
      )}
      {!status.managed && (
        <section className="panel">
          <p>
            当前模型目录来自启动配置文件，渠道可在此管理。启用数据库目录管理后，可在同一页面审核发布模型；切换前须核对并导入原目录。
          </p>
        </section>
      )}
      {status.can_manage_channels && (
        <section className="panel governance-panel">
          <h2>渠道变更记录</h2>
          <p>最近 50 条操作。待核查表示远端结果尚未确认，请先查询回执，不要重新创建渠道。</p>
          {Boolean(error) && <ErrorBox error={error} />}{' '}
          {operations.error && <ErrorBox error={operations.error} />}
          <div className="table-scroll" role="region" aria-label="渠道变更记录表" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th>操作编号 / 渠道</th>
                  <th>操作者</th>
                  <th>状态</th>
                  <th>核查</th>
                </tr>
              </thead>
              <tbody>
                {operations.data?.data.map((op) => (
                  <tr key={op.id}>
                    <td>
                      <code>{op.id}</code>
                      <br />#{op.channel_id || '待确认'}
                    </td>
                    <td>{op.actor_subject}</td>
                    <td>{states[op.state]}</td>
                    <td>
                      {op.state === 'pending' && (
                        <Button
                          onClick={() => {
                            void recover(op.id);
                          }}
                        >
                          查询保存回执
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
      <section className="panel">
        <h2>应用接入</h2>
        <p>
          发布审核模型 → 设置项目预算与模型授权 →
          签发项目密钥。应用填写网关地址、项目密钥和模型别名。支持已审核的
          Chat、Embeddings、Responses、Messages 子集，具体能力由目录与渠道共同限制。
        </p>
      </section>
    </>
  );
}

function ChannelEditor({
  channel,
  csrf,
  onClose,
  onSaved,
}: {
  channel: NewAPIChannel | null;
  csrf: string;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [name, setName] = useState(channel?.name ?? '');
  const [type, setType] = useState(channel?.type ?? 43);
  const [base, setBase] = useState(channel?.base_url ?? 'https://api.deepseek.com');
  const [models, setModels] = useState(channel?.models.join('\n') ?? '');
  const [enabled, setEnabled] = useState(channel?.enabled ?? false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<NewAPIChannelOperation | null>(null);
  const [operationID] = useState(() => crypto.randomUUID());
  const [submitted, setSubmitted] = useState(false);
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError(null);
    setPending(true);
    setSubmitted(true);
    const form = event.currentTarget;
    const input = form.elements.namedItem('supplier-key') as HTMLInputElement;
    const secret = input.value;
    input.value = '';
    try {
      const operation = await api<NewAPIChannelOperation>(
        `/api/v1/newapi/channels${channel ? `/${channel.id}` : ''}`,
        {
          method: channel ? 'PUT' : 'POST',
          csrf,
          body: {
            operation_id: operationID,
            expected_revision: channel?.edit_revision ?? '',
            name,
            type,
            base_url: base,
            models: models
              .split(/[\n,]/)
              .map((s) => s.trim())
              .filter(Boolean),
            enabled,
            ...(secret
              ? {
                  api_key: secret
                    .split(',')
                    .map((key) => key.trim())
                    .join('\n'),
                }
              : {}),
          },
        },
      );
      setResult(operation);
      await onSaved();
    } catch (e) {
      setError(e);
      await onSaved();
    } finally {
      setPending(false);
    }
  };
  return (
    <section className="panel governance-panel">
      <h2>{channel ? '编辑渠道' : '新增供应商渠道'}</h2>
      {Boolean(error) && <ErrorBox error={error} />}{' '}
      {submitted && !result && (
        <p>操作编号：{operationID}。若保存结果未知，请先刷新变更记录并查询回执。</p>
      )}
      {result && (
        <p role="status">
          {states[result.state]}。操作编号：{result.id}。保存渠道不会自动更新企业模型审核。
        </p>
      )}
      <form
        onSubmit={(e) => {
          void save(e);
        }}
      >
        <label>
          渠道名称
          <input
            aria-label="渠道名称"
            required
            maxLength={100}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label>
          渠道协议
          <select
            aria-label="渠道协议"
            value={type}
            onChange={(e) => setType(Number(e.target.value))}
          >
            {Object.entries(kinds).map(([id, label]) => (
              <option key={id} value={id}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label>
          供应商 Base URL
          <input
            aria-label="渠道 Base URL"
            type="url"
            required
            maxLength={2048}
            value={base}
            onChange={(e) => setBase(e.target.value)}
          />
        </label>
        <p>端点必须属于部署批准的目标地址。改变端点前需重新核对凭证所属账号和数据驻留要求。</p>
        <label>
          上游模型列表
          <textarea
            aria-label="渠道模型列表"
            required
            value={models}
            onChange={(e) => setModels(e.target.value)}
            placeholder="每行一个实际模型 ID"
          />
        </label>
        <label>
          供应商密钥
          <input
            type="password"
            aria-label="渠道供应商密钥"
            name="supplier-key"
            autoComplete="off"
            spellCheck={false}
            required={!channel}
            maxLength={65536}
          />
        </label>
        <p>
          {channel ? '留空保留原凭据；填写则替换整个密钥列表。' : '请填写供应商凭据。'}多 Key
          以逗号分隔，统一轮询；不会展示已存密钥。
        </p>
        <label>
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          启用渠道
        </label>
        <Button type="submit" loading={pending} disabled={pending || submitted}>
          保存渠道
        </Button>{' '}
        <Button variant="outline" disabled={pending} onClick={onClose}>
          关闭编辑
        </Button>
      </form>
    </section>
  );
}
