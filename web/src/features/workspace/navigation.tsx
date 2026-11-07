export const navigation = [
  {
    label: '工作台',
    items: [
      {
        id: 'overview',
        label: '项目概览',
        description: '查看项目预算、模型授权与访问凭证，掌握当前使用情况。',
        path: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
      },
      {
        id: 'requests',
        label: '调用账本',
        description: '按请求追踪用量、费用与结算状态，定位需要核对的调用。',
        path: 'M4 4h16v16H4z M8 8h8 M8 12h8 M8 16h4',
      },
      {
        id: 'operations',
        label: '流量与核查',
        description: '配置流量边界，核查未知费用与需要人工处理的请求。',
        path: 'M3 12h4l3-8 4 16 3-8h4',
      },
    ],
  },
  {
    label: '模型与访问',
    items: [
      {
        id: 'providers',
        label: '供应商配置',
        description: '管理供应商凭证、模型与价格，审查配置后发布生效。',
        path: 'M4 7h16 M16 3l4 4-4 4 M20 17H4 M8 13l-4 4 4 4',
      },
      {
        id: 'keys',
        label: '访问密钥',
        description: '为应用签发项目密钥，控制模型范围、有效期与撤销状态。',
        path: 'M14 9a5 5 0 1 1-3-4 M14 9l7-7 M17 5l2 2 M8 10h.01',
      },
      {
        id: 'devices',
        label: '终端准入',
        description: '登记可信设备，管理准入策略与短期会话。',
        path: 'M3 4h18v13H3z M8 21h8 M12 17v4 M7 8h10',
      },
    ],
  },
  {
    label: '费用治理',
    items: [
      {
        id: 'budget',
        label: '预算管理',
        description: '分配组织与项目预算，管理周期、调整和审批。',
        path: 'M3 5h18v14H3z M3 9h18 M16 13h2',
      },
      {
        id: 'reports',
        label: '成本报表',
        description: '分析费用归属与用量，按授权范围查询和导出报表。',
        path: 'M4 3v18h17 M8 16v-5 M13 16V7 M18 16v-9',
      },
      {
        id: 'alerts',
        label: '预算告警',
        description: '设置预算告警阈值，跟踪告警并确认处理情况。',
        path: 'M5 17h14l-2-3V9a5 5 0 0 0-10 0v5z M10 21h4 M12 2v2',
      },
    ],
  },
  {
    label: '安全与审计',
    items: [
      {
        id: 'content',
        label: '内容审计',
        description: '配置内容采集与审计授权，在受控范围内查看请求证据。',
        path: 'M12 3l8 3v6c0 4-4 7-8 9-4-2-8-5-8-9V6z M8 12l3 3 5-6',
      },
      {
        id: 'keywords',
        label: '关键字匹配',
        description: '维护关键字规则与执行策略，为内容治理设置基础边界。',
        path: 'M10 17a7 7 0 1 0 0-14 7 7 0 0 0 0 14 M15 15l6 6 M7 10h6',
      },
      {
        id: 'audit',
        label: '审计记录',
        description: '追踪组织配置、权限与访问凭证的变更记录。',
        path: 'M6 3h9l4 4v14H6z M15 3v5h4 M9 12h7 M9 16h5',
      },
    ],
  },
  {
    label: '组织与系统',
    items: [
      {
        id: 'members',
        label: '成员管理',
        description: '管理组织成员与项目授权，按职责分配访问权限。',
        path: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2 M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8 M17 4a4 4 0 0 1 0 7 M22 21v-2a4 4 0 0 0-3-4',
      },
      {
        id: 'departments',
        label: '部门管理',
        description: '维护部门层级与成员归属，为费用统计提供组织维度。',
        path: 'M9 2h6v5H9z M2 17h6v5H2z M16 17h6v5h-6z M12 7v5 M5 17v-5h14v5',
      },
      {
        id: 'ownership',
        label: '组织交接',
        description: '发起或确认所有权交接，保留交接过程的审计记录。',
        path: 'M3 7h15 M14 3l4 4-4 4 M21 17H6 M10 13l-4 4 4 4',
      },
      {
        id: 'local',
        label: '本地账号',
        description: '管理离线本地账号、启停状态与登录凭证。',
        path: 'M12 12a5 5 0 1 0 0-10 5 5 0 0 0 0 10 M3 22v-2a6 6 0 0 1 6-6h6a6 6 0 0 1 6 6v2',
      },
      {
        id: 'system',
        label: '系统管理',
        description: '查看节点资源，受控管理网络配置并核查变更记录。',
        path: 'M4 3h16v7H4z M4 14h16v7H4z M8 6h.01 M8 17h.01 M12 6h5 M12 17h5',
      },
    ],
  },
] as const;

export const navigationItems = navigation.flatMap((group) => [...group.items]);
export type ConsoleView = (typeof navigationItems)[number]['id'];

export function NavigationIcon({ path }: { path: string }) {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={path} />
    </svg>
  );
}
