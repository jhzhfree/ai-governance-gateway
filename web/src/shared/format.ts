export const roleNames: Record<string, string> = {
  owner: '所有者',
  admin: '管理员',
  developer: '开发者',
  viewer: '只读成员',
};

export const actionNames: Record<string, string> = {
  'organization.member_disabled': '停用组织成员',
  'organization.member_enabled': '恢复组织成员',
  'organization.ownership_requested': '发起所有权交接',
  'organization.ownership_accepted': '确认所有权交接',
  'organization.ownership_canceled': '取消所有权交接',
  'budget_alert.policy_changed': '调整预算告警策略',
  'budget_alert.acknowledged': '确认预算告警',
  'cost_report.exported': '导出成本报表',
  'budget.limit_changed': '调整预算上限',
  'device.registered': '登记设备公钥',
  'device.active': '确认设备登记',
  'device.revoked': '永久撤销设备',
  'device.policy_changed': '修改终端准入策略',
  'device.session_issued': '签发设备短期会话',
  'device.session_revoked': '撤销设备短期会话',
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
