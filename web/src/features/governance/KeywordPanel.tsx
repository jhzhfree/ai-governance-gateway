import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, type KeywordPolicy, type KeywordMatch, type KeywordRule } from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { date } from '../../shared/format';
const actions: Record<string, string> = { alert: '告警留痕', block: '阻断', redact: '脱敏' };
export function KeywordPanel({
  identity,
  projectPath,
  csrf,
}: {
  identity: string[];
  projectPath: string;
  csrf: string;
}) {
  const path = `${projectPath}/keywords`;
  const query = useQuery({
    queryKey: [...identity, path],
    queryFn: ({ signal }) => api<KeywordPolicy>(path, { signal }),
  });
  const matches = usePage<KeywordMatch>(identity, 'keyword-matches', `${path}/matches`, true);
  return (
    <>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>项目关键字规则</h2>
            <p>
              输入支持告警/阻断；非流式输出支持告警/阻断/脱敏。流式仅支持字面规则告警，跨片段检测；需要其他输出处置时拒绝流式请求，避免绕过。
            </p>
          </div>
          <Button
            onClick={() => {
              void query.refetch();
              void matches.refetch();
            }}
          >
            刷新关键字规则
          </Button>
        </div>
        {query.error && <ErrorBox error={query.error} />}{' '}
        {query.data && (
          <KeywordEditor key={query.data.revision} path={path} csrf={csrf} policy={query.data} />
        )}
      </section>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>关键字命中记录</h2>
            <p>仅保存规则、处置和请求编号，不复制命中的原文；告警在此留痕，外部通知另行配置。</p>
          </div>
          <Button
            onClick={() => {
              void matches.refetch();
            }}
          >
            刷新命中记录
          </Button>
        </div>
        {matches.error && <ErrorBox error={matches.error} />}
        <div className="table-scroll" role="region" aria-label="关键字命中记录表" tabIndex={0}>
          <table>
            <thead>
              <tr>
                <th>请求编号</th>
                <th>规则 / 策略版本</th>
                <th>方向 / 处置</th>
                <th>时间</th>
              </tr>
            </thead>
            <tbody>
              {matches.data?.pages
                .flatMap((page) => page.data)
                .map((item) => (
                  <tr key={item.id}>
                    <td>{item.request_id}</td>
                    <td>
                      {item.rule_id} / {item.policy_revision}
                    </td>
                    <td>
                      {item.direction === 'input' ? '输入' : '输出'} / {actions[item.action]}
                    </td>
                    <td>{date(item.created_at)}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
        {matches.hasNextPage && (
          <Button
            loading={matches.isFetchingNextPage}
            onClick={() => {
              void matches.fetchNextPage();
            }}
          >
            加载更多命中
          </Button>
        )}
      </section>
    </>
  );
}
function KeywordEditor({
  path,
  csrf,
  policy,
}: {
  path: string;
  csrf: string;
  policy: KeywordPolicy;
}) {
  const client = useQueryClient();
  const [rules, setRules] = useState<KeywordRule[]>(policy.rules);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const change = (index: number, patch: Partial<KeywordRule>) =>
    setRules((old) => old.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      await api(path, { method: 'PUT', csrf, body: { expected_revision: policy.revision, rules } });
      await client.invalidateQueries();
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  return (
    <>
      {Boolean(error) && <ErrorBox error={error} />}
      <form
        onSubmit={(event) => {
          void save(event);
        }}
      >
        <p>
          策略版本 {policy.revision} · 最多 32 条规则，规则文本最多 256
          字节。字面匹配区分大小写；正则使用 RE2，不支持回溯引用/环视。保存空规则清单可关闭匹配。
        </p>
        {rules.map((rule, index) => (
          <fieldset className="provider-model" key={rule.id}>
            <legend>规则 {index + 1}</legend>
            <label>
              匹配文本
              <input
                aria-label={`关键字文本 ${index + 1}`}
                required
                maxLength={256}
                value={rule.pattern}
                onChange={(e) => change(index, { pattern: e.target.value })}
              />
            </label>
            <label>
              匹配方式
              <select
                aria-label={`关键字方式 ${index + 1}`}
                value={rule.kind}
                onChange={(e) => change(index, { kind: e.target.value as KeywordRule['kind'] })}
              >
                <option value="literal">字面匹配</option>
                <option value="regex">正则表达式</option>
              </select>
            </label>
            <label>
              检测方向
              <select
                aria-label={`关键字方向 ${index + 1}`}
                value={rule.direction}
                onChange={(e) =>
                  change(index, {
                    direction: e.target.value as KeywordRule['direction'],
                    action:
                      rule.action === 'redact' && e.target.value !== 'output'
                        ? 'block'
                        : rule.action,
                  })
                }
              >
                <option value="input">输入</option>
                <option value="output">输出</option>
                <option value="both">输入和输出</option>
              </select>
            </label>
            <label>
              处置
              <select
                aria-label={`关键字处置 ${index + 1}`}
                value={rule.action}
                onChange={(e) => change(index, { action: e.target.value as KeywordRule['action'] })}
              >
                <option value="alert">告警留痕</option>
                <option value="block">阻断</option>
                <option value="redact" disabled={rule.direction !== 'output'}>
                  脱敏（非流式输出）
                </option>
              </select>
            </label>
            <Button
              variant="outline"
              theme="danger"
              onClick={() => setRules((old) => old.filter((_, i) => i !== index))}
            >
              删除规则 {index + 1}
            </Button>
          </fieldset>
        ))}
        <Button
          disabled={pending || rules.length >= 32}
          onClick={() =>
            setRules((old) => [
              ...old,
              {
                id: crypto.randomUUID(),
                kind: 'literal',
                pattern: '',
                direction: 'input',
                action: 'block',
              },
            ])
          }
        >
          添加关键字规则
        </Button>
        <Button type="submit" theme="primary" loading={pending}>
          保存关键字策略
        </Button>
      </form>
    </>
  );
}
