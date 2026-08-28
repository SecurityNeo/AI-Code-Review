// js/menu-config.js
// 菜单配置 v3：admin/user 差异化，任务列表归入 AI 评审子菜单
// admin 顺序：控制台 → 项目总览 → 数据洞察 → AI 评审 → 通知管理 → 消息中心 → 系统管理
// user  顺序：工作台 → 项目总览 → 数据洞察 → 消息中心
const MENU_CONFIG = [
    // ========== admin 置顶 ==========
    {
        id: 'admin-dashboard',
        name: '控制台',
        href: 'admin-dashboard.html',
        icon: 'fa-globe',
        role: ['admin']
    },

    // ========== user 置顶 ==========
    {
        id: 'developer-dashboard',
        name: '工作台',
        href: 'developer-dashboard.html',
        icon: 'fa-briefcase',
        role: ['user']
    },

    // ========== 公共菜单（admin + user 均可见） ==========
    {
        id: 'project-dashboard',
        name: '项目总览',
        href: 'project-dashboard.html',
        icon: 'fa-building',
        role: ['admin', 'user']
    },

    // ========== 数据洞察（公共） ==========
    {
        id: 'insights-group',
        name: '数据洞察',
        icon: 'fa-chart-bar',
        role: ['admin', 'user'],
        children: [
            { id: 'data-overview', name: '全局数据概览', href: 'data-overview.html', icon: 'fa-chart-line', role: ['admin', 'user'] },
            { id: 'mr-stats',    name: '代码提交统计', href: 'mr-stats.html',    icon: 'fa-code-branch', role: ['admin', 'user'] },
            { id: 'rule-stats',  name: '规则命中统计', href: 'rule-stats.html',  icon: 'fa-bullseye',    role: ['admin', 'user'] },
            { id: 'token-usage', name: 'Token 用量',   href: 'token-usage.html', icon: 'fa-coins',       role: ['admin'] },
        ]
    },

    // ========== AI 评审（admin 全面，user 仅任务列表） ==========
    {
        id: 'ai-review-group',
        name: 'AI 评审',
        icon: 'fa-robot',
        role: ['admin', 'user'],
        children: [
            { id: 'projects',     name: '项目管理',   href: 'projects.html',     icon: 'fa-folder-open',    role: ['admin', 'user'] },
            { id: 'review-rules', name: '评审规则库', href: 'review-rules.html', icon: 'fa-shield-halved',  role: ['admin'] },
            { id: 'incubator',    name: '规则孵化台', href: 'incubator.html',    icon: 'fa-flask',            role: ['admin'] },
            { id: 'tasks',        name: '评审任务列表', href: 'tasks.html',        icon: 'fa-tasks',             role: ['admin', 'user'] },
            { id: 'pools',        name: '任务资源池', href: 'pools.html',        icon: 'fa-server',            role: ['admin'] },
            { id: 'models',       name: '大模型管理', href: 'models.html',       icon: 'fa-microchip',         role: ['admin'] },
            { id: 'vulnerability-db', name: '漏洞数据库', href: 'vulnerability-db.html', icon: 'fa-bug', role: ['admin', 'user'] },
            { id: 'agent-config',     name: '智能体配置', href: 'agent-config.html', icon: 'fa-sliders', role: ['admin'] },
        ]
    },

    // ========== 通知管理（admin only） ==========
    {
        id: 'notify-group',
        name: '通知管理',
        icon: 'fa-bell',
        role: ['admin'],
        children: [
            { id: 'notifiers', name: '企业微信',     href: 'notifiers.html',       icon: 'fa-comment' },
            { id: 'mail',      name: '邮件',         href: 'mail.html',            icon: 'fa-envelope' },
            { id: 'notification-rules', name: '通知规则', href: 'notification-rules.html', icon: 'fa-gear' },
            { id: 'settings-holiday', name: '节假日管理',  href: 'holiday-management.html', icon: 'fa-calendar-day' },
            { id: 'report',    name: '报告管理',     href: 'report.html',          icon: 'fa-file-alt' },
        ]
    },

    // ========== 消息中心（admin + user） ==========
    // 位置：通知管理之后，系统管理之前
    { id: 'notifications', name: '消息中心', href: 'notifications.html', icon: 'fa-inbox', role: ['admin', 'user'] },

    // ========== MCP 集成（admin 全面，user 仅能力中心）==========
    {
        id: 'mcp-group',
        name: 'MCP 集成',
        icon: 'fa-microchip',
        role: ['admin', 'user'],
        children: [
            { id: 'mcp-capabilities', name: '能力中心', href: 'mcp-capabilities.html', icon: 'fa-robot', role: ['admin', 'user'] },
            { id: 'mcp-keys',         name: '密钥管理', href: 'mcp-keys.html',         icon: 'fa-key',   role: ['admin'] },
            { id: 'mcp-logs',         name: '调用日志', href: 'mcp-logs.html',         icon: 'fa-file-lines', role: ['admin'] },
        ]
    },

    // ========== 系统管理（admin only） ==========
    {
        id: 'settings-group',
        name: '系统管理',
        icon: 'fa-gear',
        role: ['admin'],
        children: [
            { id: 'settings-config', name: '系统配置',     href: 'settings.html?tab=config',         icon: 'fa-sliders' },
            { id: 'settings-storage', name: '对象存储',     href: 'storage-config.html',              icon: 'fa-database' },
            { id: 'settings-ai',     name: 'AI 对话模板',  href: 'settings.html?tab=aitemplate',     icon: 'fa-robot' },
            { id: 'settings-review', name: '代码审查模板', href: 'settings.html?tab=reviewtemplate', icon: 'fa-code' },
            { id: 'settings-users',  name: '用户管理',     href: 'settings.html?tab=users',          icon: 'fa-user-cog' },
            { id: 'settings-logs',   name: '操作日志',     href: 'settings.html?tab=logs',           icon: 'fa-history' },
            { id: 'settings-info',   name: '系统信息',     href: 'settings.html?tab=info',           icon: 'fa-circle-info' },
        ]
    },
];

// 判断当前菜单项对指定角色是否可见（大小写不敏感，兼容数字 role）
function isMenuVisible(menuRole, userRole) {
    if (!menuRole || !Array.isArray(menuRole) || menuRole.length === 0) return true;
    const normalizedUser = String(userRole || '').trim().toLowerCase();
    if (!normalizedUser) return true;
    return menuRole.some(r => String(r || '').trim().toLowerCase() === normalizedUser);
}

// 判断当前路径是否匹配菜单项 href（支持 query 参数）
function isMenuItemActive(href, currentPath, currentSearch) {
    if (!href) return false;
    const [path, query] = href.split('?');
    if (path !== currentPath) return false;
    if (!query) return true;
    // 命中条件：当前 URL 也包含该 query 参数（用于 settings.html?tab=users）
    const params = new URLSearchParams(currentSearch || '');
    const target = new URLSearchParams(query);
    for (const [k, v] of target) {
        if (params.get(k) !== v) return false;
    }
    return true;
}
