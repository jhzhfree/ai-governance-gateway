import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import {
  api,
  type Device,
  type DevicePolicy,
  type DeviceSession,
  type DeviceEvent,
  type DeviceRequest,
} from '../../api';
import { usePage } from '../../shared/useResourcePage';
import { ErrorBox } from '../../shared/Feedback';
import { date } from '../../shared/format';
type Props = {
  identity: string[];
  orgPath: string;
  projectPath?: string;
  csrf: string;
  canManage: boolean;
};
const states: Record<string, string> = { pending: '待确认', active: '已确认', revoked: '已撤销' };
export function DevicePanel(props: Props) {
  const client = useQueryClient();
  const list = usePage<Device>(props.identity, 'devices', `${props.orgPath}/devices`, true);
  const [selected, setSelected] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState('');
  const devices = list.data?.pages.flatMap((p) => p.data) ?? [];
  const device = devices.find((d) => d.id === selected);
  async function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    setPending(true);
    setError(null);
    try {
      const v = await api<Device>(`${props.orgPath}/devices`, {
        method: 'POST',
        csrf: props.csrf,
        body: {
          name: String(data.get('name')).trim(),
          public_key: String(data.get('public_key')).trim(),
        },
      });
      setSelected(v.id);
      form.reset();
      setNotice('公钥已登记，等待组织管理员核对确认。');
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <>
      <section className="panel governance-panel">
        <div className="panel-heading">
          <div>
            <h2>终端基础准入</h2>
            <p>设备公钥登记、管理员确认与私钥持有证明；当前不包含安全体检、TPM 或 SAC/DAS 对接。</p>
          </div>
          <Button
            tag="button"
            onClick={() => {
              void list.refetch();
            }}
          >
            刷新设备
          </Button>
        </div>
        <p>
          先在本机运行 aigw device
          keygen，再登记其输出的公钥。私钥和本机访问令牌只留在终端，切勿粘贴到页面。Linux SDK 可通过
          aigw device proxy 自动附加五分钟设备凭证与每次请求证明。
        </p>
        {list.error && <ErrorBox error={list.error} />}{' '}
        {error != null && <ErrorBox error={error} />} {notice && <p role="status">{notice}</p>}
        <form
          className="member-lifecycle-form"
          onSubmit={(e) => {
            void register(e);
          }}
        >
          <label>
            设备名称
            <input name="name" required maxLength={80} />
          </label>
          <label>
            Ed25519 设备公钥
            <input
              name="public_key"
              required
              minLength={43}
              maxLength={43}
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          <Button tag="button" type="submit" theme="primary" loading={pending}>
            登记设备公钥
          </Button>
        </form>
        <label>
          选择设备
          <select
            aria-label="选择设备"
            value={selected}
            onChange={(e) => setSelected(e.target.value)}
          >
            <option value="">请选择设备查看详情</option>
            {devices.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name} · {states[d.state]} · {d.owner.subject}
              </option>
            ))}
          </select>
        </label>
        {list.hasNextPage && (
          <Button
            tag="button"
            onClick={() => {
              void list.fetchNextPage();
            }}
          >
            加载更多设备
          </Button>
        )}
        {devices.length === 0 && !list.isPending && <p>暂无登记设备。</p>}
      </section>
      {props.projectPath && <DevicePolicyPanel {...props} projectPath={props.projectPath} />}
      {device && (
        <DeviceDetails key={device.id + '/' + device.revision} {...props} device={device} />
      )}
    </>
  );
}
function DevicePolicyPanel(props: Props & { projectPath: string }) {
  const client = useQueryClient();
  const path = `${props.projectPath}/device-policy`;
  const policy = useQuery({
    queryKey: [...props.identity, 'device-policy', path],
    queryFn: ({ signal }) => api<DevicePolicy>(path, { signal }),
  });
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!policy.data || pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    try {
      await api(path, {
        method: 'PUT',
        csrf: props.csrf,
        body: {
          required: data.get('required') === 'on',
          expected_revision: policy.data.revision,
          reason: String(data.get('reason')).trim(),
        },
      });
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="panel governance-panel">
      <h2>当前项目准入策略</h2>
      <p>
        强制准入开启后，长期项目密钥不能直接调用模型或发现模型。请先验证已确认设备与本机代理，再开启。
      </p>
      {policy.error && <ErrorBox error={policy.error} />}{' '}
      {error != null && <ErrorBox error={error} />}{' '}
      {policy.data && (
        <>
          <p>
            当前状态：{policy.data.required ? '强制设备准入' : '允许原项目密钥'} · 策略版本{' '}
            {policy.data.revision}
          </p>
          {!policy.data.available && (
            <p>服务端尚未配置有效公开 HTTPS 地址，不能开启强制准入；请由运维配置控制台公开地址。</p>
          )}
          {props.canManage && (
            <form
              key={policy.data.revision}
              className="member-lifecycle-form"
              onSubmit={(e) => {
                void save(e);
              }}
            >
              <label>
                <input
                  type="checkbox"
                  name="required"
                  defaultChecked={policy.data.required}
                  disabled={!policy.data.available && !policy.data.required}
                />
                要求已确认设备与请求证明
              </label>
              <label>
                准入策略变更原因
                <input name="reason" required maxLength={256} />
              </label>
              <label>
                <input type="checkbox" required />
                我确认开启后原项目密钥将被阻断
              </label>
              <Button tag="button" type="submit" theme="primary" loading={pending}>
                保存项目准入策略
              </Button>
            </form>
          )}
        </>
      )}
    </section>
  );
}
function DeviceDetails(props: Props & { device: Device }) {
  const client = useQueryClient();
  const d = props.device;
  const path = `${props.orgPath}/devices/${encodeURIComponent(d.id)}`;
  const sessions = usePage<DeviceSession>(
    props.identity,
    'device-sessions',
    `${path}/sessions`,
    true,
  );
  const history = usePage<DeviceEvent>(props.identity, 'device-events', `${path}/events`, true);
  const requests = usePage<DeviceRequest>(
    props.identity,
    'device-requests',
    `${path}/requests`,
    true,
  );
  const [action, setAction] = useState(
    props.canManage && d.state === 'pending' ? 'active' : 'revoked',
  );
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function change(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    try {
      await api(path, {
        method: 'PUT',
        csrf: props.csrf,
        body: {
          state: action,
          expected_revision: d.revision,
          reason: String(data.get('reason')).trim(),
        },
      });
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  async function revoke(event: FormEvent<HTMLFormElement>, session: DeviceSession) {
    event.preventDefault();
    if (pending) return;
    const data = new FormData(event.currentTarget);
    setPending(true);
    setError(null);
    try {
      await api(`${path}/sessions/${encodeURIComponent(session.id)}/revoke`, {
        method: 'POST',
        csrf: props.csrf,
        body: { reason: String(data.get('reason')).trim() },
      });
      await client.invalidateQueries();
    } catch (failure) {
      setError(failure);
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="panel governance-panel member-identity">
      <h2>设备详情：{d.name}</h2>
      <p>
        设备编号 {d.id} · {states[d.state]} · 版本 {d.revision}
      </p>
      <p>
        所属人员 {d.owner.subject}
        <br />
        {d.owner.issuer}
      </p>
      <p>登记公钥 {d.public_key}</p>
      {error != null && <ErrorBox error={error} />}
      {d.state !== 'revoked' && (
        <form
          className="member-lifecycle-form"
          onSubmit={(e) => {
            void change(e);
          }}
        >
          <label>
            设备操作
            <select
              aria-label="设备操作"
              value={action}
              onChange={(e) => setAction(e.target.value)}
            >
              {props.canManage && d.state === 'pending' && (
                <option value="active">确认设备登记</option>
              )}
              <option value="revoked">永久撤销设备及全部会话</option>
            </select>
          </label>
          <label>
            设备操作原因
            <input name="reason" required maxLength={256} />
          </label>
          <label>
            <input type="checkbox" required />
            {action === 'active'
              ? '我已核对设备公钥和所属人员，此确认不代表安全体检'
              : '我确认永久撤销，恢复账号也不会复活此设备'}
          </label>
          <Button
            tag="button"
            type="submit"
            theme={action === 'revoked' ? 'danger' : 'primary'}
            loading={pending}
          >
            提交设备操作
          </Button>
        </form>
      )}
      {d.state === 'revoked' && (
        <p>设备已永久撤销。重新接入须生成新密钥并重新登记，不能复用已撤销公钥。</p>
      )}
      <h3>设备短期会话</h3>
      {sessions.error && <ErrorBox error={sessions.error} />}{' '}
      {sessions.data?.pages
        .flatMap((p) => p.data)
        .map((v) => (
          <article key={v.id} className="member-state-event" aria-label={`设备会话 ${v.id}`}>
            <p>
              会话 {v.id} · 项目 {v.project_id} ·{' '}
              {v.revoked_at
                ? '已撤销'
                : new Date(v.expires_at).getTime() <= Date.now()
                  ? '已过期'
                  : '有效'}{' '}
              · 截止 {date(v.expires_at)}
            </p>
            <p>模型 {v.models.join(' · ')}</p>
            {!v.revoked_at && d.state === 'active' && (
              <form
                className="member-lifecycle-form"
                onSubmit={(e) => {
                  void revoke(e, v);
                }}
              >
                <label>
                  会话撤销原因
                  <input
                    aria-label={`会话撤销原因 ${v.id}`}
                    name="reason"
                    required
                    maxLength={256}
                  />
                </label>
                <Button
                  tag="button"
                  type="submit"
                  theme="danger"
                  loading={pending}
                  aria-label={`撤销设备会话 ${v.id}`}
                >
                  撤销设备会话
                </Button>
              </form>
            )}
          </article>
        ))}
      {sessions.hasNextPage && (
        <Button
          tag="button"
          onClick={() => {
            void sessions.fetchNextPage();
          }}
        >
          加载更多设备会话
        </Button>
      )}
      <h3>已入账调用</h3>
      <p>记录表示已建立预算占额，不能据此判断供应商执行或客户端收取响应是否成功。</p>
      {requests.error && <ErrorBox error={requests.error} />}
      <ul className="department-list">
        {requests.data?.pages
          .flatMap((p) => p.data)
          .map((v) => (
            <li key={v.request_id}>
              <a
                href={`/?${new URLSearchParams({ org: props.orgPath.split('/').at(-1) ?? '', project: v.project_id, view: 'requests', request: v.request_id })}`}
              >
                {v.request_id}
              </a>{' '}
              · {date(v.created_at)} · 会话 {v.session_id}
            </li>
          ))}
      </ul>
      {requests.hasNextPage && (
        <Button
          tag="button"
          onClick={() => {
            void requests.fetchNextPage();
          }}
        >
          加载更多设备调用
        </Button>
      )}
      <h3>设备状态历史</h3>
      {history.error && <ErrorBox error={history.error} />}
      <ul className="department-list">
        {history.data?.pages
          .flatMap((p) => p.data)
          .map((v) => (
            <li key={v.revision}>
              版本 {v.revision} · {states[v.state]} · {v.actor.subject} · {date(v.created_at)}
              <br />
              {v.reason}
            </li>
          ))}
      </ul>
    </section>
  );
}
