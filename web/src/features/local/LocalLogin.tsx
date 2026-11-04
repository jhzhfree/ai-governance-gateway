import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button } from 'tdesign-react';
import { api, ApiError } from '../../api';
import { ErrorBox } from '../../shared/Feedback';

export function LoginOptions() {
  const config = useQuery({
    queryKey: ['auth-method'],
    queryFn: ({ signal }) => api<{ mode: 'oidc' | 'local' }>('/auth/config', { signal }),
    retry: false,
  });
  if (config.isPending) return <p role="status">正在读取登录方式…</p>;
  if (config.data?.mode !== 'local')
    return (
      <>
        <a className="login-button" href="/auth/login">
          使用组织账号登录 <span aria-hidden="true">↗</span>
        </a>
        <div className="login-note">通过组织身份服务安全登录</div>
      </>
    );
  return <LocalLogin />;
}
function LocalLogin() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    const password = String(values.get('password') ?? '');
    (form.elements.namedItem('password') as HTMLInputElement).value = '';
    setPending(true);
    setError(null);
    try {
      await api('/auth/local/login', {
        method: 'POST',
        body: { username: values.get('username'), password },
      });
      window.location.replace('/');
    } catch (value) {
      setError(
        value instanceof ApiError && value.status === 401
          ? new Error('账号或密码无效，请核对后重试。')
          : value,
      );
    } finally {
      setPending(false);
    }
  };
  return (
    <form
      className="dialog-form local-login-form"
      onSubmit={(event) => {
        void submit(event);
      }}
    >
      <h2>本地账号登录</h2>
      <p>使用部署管理员创建的账号，无需连接外部身份服务。</p>
      <label>
        用户名
        <input
          aria-label="本地用户名"
          name="username"
          autoComplete="username"
          required
          maxLength={64}
        />
      </label>
      <label>
        密码
        <input
          aria-label="本地密码"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          maxLength={256}
        />
      </label>
      {Boolean(error) && <ErrorBox error={error} />}
      <Button type="submit" theme="primary" loading={pending}>
        登录本地账号
      </Button>
    </form>
  );
}
export function LocalPasswordForm({ forced = false }: { forced?: boolean }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    const current = String(values.get('current') ?? '');
    const next = String(values.get('next') ?? '');
    const confirm = String(values.get('confirm') ?? '');
    if (next !== confirm) {
      setError(new Error('两次输入的新密码不一致。'));
      return;
    }
    form.reset();
    setPending(true);
    setError(null);
    try {
      const session = await api<{ csrf_token: string }>('/auth/session');
      await api('/auth/local/password', {
        method: 'POST',
        csrf: session.csrf_token,
        body: { current_password: current, new_password: next },
      });
      window.location.replace('/');
    } catch (value) {
      setError(value);
    } finally {
      setPending(false);
    }
  };
  return (
    <section className="panel governance-panel">
      <div className="panel-heading">
        <div>
          <h2>{forced ? '首次登录或重置后请修改密码' : '修改本地密码'}</h2>
          <p>
            新密码至少 12 个字符、最多 256 UTF-8
            字节。保存后所有本地会话及短期令牌撤销，需要重新登录。
          </p>
        </div>
      </div>
      {Boolean(error) && <ErrorBox error={error} />}
      <form
        onSubmit={(event) => {
          void submit(event);
        }}
      >
        <label>
          当前密码
          <input
            aria-label="当前本地密码"
            name="current"
            type="password"
            autoComplete="current-password"
            required
            maxLength={256}
          />
        </label>
        <label>
          新密码
          <input
            aria-label="新的本地密码"
            name="next"
            type="password"
            autoComplete="new-password"
            required
            minLength={12}
            maxLength={256}
          />
        </label>
        <label>
          确认新密码
          <input
            aria-label="确认新的本地密码"
            name="confirm"
            type="password"
            autoComplete="new-password"
            required
            minLength={12}
            maxLength={256}
          />
        </label>
        <Button type="submit" theme="primary" loading={pending}>
          保存本地密码并重新登录
        </Button>
      </form>
    </section>
  );
}
