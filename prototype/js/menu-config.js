// js/menu-config.js
// 菜单配置 v2：6 个折叠组，admin/user 差异化
// 顶层 admin: 首页 / 任务列表 / 数据洞察 / AI 评审 / 通知管理 / 系统管理
// 顶层 user: 首页 / 任务列表 / 数据洞察
const MENU_CONFIG = [
    // ========== 公共菜单 ==========
    {
        id: 'dashboard',
        name: '首页',
        href: 'statistics.html',
        icon: 'fa-chart-line',
        role: ['admin', 'user']
    },
    {
        id: 'tasks',
        name: '任务列表',
        href: 'tasks.html',
        icon: 'fa-tasks',
        role: ['admin', 'user']
    },

        { id: 'notifications', name: '消息中心', href: 'notifications.html', icon: 'fa-bell', role: ['admin', 'user'] },

        // ========== 数据洞察 ==========
    {
        id: 'insights-group',
        name: '数据洞察',
        icon: 'fa-chart-bar',
        role: ['admin', 'user'],
        children: [
            { id: 'mr-stats',    name: '代码提交统计', href: 'mr-stats.html',    icon: 'fa-code-branch', role: ['admin', 'user'] },
            { id: 'rule-stats',  name: '规则命中统计', href: 'rule-stats.html',  icon: 'fa-bullseye',    role: ['admin', 'user'] },
            { id: 'token-usage', name: 'Token 用量',   href: 'token-usage.html', icon: 'fa-coins',       role: ['admin'] },
        ]
    },

    // ========== AI 评审（admin only） ==========
    {
        id: 'ai-review-group',
        name: 'AI 评审',
        icon: 'fa-robot',
        role: ['admin'],
        children: [
            { id: 'projects',     name: '项目管理',   href: 'projects.html',     icon: 'fa-folder-open' },
            { id: 'review-rules', name: '评审规则库', href: 'review-rules.html', icon: 'fa-shield-halved' },
            { id: 'incubator',    name: '规则孵化台', href: 'incubator.html',    icon: 'fa-flask' },
            { id: 'pools',        name: '任务资源池', href: 'pools.html',        icon: 'fa-server' },
            { id: 'models',       name: '大模型管理', href: 'models.html',       icon: 'fa-microchip' },
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
            { id: 'mappings',  name: '成员映射',     href: 'member-mappings.html', icon: 'fa-users' },
            { id: 'report',    name: '报告管理',     href: 'report.html',          icon: 'fa-file-alt' },
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
            { id: 'settings-ai',     name: 'AI 对话模板',  href: 'settings.html?tab=aitemplate',     icon: 'fa-robot' },
            { id: 'settings-review', name: '代码审查模板', href: 'settings.html?tab=reviewtemplate', icon: 'fa-code' },
            { id: 'settings-users',  name: '用户管理',     href: 'settings.html?tab=users',          icon: 'fa-user-cog' },
            { id: 'settings-logs',   name: '操作日志',     href: 'settings.html?tab=logs',           icon: 'fa-history' },
            { id: 'settings-info',   name: '系统信息',     href: 'settings.html?tab=info',           icon: 'fa-circle-info' },
        ]
    },
];

// 判断当前菜单项对指定角色是否可见
function isMenuVisible(menuRole, userRole) {
    if (!menuRole) return true;
    return menuRole.includes(userRole);
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
