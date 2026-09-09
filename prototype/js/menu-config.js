// js/menu-config.js
// 菜单配置 v4：super_admin / org_admin / developer 三层权限
// super_admin（系统管理员）：全局配置、组织树、审计日志、系统级基础设施
// org_admin（组织管理员）：日常组织管理（项目、任务、通知规则、节假日、人员）
// developer（开发者）：普通成员操作（查看、处理任务、浏览数据）
const MENU_CONFIG = [
    // ========== 公共置顶 ==========
    {
        id: 'developer-dashboard',
        name: '工作台',
        href: 'developer-dashboard.html',
        icon: 'fa-briefcase',
        role: ['developer']
    },
    {
        id: 'project-dashboard',
        name: '项目总览',
        href: 'project-dashboard.html',
        icon: 'fa-building',
        role: ['developer']
    },

    // ========== 数据洞察（所有人可见） ==========
    {
        id: 'insights-group',
        name: '数据洞察',
        icon: 'fa-chart-bar',
        role: ['developer'],
        children: [
            { id: 'data-overview', name: '全局数据概览', href: 'data-overview.html', icon: 'fa-chart-line', role: ['developer'] },
            { id: 'mr-stats',    name: '代码提交统计', href: 'mr-stats.html',    icon: 'fa-code-branch', role: ['developer'] },
            { id: 'rule-stats',  name: '规则命中统计', href: 'rule-stats.html',  icon: 'fa-bullseye',    role: ['developer'] },
            { id: 'token-usage', name: 'Token 用量',   href: 'token-usage.html', icon: 'fa-coins',       role: ['org_admin'] },
        ]
    },

    // ========== AI 评审 ==========
    {
        id: 'ai-review-group',
        name: 'AI 评审',
        icon: 'fa-robot',
        role: ['developer'],
        children: [
            { id: 'projects',     name: '项目管理',   href: 'projects.html',     icon: 'fa-folder-open',    role: ['developer'] },
            { id: 'tasks',        name: '评审任务列表', href: 'tasks.html',        icon: 'fa-tasks',             role: ['developer'] },
            { id: 'review-rules', name: '评审规则库', href: 'review-rules.html', icon: 'fa-shield-halved',  role: ['org_admin'] },
            { id: 'vulnerability-db', name: '漏洞数据库', href: 'vulnerability-db.html', icon: 'fa-bug', role: ['developer'] },
            { id: 'models',       name: '大模型管理', href: 'models.html',       icon: 'fa-microchip',         role: ['super_admin'] },
            { id: 'agent-config', name: '智能体配置', href: 'agent-config.html', icon: 'fa-sliders',           role: ['org_admin'] },
            { id: 'incubator',    name: '规则孵化台', href: 'incubator.html',    icon: 'fa-flask',             role: ['org_admin'] },
        ]
    },

    // ========== 通知管理与组织运营（org_admin + super_admin） ==========
    {
        id: 'notify-group',
        name: '通知管理',
        icon: 'fa-bell',
        role: ['org_admin'],
        children: [
            { id: 'notifiers', name: '企业微信',     href: 'notifiers.html',       icon: 'fa-comment' },
            { id: 'mail',      name: '邮件',         href: 'mail.html',            icon: 'fa-envelope' },
            { id: 'notification-rules', name: '通知规则', href: 'notification-rules.html', icon: 'fa-gear' },
            { id: 'settings-holiday', name: '节假日管理',  href: 'holiday-management.html', icon: 'fa-calendar-day' },
            { id: 'report',    name: '报告管理',     href: 'report.html',          icon: 'fa-file-alt' },
        ]
    },

    // ========== 消息中心（公共） ==========
    { id: 'notifications', name: '消息中心', href: 'notifications.html', icon: 'fa-inbox', role: ['developer'] },

    // ========== MCP 集成 ==========
    {
        id: 'mcp-group',
        name: 'MCP 集成',
        icon: 'fa-microchip',
        role: ['developer'],
        children: [
            { id: 'mcp-capabilities', name: '能力中心', href: 'mcp-capabilities.html', icon: 'fa-robot', role: ['developer'] },
            { id: 'mcp-keys',         name: '密钥管理', href: 'mcp-keys.html',         icon: 'fa-key',   role: ['super_admin'] },
            { id: 'mcp-logs',         name: '调用日志', href: 'mcp-logs.html',         icon: 'fa-file-lines', role: ['super_admin'] },
        ]
    },

    // ========== 系统管理（仅 super_admin） ==========
    {
        id: 'settings-group',
        name: '系统管理',
        icon: 'fa-gear',
        role: ['super_admin'],
        children: [
            { id: 'settings-config', name: '系统配置',     href: 'settings.html?tab=config',         icon: 'fa-sliders' },
            { id: 'settings-storage', name: '对象存储',     href: 'storage-config.html',              icon: 'fa-database' },
            { id: 'settings-org',    name: '组织管理',     href: 'pages/org-admin.html',             icon: 'fa-building' },
            { id: 'settings-sso',  name: '单点登录', href: 'pages/sso-config.html',            icon: 'fa-key' },
            { id: 'settings-users',  name: '用户管理',     href: 'settings.html?tab=users',          icon: 'fa-user-cog' },
            { id: 'settings-logs',   name: '操作日志',     href: 'settings.html?tab=logs',           icon: 'fa-history' },
            { id: 'settings-info',   name: '系统信息',     href: 'settings.html?tab=info',           icon: 'fa-circle-info' },
        ]
    },
];

// 角色层级：数字越大权限越高
const ROLE_LEVEL = {
    super_admin: 3,
    org_admin:   2,
    developer:   1,
};

// 判断当前菜单项对指定角色是否可见
// 逻辑：用户的角色级别 >= 菜单要求的最低级别
function isMenuVisible(menuRole, userRole) {
    if (!menuRole || !Array.isArray(menuRole) || menuRole.length === 0) return true;
    const userLevel = ROLE_LEVEL[userRole] || 0;
    if (!userLevel) return true; // 未知角色默认放行
    return menuRole.some(r => userLevel >= (ROLE_LEVEL[r] || 0));
}

// 判断当前路径是否匹配菜单项 href（支持 query 参数）
function isMenuItemActive(href, currentPath, currentSearch) {
    if (!href) return false;
    const [path, query] = href.split('?');
    if (path !== currentPath) return false;
    if (!query) return true;
    const params = new URLSearchParams(currentSearch || '');
    const target = new URLSearchParams(query);
    for (const [k, v] of target) {
        if (params.get(k) !== v) return false;
    }
    return true;
}
