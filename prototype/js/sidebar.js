// js/sidebar.js
// 通用动态侧边栏渲染 v2
// 配合 css/theme.css 使用 .cg-* 类

(function () {
    'use strict';

    // 统一将相对路径转为绝对路径（避免在 /pages/ 下时路径错乱）
    function normalizeHref(href) {
        if (!href) return href;
        if (href.startsWith('http') || href.startsWith('/') || href.startsWith('#')) return href;
        return '/' + href;
    }

    // 折叠组状态持久化
    const EXPANDED_KEY = 'sidebar_expanded_groups';

    function getExpandedGroups() {
        try {
            return JSON.parse(localStorage.getItem(EXPANDED_KEY) || '[]');
        } catch (e) {
            return [];
        }
    }

    function persistExpanded(arr) {
        try { localStorage.setItem(EXPANDED_KEY, JSON.stringify(arr)); } catch (e) {}
    }

    function toggleGroup(groupId) {
        const expanded = getExpandedGroups();
        const idx = expanded.indexOf(groupId);
        if (idx >= 0) {
            expanded.splice(idx, 1);
        } else {
            expanded.push(groupId);
        }
        persistExpanded(expanded);
        renderSidebar(window.activePageId);
    }

    window.activePageId = null;
    window.toggleGroup = toggleGroup;

    // 主题切换：在 light / dark / system 三态间循环
    window.toggleTheme = function () {
        if (!window.ThemeManager) return;
        const order = ['light', 'dark', 'system'];
        const cur = window.ThemeManager.getPref();
        const next = order[(order.indexOf(cur) + 1) % order.length];
        window.ThemeManager.setPref(next);
        // 重新渲染 sidebar（更新图标 + 可能影响视觉权重）
        if (window.activePageId) renderSidebar(window.activePageId);
    };

    window.renderSidebar = function (activeId) {
        window.activePageId = activeId;
        const sidebar = document.getElementById('sidebar');
        if (!sidebar) return;

        const userInfo = (() => {
            try { return JSON.parse(localStorage.getItem('user_info') || '{}'); }
            catch (e) { return {}; }
        })();
        // 直接使用 current_org_role 进行菜单权限判断
        const userRole = (userInfo.current_org_role || 'developer').trim().toLowerCase();
        // 直接使用持久化的 expanded 列表，不再每次 renderSidebar 时强制展开当前 active 的组
        // —— 否则用户手动折叠后会被立即重新展开，看起来"无法折叠"
        const expandedGroups = getExpandedGroups();

        const currentPath = window.location.pathname.split('/').pop() || '';
        const currentSearch = window.location.search || '';

        // ===== Logo 区 =====
        let html = `
        <div class="cg-sidebar-logo p-6 border-b" style="border-color: var(--cg-sidebar-border);">
            <div style="display: flex; align-items: center; gap: 12px;">
                <div style="background: linear-gradient(135deg, var(--cg-brand-primary), var(--cg-brand-secondary)); width: 38px; height: 38px; border-radius: 10px; display: flex; align-items: center; justify-content: center; box-shadow: 0 4px 12px rgba(79, 70, 229, 0.25); flex-shrink: 0;">
                    <i class="fas fa-shield-halved" style="color: white; font-size: 16px;"></i>
                </div>
                <div style="flex: 1; min-width: 0;">
                    <h1 style="font-size: 16px; font-weight: 700; color: var(--cg-sidebar-text-active); letter-spacing: -0.01em; line-height: 1.2;">CodeGuard</h1>
                    <p style="font-size: 11px; color: var(--cg-text-tertiary); margin-top: 2px;">代码智能门禁</p>
                </div>
            </div>
        </div>
        <nav style="flex: 1; padding: 12px 8px; overflow-y: auto;">
        `;

        // ===== 菜单项 =====
        const visibleMenuNames = [];
        MENU_CONFIG.forEach(menu => {
            const visible = isMenuVisible(menu.role, userRole);
            if (!visible) return;
            visibleMenuNames.push(menu.name);

            if (menu.children && menu.children.length > 0) {
                // 折叠组
                const isExpanded = expandedGroups.includes(menu.id);
                const hasActiveChild = menu.children.some(child =>
                    child.id === activeId || isMenuItemActive(child.href, currentPath, currentSearch)
                );

                html += `
                <div class="cg-sidebar-group">
                    <div class="cg-sidebar-group-toggle ${isExpanded ? 'expanded' : ''}" onclick="toggleGroup('${menu.id}')">
                        <div style="display: flex; align-items: center; gap: 12px;">
                            <i class="fas ${menu.icon}" style="width:18px;text-align:center;font-size:14px;flex-shrink:0;"></i>
                            <span>${menu.name}</span>
                        </div>
                        <i class="fas fa-chevron-right arrow"></i>
                    </div>
                    <div class="cg-sidebar-group-children ${isExpanded ? 'expanded' : ''}">
                `;

                menu.children.forEach(child => {
                    if (!isMenuVisible(child.role, userRole)) return;
                    const isActive = child.id === activeId ||
                        isMenuItemActive(child.href, currentPath, currentSearch);
                    const href = normalizeHref(child.href);
                    html += `
                    <a href="${href}" class="cg-sidebar-child ${isActive ? 'active' : ''}">
                        <i class="fas ${child.icon}"></i>
                        <span>${child.name}</span>
                    </a>
                    `;
                });

                html += `</div></div>`;
            } else {
                // 普通菜单项
                const isActive = menu.id === activeId ||
                    isMenuItemActive(menu.href, currentPath, currentSearch);
                const href = normalizeHref(menu.href);
                html += `
                <a href="${href}" class="cg-sidebar-item ${isActive ? 'active' : ''}">
                    <i class="fas ${menu.icon}"></i>
                    <span>${menu.name}</span>
                </a>
                `;
            }
        });

        html += `</nav>`;

        // ===== 用户区 =====
        // 布局：第一行 用户名（角色），第二行 组织名
        const displayName = userInfo.display_name || userInfo.username || '用户';
        const roleLabels = {
            super_admin: '系统管理员',
            org_admin:   '组织管理员',
            developer:   '开发者'
        };
        const roleLabel = roleLabels[userRole] || userRole;
        const orgName = userInfo.current_org_name || '';
        // 第一行：用户名（角色），整体单行截断
        const userLine = escapeHtml(displayName) + '<span style="opacity:0.55;margin-left:4px;font-size:12px;font-weight:400;">（' + escapeHtml(roleLabel) + '）</span>';
        // 第二行：组织名，超长截断
        const orgLine = orgName
            ? `<span style="display:inline-block;vertical-align:bottom;max-width:100%;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;" title="${escapeHtml(orgName)}">${escapeHtml(orgName)}</span>`
            : '<span style="opacity:0.4;">未分配组织</span>';
        html += `
        <div style="padding: 12px; border-top: 1px solid var(--cg-sidebar-border);">
            <div onclick="toggleUserMenu()" style="display: flex; align-items: center; gap: 10px; padding: 8px; border-radius: 8px; cursor: pointer; transition: background var(--cg-transition);" onmouseover="this.style.background='var(--cg-sidebar-item-hover)'" onmouseout="this.style.background='transparent'">
                <div style="width: 32px; height: 32px; border-radius: 50%; background: linear-gradient(135deg, #4f46e5, #06b6d4); display: flex; align-items: center; justify-content: center; color: white; font-weight: 600; font-size: 13px; flex-shrink:0;">
                    ${escapeHtml(displayName.charAt(0).toUpperCase())}
                </div>
                <div style="flex: 1; min-width: 0;">
                    <div id="currentUser" style="font-size: 13px; font-weight: 500; color: var(--cg-sidebar-text-active); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">${userLine}</div>
                    <div id="currentOrg" style="font-size: 11px; color: var(--cg-text-tertiary); margin-top:1px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;">${orgLine}</div>
                </div>
                <i class="fas fa-ellipsis-vertical" style="font-size: 12px; color: var(--cg-text-tertiary); flex-shrink: 0;"></i>
            </div>
            <div id="userMenu" class="hidden" style="margin-top: 8px; background: var(--cg-bg-surface); border-radius: 8px; padding: 4px; border: 1px solid var(--cg-border-default); box-shadow: var(--cg-shadow-md);">
                <button onclick="showChangePasswordModal()" style="display: flex; align-items: center; gap: 12px; width: 100%; background: transparent; border: none; padding: 8px 12px; font-size: 13px; color: var(--cg-text-primary); border-radius: 6px; cursor: pointer; transition: background var(--cg-transition);" onmouseover="this.style.background='var(--cg-bg-elevated)'" onmouseout="this.style.background='transparent'">
                    <i class="fas fa-key" style="width: 18px; text-align: center; color: var(--cg-text-secondary);"></i>
                    <span>修改密码</span>
                </button>
                <button onclick="logout()" style="display: flex; align-items: center; gap: 12px; width: 100%; background: transparent; border: none; padding: 8px 12px; font-size: 13px; color: var(--cg-text-primary); border-radius: 6px; cursor: pointer; transition: background var(--cg-transition);" onmouseover="this.style.background='var(--cg-bg-elevated)'" onmouseout="this.style.background='transparent'">
                    <i class="fas fa-sign-out-alt" style="width: 18px; text-align: center; color: var(--cg-text-secondary);"></i>
                    <span>退出登录</span>
                </button>
            </div>
        </div>
        `;

        console.debug('[sidebar] userRole=' + userRole + ' visibleMenus=[' + visibleMenuNames.join(', ') + ']');

        sidebar.innerHTML = html;
        // 给外层 aside 容器加 cg-sidebar 类，让 theme.css 的样式生效
        sidebar.classList.add('cg-sidebar');
        sidebar.classList.remove('bg-slate-900', 'text-white');

        // 注入消息中心未读徽标
        refreshNotificationBadge();
    };

    window.refreshNotificationBadge = async function() {
        try {
            const res = await apiFetch('/api/v1/notifications/unread-count');
            if (!res.ok) return;
            const json = await res.json();
            const count = (typeof json.data === 'number') ? json.data : (json.data?.count || 0);
            // 查找消息中心菜单项（href 为 notifications.html）
            const sidebar = document.getElementById('sidebar');
            if (!sidebar) return;
            const links = sidebar.querySelectorAll('a[href="notifications.html"]');
            links.forEach(link => {
                let badge = link.querySelector('.notif-badge');
                if (!badge) {
                    badge = document.createElement('span');
                    badge.className = 'notif-badge';
                    badge.style.cssText = 'margin-left:auto;font-size:11px;font-weight:700;min-width:18px;height:18px;border-radius:9px;display:inline-flex;align-items:center;justify-content:center;background:#ef4444;color:#fff;padding:0 5px;';
                    link.style.display = 'flex';
                    link.style.alignItems = 'center';
                    link.appendChild(badge);
                }
                badge.textContent = count > 99 ? '99+' : count;
                badge.style.display = count > 0 ? 'inline-flex' : 'none';
            });
        } catch (e) { /* ignore */ }
    };

    // ========== 全局铃铛徽标（可拖拽、位置持久化） ==========
    window.injectGlobalBell = function() {
        if (document.getElementById('globalBell')) return;
        const bell = document.createElement('div');
        bell.id = 'globalBell';
        bell.className = 'fixed z-[60]';
        bell.style.cursor = 'move';

        // 恢复上次保存的位置（使用 right 方便从右边缘定位）
        const saved = localStorage.getItem('cg_bell_pos');
        if (saved) {
            try {
                const pos = JSON.parse(saved);
                if (typeof pos.top === 'number') {
                    bell.style.top = pos.top + 'px';
                    bell.style.right = typeof pos.right === 'number' ? pos.right + 'px' : '24px';
                }
            } catch (e) {
                // 解析失败回退默认
            }
        }

        // 没有存储过或解析失败 → 默认位置
        if (!bell.style.top) {
            bell.style.top = '16px';
            bell.style.right = '24px';
        }

        bell.innerHTML = `
            <a href="notifications.html" class="relative block w-10 h-10 bg-white rounded-full shadow border flex items-center justify-center hover:bg-gray-50 transition select-none">
                <i class="fas fa-bell text-gray-500 text-lg pointer-events-none"></i>
                <span id="globalBellBadge" class="absolute top-0 right-0 bg-red-500 text-white text-[10px] font-bold px-1.5 rounded-full hidden pointer-events-none" style="min-width:16px;height:16px;display:none;align-items:center;justify-content:center;">0</span>
            </a>
        `;

        let isDragging = false;
        let startX, startY;
        let hasDragged = false;
        let rectAtStart;

        bell.addEventListener('mousedown', function(e) {
            if (e.button !== 0) return; // 仅左键
            isDragging = true;
            hasDragged = false;
            startX = e.clientX;
            startY = e.clientY;
            rectAtStart = bell.getBoundingClientRect();
            e.preventDefault(); // 避免触发 a 的默认行为导致跳转
        });

        function onMove(e) {
            if (!isDragging) return;
            const dx = e.clientX - startX;
            const dy = e.clientY - startY;
            if (Math.abs(dx) > 5 || Math.abs(dy) > 5) {
                hasDragged = true;
            }
            let newTop = rectAtStart.top + dy;
            let newLeft = rectAtStart.left + dx;

            // 边界限制
            const maxLeft = window.innerWidth - bell.offsetWidth;
            const maxTop = window.innerHeight - bell.offsetHeight;
            newLeft = Math.max(0, Math.min(newLeft, maxLeft));
            newTop = Math.max(0, Math.min(newTop, maxTop));

            bell.style.top = newTop + 'px';
            bell.style.left = newLeft + 'px';
            bell.style.right = 'auto';
        }

        function onUp(e) {
            if (!isDragging) return;
            isDragging = false;
            if (hasDragged) {
                // 保存当前位置到 localStorage
                const rect = bell.getBoundingClientRect();
                localStorage.setItem('cg_bell_pos', JSON.stringify({
                    top: rect.top,
                    right: window.innerWidth - rect.right
                }));
            }
            document.removeEventListener('mousemove', onMove);
            document.removeEventListener('mouseup', onUp);
        }

        bell.addEventListener('mousedown', function() {
            document.addEventListener('mousemove', onMove);
            document.addEventListener('mouseup', onUp);
        });

        // 阻止拖拽导致的链接跳转
        const link = bell.querySelector('a');
        if (link) {
            link.addEventListener('click', function(e) {
                if (hasDragged) {
                    e.preventDefault();
                    e.stopPropagation();
                    hasDragged = false;
                }
            });
        }

        document.body.appendChild(bell);
    };

    // 覆盖 refreshNotificationBadge 以同时更新全局铃铛
    const _originalRefresh = window.refreshNotificationBadge;
    window.refreshNotificationBadge = async function() {
        await _originalRefresh();
        try {
            const res = await apiFetch('/api/v1/notifications/unread-count');
            if (!res.ok) return;
            const json = await res.json();
            const count = (typeof json.data === 'number') ? json.data : (json.data?.count || 0);
            const badge = document.getElementById('globalBellBadge');
            if (badge) {
                badge.textContent = count > 99 ? '99+' : count;
                badge.style.display = count > 0 ? 'flex' : 'none';
            }
        } catch (e) {}
    };

    function escapeHtml(s) {
        if (s == null) return '';
        const div = document.createElement('div');
        div.textContent = String(s);
        return div.innerHTML;
    }

    // 默认在 DOMReady 时渲染：
    // 1. 优先使用页面显式设置的 window.activePageId
    // 2. 否则从 URL 自动推断当前菜单项 id
    document.addEventListener('DOMContentLoaded', function () {
        // 先检测当前激活的菜单 id（无论是显式设置还是从 URL 推断）
        let activeId = window.activePageId || null;
        if (!activeId) {
            const path = window.location.pathname.split('/').pop() || '';
            const search = window.location.search || '';
            for (const m of MENU_CONFIG) {
                if (m.children) {
                    for (const c of m.children) {
                        if (isMenuItemActive(c.href, path, search)) { activeId = c.id; break; }
                    }
                } else if (isMenuItemActive(m.href, path, search)) {
                    activeId = m.id;
                }
                if (activeId) break;
            }
        }

        // 首次进入：自动展开包含当前 active 的组（仅这一次，不会被 renderSidebar 反复触发）
        if (activeId) {
            const expanded = getExpandedGroups();
            let changed = false;
            for (const m of MENU_CONFIG) {
                if (m.children && m.children.some(c => c.id === activeId)) {
                    if (!expanded.includes(m.id)) {
                        expanded.push(m.id);
                        changed = true;
                    }
                }
            }
            if (changed) persistExpanded(expanded);
        }

        renderSidebar(activeId);
        injectGlobalBell();
        refreshNotificationBadge();

        // 启动通知徽标轮询（每 5 分钟，降低日志和服务器压力）
        setInterval(refreshNotificationBadge, 300000);
    });
})();
