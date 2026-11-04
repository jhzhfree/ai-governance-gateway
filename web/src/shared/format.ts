export const roleNames: Record<string, string> = {
  owner: '所有者',
  admin: '管理员',
  developer: '开发者',
  viewer: '只读成员',
};

export const actionNames: Record<string, string> = {
  'budget.limit_changed': '调整预算上限',
  'budget.cycle_changed': '调整预算轮转',
  'budget.period_rotated': '切换预算周期',
  'keyword.policy_changed': '调整关键字策略',
  'organization.created': '创建组织',
  'content_audit.policy_changed': '调整内容采集策略',
  'content_audit.auditor_changed': '调整内容审计授权',
  'organization.member_changed': '调整组织成员',
  'project.created': '创建项目',
  'project.member_removed': '移除项目授权',
  'project.member_changed': '调整项目成员',
  'billing.imported': '导入账单',
  'billing.approved': '通过账单审核',
  'billing.rejected': '驳回账单',
  'project.limits_changed': '更新流量策略',
  'project.models_changed': '更新模型授权',
  'project.key_created': '创建访问密钥',
  'project.key_revoked': '撤销访问密钥',
};

export const number = (value: number) => value.toLocaleString('zh-CN');

export const date = (value: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value));
