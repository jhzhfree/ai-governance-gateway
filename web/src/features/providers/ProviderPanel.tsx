import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Tag } from 'tdesign-react';
import { api, type ProviderConfiguration, type ProviderConfigurationStatus } from '../../api';
import { ErrorBox } from '../../shared/Feedback';

const names: Record<string, string> = {
  aliyun: '阿里云百炼',
  volcengine: '火山方舟',
  zhipu: '智谱',
};
const emptyResidency = () => ({ region: 'cn-mainland' as const, evidence_ref: '', expires_at: '' });
export function ProviderPanel({ identity, csrf }: { identity: string[]; csrf: string }) {
  const query = useQuery({
    queryKey: [...identity, 'providers'],
    queryFn: ({ signal }) => api<ProviderConfigurationStatus>('/api/v1/providers', { signal }),
  });
  return (
    <>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>真实供应商配置</h2>
            <p>
              平台级路由供所有组织使用，项目模型白名单继续独立生效。保存配置不会调用模型或产生推理费用。
            </p>
          </div>
          <Button
            onClick={() => {
              void query.refetch();
            }}
          >
            刷新供应商
          </Button>
        </div>
        {query.error && <ErrorBox error={query.error} />}
        {query.isPending && <p role="status">正在读取供应商配置…</p>}
        {query.data && (
          <>
            <p>
              <Tag theme={query.data.ready ? 'success' : 'warning'}>
                {query.data.ready ? '真实路由可用' : '待配置或配置失效'}
              </Tag>{' '}
              · 已发布版本 {query.data.revision}
            </p>
            <div className="table-scroll" role="region" aria-label="真实供应商状态表" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th>供应商 / 区域</th>
                    <th>固定服务入口</th>
                    <th>部署凭证</th>
                  </tr>
                </thead>
                <tbody>
                  {query.data.suppliers.map((supplier) => (
                    <tr key={supplier.id}>
                      <td>
                        {names[supplier.id] ?? supplier.id}
                        <br />
                        {supplier.region}
                      </td>
                      <td>
                        <code>{supplier.endpoint}</code>
                      </td>
                      <td>
                        {supplier.credential_configured ? '已注入' : '未注入或当前模式不可检测'}
                        <br />
                        <code>{supplier.key_environment}</code>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <h3>已生效模型与价格</h3>
            {query.data.models.filter((model) => model.currency === 'CNY_MICRO').length === 0 ? (
              <p>暂无可用的真实模型。请先完成凭证与配置发布，不会显示演示数据。</p>
            ) : (
              <div
                className="table-scroll"
                role="region"
                aria-label="已发布模型配置表"
                tabIndex={0}
              >
                <table>
                  <thead>
                    <tr>
                      <th>模型别名 / 供应商</th>
                      <th>输入 / 输出单价（微元/百万 token）</th>
                      <th>输入 / 输出 token 上界</th>
                    </tr>
                  </thead>
                  <tbody>
                    {query.data.models
                      .filter((model) => model.currency === 'CNY_MICRO')
                      .map((model) => (
                        <tr key={model.id}>
                          <td>
                            {model.id}
                            <br />
                            {names[model.provider] ?? model.provider} · {model.region}
                          </td>
                          <td>
                            {model.input_per_million} / {model.output_per_million}
                          </td>
                          <td>
                            {model.max_input_tokens} / {model.max_output_tokens}
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
            )}
            <p>
              厂商密钥通过服务器秘密环境注入；本页面不会回显或保存厂商密钥。必须核验中国大陆处理、存储、备份及合同，不以域名代替驻留证明。
            </p>
            {!query.data.managed && (
              <p>
                当前为文件配置或测试模式。正常启动不传 --providers/--mock-only
                即使用数据库管理模式；文件模式保持只读兼容。
              </p>
            )}
            {query.data.managed && !query.data.can_manage && (
              <p>
                当前身份可以查看路由状态，不能修改平台配置。请由部署人员设置
                AIGW_PROVIDER_ADMIN_ISSUER 和
                AIGW_PROVIDER_ADMIN_SUBJECT，绑定指定平台管理员；组织所有者不会自动获得全局写权限。
              </p>
            )}
          </>
        )}
      </section>
      {query.data?.can_manage && (
        <ProviderEditor key={query.data.revision} status={query.data} csrf={csrf} />
      )}
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>应用接入方式</h2>
            <p>
              供应商配置发布 → 创建 CNY_MICRO 组织和项目 → 授权模型 → 签发项目密钥 → 修改应用地址。
            </p>
          </div>
        </div>
        <p>
          OpenAI 兼容 SDK 的 base_url 设置为当前网关 HTTPS 地址加 /v1，api_key 使用项目密钥，model
          使用已发布并授权的别名。支持文本 Chat Completions JSON/SSE；尚不支持工具调用、多模态或
          Embedding。
        </p>
        <p>
          请求经过密钥认证、模型权限、共享限流、熔断和预算预留后，才用服务器上的厂商凭证发往固定大陆入口；失败不回退到模拟数据。
        </p>
      </section>
    </>
  );
}
function ProviderEditor({ status, csrf }: { status: ProviderConfigurationStatus; csrf: string }) {
  const client = useQueryClient();
  const [draft, setDraft] = useState<ProviderConfiguration>(
    status.configuration ?? { version: 1, deployment_residency: emptyResidency(), models: [] },
  );
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const changeModel = (index: number, field: string, value: string | number) => {
    setDraft((old) => ({
      ...old,
      models: old.models.map((model, i) => (i === index ? { ...model, [field]: value } : model)),
    }));
  };
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError(null);
    setNotice('');
    try {
      await api('/api/v1/providers', {
        method: 'PUT',
        csrf,
        body: { expected_revision: status.revision, configuration: draft },
      });
      await client.invalidateQueries();
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  return (
    <section className="panel governance-panel provider-editor">
      <div className="panel-heading">
        <div>
          <h2>发布真实路由与价格</h2>
          <p>
            一次原子发布整份配置，保存后新请求读取数据库版本，正在执行的请求保持原路由与价格。移除模型不会自动删除项目授权，后续请求按新目录拒绝。
          </p>
        </div>
      </div>
      {Boolean(error) && <ErrorBox error={error} />}
      {notice && <p role="status">{notice}</p>}
      <form
        onSubmit={(event) => {
          void save(event);
        }}
      >
        <h3>中国大陆部署与存储依据</h3>
        <label>
          部署证据引用
          <input
            aria-label="部署证据引用"
            required
            maxLength={256}
            value={draft.deployment_residency.evidence_ref}
            onChange={(e) =>
              setDraft({
                ...draft,
                deployment_residency: {
                  ...draft.deployment_residency,
                  evidence_ref: e.target.value,
                },
              })
            }
          />
        </label>
        <label>
          部署证据有效期（UTC）
          <input
            aria-label="部署证据有效期"
            type="datetime-local"
            required
            value={draft.deployment_residency.expires_at.slice(0, 16)}
            onChange={(e) =>
              setDraft({
                ...draft,
                deployment_residency: {
                  ...draft.deployment_residency,
                  expires_at: e.target.value ? `${e.target.value}:00Z` : '',
                },
              })
            }
          />
        </label>
        {draft.models.map((model, index) => (
          <fieldset className="provider-model" key={index}>
            <legend>模型路由 {index + 1}</legend>
            <label>
              供应商
              <select
                aria-label={`供应商 ${index + 1}`}
                value={model.provider}
                onChange={(e) => changeModel(index, 'provider', e.target.value)}
              >
                {Object.entries(names).map(([id, name]) => (
                  <option value={id} key={id}>
                    {name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              网关模型别名
              <input
                aria-label={`网关模型别名 ${index + 1}`}
                required
                maxLength={128}
                placeholder="供应商ID/团队模型别名"
                value={model.id}
                onChange={(e) => changeModel(index, 'id', e.target.value)}
              />
            </label>
            <label>
              厂商模型或推理端点 ID
              <input
                aria-label={`厂商模型 ${index + 1}`}
                required
                maxLength={128}
                value={model.upstream_model}
                onChange={(e) => changeModel(index, 'upstream_model', e.target.value)}
              />
            </label>
            <label>
              价格版本
              <input
                aria-label={`价格版本 ${index + 1}`}
                required
                maxLength={64}
                value={model.price_version}
                onChange={(e) => changeModel(index, 'price_version', e.target.value)}
              />
            </label>
            {(
              [
                ['input_per_million', '输入单价（微元/百万 token）', 9007199254740991],
                ['output_per_million', '输出单价（微元/百万 token）', 9007199254740991],
                ['max_input_tokens', '最大输入 token', 2000000],
                ['max_output_tokens', '最大输出 token', 1000000],
              ] as const
            ).map(([field, label, max]) => (
              <label key={field}>
                {label}
                <input
                  aria-label={`${label} ${index + 1}`}
                  type="number"
                  required
                  min={1}
                  max={max}
                  step={1}
                  value={model[field] || ''}
                  onChange={(e) => changeModel(index, field, Number(e.target.value))}
                />
              </label>
            ))}
            <label>
              模型驻留证据
              <input
                aria-label={`模型驻留证据 ${index + 1}`}
                required
                maxLength={256}
                value={model.residency.evidence_ref}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    models: draft.models.map((m, i) =>
                      i === index
                        ? { ...m, residency: { ...m.residency, evidence_ref: e.target.value } }
                        : m,
                    ),
                  })
                }
              />
            </label>
            <label>
              模型证据有效期（UTC）
              <input
                aria-label={`模型证据有效期 ${index + 1}`}
                type="datetime-local"
                required
                value={model.residency.expires_at.slice(0, 16)}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    models: draft.models.map((m, i) =>
                      i === index
                        ? {
                            ...m,
                            residency: {
                              ...m.residency,
                              expires_at: e.target.value ? `${e.target.value}:00Z` : '',
                            },
                          }
                        : m,
                    ),
                  })
                }
              />
            </label>
            <Button
              variant="outline"
              theme="danger"
              onClick={() =>
                setDraft({ ...draft, models: draft.models.filter((_, i) => i !== index) })
              }
            >
              移除模型 {index + 1}
            </Button>
          </fieldset>
        ))}
        <Button
          disabled={pending || draft.models.length >= 32}
          onClick={() =>
            setDraft({
              ...draft,
              models: [
                ...draft.models,
                {
                  id: '',
                  provider: 'aliyun',
                  upstream_model: '',
                  price_version: '',
                  input_per_million: 0,
                  output_per_million: 0,
                  max_input_tokens: 0,
                  max_output_tokens: 0,
                  residency: emptyResidency(),
                },
              ],
            })
          }
        >
          添加模型路由
        </Button>
        <Button theme="primary" type="submit" loading={pending} disabled={!draft.models.length}>
          发布供应商配置
        </Button>
        <p>
          价格必须填写实际合同报价：1 元 = 1,000,000 微元。例如 2 元/百万 token 对应 2,000,000
          微元/百万 token。请勿照抄示例价格；系统按配置的完整输入上界预留预算。
        </p>
      </form>
    </section>
  );
}
