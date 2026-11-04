import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Session } from './api';
import { Mark, ErrorBox } from './shared/Feedback';
import { useSearchParams } from 'react-router-dom';
import { AuditorWorkspace } from './features/content/AuditorWorkspace';
import { LoginOptions, LocalPasswordForm } from './features/local/LocalLogin';
import { Console } from './features/workspace/Console';

export function App() {
  const client = useQueryClient();
  const [params] = useSearchParams();
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    const expired = () => {
      setExpired(true);
      client.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
      client.setQueryData(['session'], null);
    };
    window.addEventListener('aigw:session-expired', expired);
    return () => window.removeEventListener('aigw:session-expired', expired);
  }, [client]);
  const session = useQuery({
    queryKey: ['session'],
    queryFn: ({ signal }) => api<Session>('/auth/session', { signal }),
    staleTime: 60_000,
  });
  if (session.isPending && !expired)
    return (
      <div className="center-page" role="status">
        正在验证登录状态…
      </div>
    );
  if (
    !expired &&
    session.error &&
    (!(session.error instanceof ApiError) || session.error.status !== 401)
  )
    return (
      <div className="center-page">
        <ErrorBox
          error={session.error}
          retry={() => {
            void session.refetch();
          }}
        />
      </div>
    );
  if (
    expired ||
    !session.data ||
    (session.error instanceof ApiError && session.error.status === 401)
  )
    return (
      <div className="login-page">
        <div className="login-brand">
          <Mark />
          <span>
            序衡 <small>AI GOVERNANCE</small>
          </span>
        </div>
        <main className="login-card">
          <div className="eyebrow">团队的 AI 访问控制中心</div>
          <h1>
            统一管理模型访问
            <br />
            与团队预算。
          </h1>
          <p>把项目、访问密钥和用量放在一起，让每次调用都有清晰的权限和记录。</p>
          <LoginOptions />
        </main>
        <footer>序衡 · AI 治理网关</footer>
      </div>
    );
  if (session.data.must_change_password)
    return (
      <main className="main-content">
        <h1>本地账号安全初始化</h1>
        <LocalPasswordForm forced />
      </main>
    );
  if (params.get('audit_only') === '1' && params.get('org') && params.get('project'))
    return (
      <AuditorWorkspace
        session={session.data}
        org={params.get('org')!}
        project={params.get('project')!}
      />
    );
  return <Console session={session.data} />;
}
