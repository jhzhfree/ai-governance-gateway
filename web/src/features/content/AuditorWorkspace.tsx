import { ContentAuditPanel } from './ContentAuditPanel';
import type { Session } from '../../api';
export function AuditorWorkspace({
  session,
  org,
  project,
}: {
  session: Session;
  org: string;
  project: string;
}) {
  const projectPath = `/api/v1/organizations/${encodeURIComponent(org)}/projects/${encodeURIComponent(project)}`;
  return (
    <main className="main-content">
      <a href="/">返回管理控制台</a>
      <h1>项目内容审计专用访问</h1>
      <p>
        组织：{org} · 项目：{project} · 当前身份：{session.user.subject}
      </p>
      <ContentAuditPanel
        key={projectPath}
        identity={[session.user.issuer, session.user.subject]}
        projectPath={projectPath}
        csrf={session.csrf_token}
      />
    </main>
  );
}
