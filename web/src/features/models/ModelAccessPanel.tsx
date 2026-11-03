import { useRef, useState, type FormEvent } from 'react';
import { Button, Dialog, Tag } from 'tdesign-react';
import { api, type ModelInfo, type ProjectDetails } from '../../api';
import { ErrorBox } from '../../shared/Feedback';

const providers: Record<string, string> = {
  mock: '模拟模型',
  aliyun: '阿里云百炼 · 北京',
  volcengine: '火山方舟 · 北京',
  zhipu: '智谱 · 国内服务',
};
export const currencyLabel = (currency: string) =>
  currency === 'CNY_MICRO' ? '微人民币（1 元 = 1,000,000 单位）' : '模拟计费单位';
export function ModelAccessPanel({
  project,
  path,
  currency,
  models,
  error,
  canManage,
  csrf,
  onSaved,
}: {
  project: ProjectDetails;
  path: string;
  currency: string;
  models: ModelInfo[];
  error: unknown;
  canManage: boolean;
  csrf: string;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const dialogSemantics = { role: 'dialog', 'aria-modal': true, 'aria-label': '项目模型授权' };
  const form = useRef<HTMLFormElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const available = models.filter((model) => model.currency === currency);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const chosen = new FormData(event.currentTarget).getAll('models').map(String);
    setPending(true);
    setFailure(null);
    try {
      await api(`${path}/models`, { method: 'PUT', csrf, body: { models: chosen } });
      setOpen(false);
      onSaved();
    } catch (error) {
      setFailure(error);
    } finally {
      setPending(false);
    }
  }
  return (
    <>
      <section className="model-strip">
        <span className="model-icon" aria-hidden="true">
          ✳
        </span>
        <div>
          <strong>已授权模型</strong>
          <p>
            {project.models.length
              ? project.models.join(' · ')
              : '当前未授权模型，项目不能发起新的模型调用。'}
          </p>
        </div>
        <Tag variant="outline">{currencyLabel(currency)}</Tag>
        {canManage && (
          <Button
            ref={trigger}
            variant="outline"
            disabled={Boolean(error) || available.length === 0}
            onClick={() => {
              setFailure(null);
              setOpen(true);
            }}
          >
            配置模型
          </Button>
        )}
      </section>
      {Boolean(error) && <ErrorBox error={error} />}
      <Dialog
        {...dialogSemantics}
        header="项目模型授权"
        visible={open}
        footer={false}
        destroyOnClose
        onClose={() => {
          if (!pending) setOpen(false);
        }}
        onOpened={() =>
          form.current?.querySelector<HTMLInputElement>('input[type="checkbox"]')?.focus()
        }
        onClosed={() => trigger.current?.focus()}
      >
        <form
          ref={form}
          onKeyDown={(event) => {
            if (event.key !== 'Tab') return;
            const controls = form.current?.querySelectorAll<HTMLElement>(
              'input:not([disabled]),button:not([disabled])',
            );
            if (!controls?.length) return;
            const first = controls[0],
              last = controls[controls.length - 1];
            if (event.shiftKey && document.activeElement === first) {
              event.preventDefault();
              last.focus();
            } else if (!event.shiftKey && document.activeElement === last) {
              event.preventDefault();
              first.focus();
            }
          }}
          className="dialog-form model-selection"
          onSubmit={(event) => {
            void save(event);
          }}
        >
          <p>
            仅可授权与组织预算单位一致的已启用模型。清空授权会立即阻止后续调用；在途请求正常结算。
          </p>
          {currency === 'CNY_MICRO' && (
            <p>
              供应商、网关、数据库、日志与备份均需通过中国大陆处理与存储验收；此页面不替代供应商合同核验。
            </p>
          )}
          <fieldset>
            <legend>可授权模型</legend>
            {available.map((model) => (
              <label key={model.id}>
                <input
                  type="checkbox"
                  name="models"
                  value={model.id}
                  defaultChecked={project.models.includes(model.id)}
                />
                <span>
                  <strong>{model.id}</strong>
                  <small>{providers[model.provider] ?? model.provider}</small>
                  {model.provider !== 'mock' && (
                    <small>
                      每百万 token 输入 {model.input_per_million} / 输出 {model.output_per_million}{' '}
                      微人民币 · 按配置费率计价
                    </small>
                  )}
                </span>
              </label>
            ))}
          </fieldset>
          {Boolean(failure) && <ErrorBox error={failure} />}
          <div className="dialog-actions">
            <Button variant="outline" disabled={pending} onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button theme="primary" type="submit" loading={pending}>
              保存模型授权
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
