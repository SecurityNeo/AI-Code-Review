// 全局认证拦截脚本 - 必须在其他脚本之前加载
(function() {
    'use strict';
    
    const TOKEN_KEY = 'auth_token';
    const USER_KEY  = 'user_info';

    // 全局 Toast 辅助（如果页面未定义则提供默认实现）
    window.showToast = window.showToast || function(message, type = 'info') {
        const colors = { success: 'bg-green-500', error: 'bg-red-500', info: 'bg-blue-500' };
        const toast = document.createElement('div');
        toast.className = `fixed top-4 right-4 ${colors[type] || colors.info} text-white px-4 py-2 rounded-lg shadow-lg z-50 transition-opacity duration-300`;
        toast.textContent = message;
        document.body.appendChild(toast);
        setTimeout(() => { toast.style.opacity = '0'; setTimeout(() => toast.remove(), 300); }, 3000);
    };

    // ========== Token 相关函数 ==========
    window.getToken = function() {
        return localStorage.getItem(TOKEN_KEY);
    };

    window.redirectToLogin = function() {
        localStorage.removeItem(TOKEN_KEY);
        localStorage.removeItem(USER_KEY);
        if (window.location.pathname !== '/login.html') {
            window.location.href = '/login.html';
        }
    };

    // ========== 角色权限检查（基于 org_users.role，废弃 users.role） ==========
    // isAdmin = 组织管理员及以上（super_admin / org_admin）
    window.isAdmin = function() {
        try {
            if (window.getCurrentOrg) {
                const currentOrg = window.getCurrentOrg();
                if (currentOrg && currentOrg.role) {
                    return ['super_admin', 'org_admin'].includes(currentOrg.role);
                }
            }
            const info = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
            return ['super_admin', 'org_admin'].includes(info.current_org_role);
        } catch(e) {
            return false;
        }
    };
    // isSystemAdmin = 仅系统管理员（super_admin）
    window.isSystemAdmin = function() {
        try {
            if (window.getCurrentOrg) {
                const currentOrg = window.getCurrentOrg();
                if (currentOrg && currentOrg.role) {
                    return currentOrg.role === 'super_admin';
                }
            }
            const info = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
            return info.current_org_role === 'super_admin';
        } catch(e) {
            return false;
        }
    };
    // isUser 废弃：角色不再只有 user/admin 二元区分，统一用 isAdmin 判定
    window.isUser = function() {
        return !window.isAdmin();
    };

    // ========== 页面加载后自动隐藏/显示管理员专属元素 ==========
    // 多租户改造：在 OrgContext 初始化完成后调用，确保 currentOrg.role 可用
    window.applyAdminVisibility = function() {
        const adminEls = document.querySelectorAll('[data-admin-only]');
        adminEls.forEach(el => {
            if (isAdmin()) {
                // 管理员：恢复默认显示（移除可能存在的 display:none）
                el.style.display = '';
            } else {
                // 非管理员：隐藏
                el.style.display = 'none';
            }
        });
    };
    // 普通用户：限制作者筛选框为"所有作者"置灰，默认选中当前用户
    window.restrictAuthorFilterToSelf = function() {
        try {
            const info = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
            if (!window.isUser() || !info.gitlab_username) return;
            const filter = document.getElementById('authorFilter');
            if (!filter) return;
            // 保留"所有作者"但置灰不可用，默认选中自己
            // 多租户改造：使用 createElement 替代 innerHTML，防止 XSS
            filter.innerHTML = '';
            const allOpt = document.createElement('option');
            allOpt.value = '';
            allOpt.textContent = '所有作者';
            allOpt.disabled = true;
            filter.appendChild(allOpt);
            const selfOpt = document.createElement('option');
            selfOpt.value = info.gitlab_username;
            selfOpt.textContent = info.gitlab_username;
            selfOpt.selected = true;
            filter.appendChild(selfOpt);
            filter.title = '仅显示您自己的数据';
        } catch(e) {}
    };

    window.checkAuth = function() {
        if (!getToken()) {
            redirectToLogin();
            return false;
        }
        return true;
    };

    // ========== 全局 fetch 拦截 ==========
    const _origFetch = window.fetch;
    
    window.fetch = async function(url, options) {
        const token = getToken();
        const urlStr = (typeof url === 'string') ? url : (url && typeof url.url === 'string') ? url.url : '';
        const isApi    = urlStr.includes('/api/');
        const isLogin  = urlStr.endsWith('/login');
        const isLogout = urlStr.endsWith('/logout');
        
        // API 请求（排除登录/登出）自动加 token
        if (isApi && !isLogin && !isLogout && token) {
            const opts = options || {};
            const headers = Object.assign({}, opts.headers || {});
            // 避免重复添加
            if (!headers['Authorization'] && !headers['authorization']) {
                headers['Authorization'] = 'Bearer ' + token;
            }
            // 多租户改造：自动注入 X-Org-Id
            if (!headers['X-Org-Id'] && !headers['x-org-id']) {
                const currentOrg = window.getCurrentOrg && window.getCurrentOrg();
                if (currentOrg && currentOrg.id) {
                    headers['X-Org-Id'] = String(currentOrg.id);
                }
            }
            options = Object.assign({}, opts, { headers });
        }
        
        // 调用原生 fetch
        const res = await _origFetch(url, options);
        
        // 401 自动跳转
        if (res.status === 401 && isApi && !isLogin) {
            redirectToLogin();
        }
        
        return res;
    };
    
    // apiFetch 独立实现，确保始终携带 Token + Org ID
    window.apiFetch = async function(url, options) {
        const token = getToken();
        const opts = options || {};
        const urlStr = (typeof url === 'string') ? url : (url && typeof url.url === 'string') ? url.url : '';
        const isLogin  = urlStr.endsWith('/login');

        // 确保有 headers 对象
        const headers = Object.assign({}, opts.headers || {});

        // 非登录接口且未设置 Authorization 时自动注入
        if (!isLogin && token) {
            if (!headers['Authorization'] && !headers['authorization']) {
                headers['Authorization'] = 'Bearer ' + token;
            }
        }

        // 多租户改造：自动注入 X-Org-Id Header（优先使用 OrgContext 中的当前组织）
        if (!isLogin && !headers['X-Org-Id'] && !headers['x-org-id']) {
            const currentOrg = window.getCurrentOrg && window.getCurrentOrg();
            if (currentOrg && currentOrg.id) {
                headers['X-Org-Id'] = String(currentOrg.id);
            }
        }

        const newOptions = Object.assign({}, opts, { headers });
        const res = await _origFetch(url, newOptions);

        // 401 自动跳转登录页
        if (res.status === 401 && !isLogin) {
            redirectToLogin();
        }
        return res;
    };

    // ========== 用户功能 ==========
    window.logout = function() {
        const token = getToken();
        if (token) {
            _origFetch('/api/v1/logout', {
                method: 'POST',
                headers: { 'Authorization': 'Bearer ' + token }
            }).catch(() => {});
        }
        redirectToLogin();
    };

    // 修改密码弹窗
    window.showChangePasswordModal = function() {
        const modal = document.getElementById('changePasswordModal');
        if (!modal) return;
        modal.classList.remove('hidden');
        modal.classList.add('flex');
        ['cp-old','cp-new','cp-confirm'].forEach(id => {
            const el = document.getElementById(id);
            if (el) el.value = '';
        });
        const err = document.getElementById('cp-error');
        if (err) err.classList.add('hidden');
    };

    window.hideChangePasswordModal = function() {
        const modal = document.getElementById('changePasswordModal');
        if (modal) {
            modal.classList.add('hidden');
            modal.classList.remove('flex');
        }
    };

    window.doChangePassword = async function() {
        const oldPw = document.getElementById('cp-old')?.value || '';
        const newPw = document.getElementById('cp-new')?.value || '';
        const confirmPw = document.getElementById('cp-confirm')?.value || '';
        const errorEl = document.getElementById('cp-error');

        if (!oldPw || !newPw || !confirmPw) {
            errorEl.textContent = '请填写所有字段';
            errorEl.classList.remove('hidden');
            return;
        }
        if (newPw.length < 6) {
            errorEl.textContent = '新密码至少6位';
            errorEl.classList.remove('hidden');
            return;
        }
        if (newPw !== confirmPw) {
            errorEl.textContent = '两次输入的新密码不一致';
            errorEl.classList.remove('hidden');
            return;
        }

        try {
            const res = await fetch('/api/v1/users/password', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ old_password: oldPw, new_password: newPw })
            });
            if (res.ok) {
                showToast('密码修改成功，请重新登录', 'success');
                logout();
            } else {
                const data = await res.json();
                errorEl.textContent = data.error || '修改失败';
                errorEl.classList.remove('hidden');
            }
        } catch (e) {
            errorEl.textContent = e.message || '网络错误';
            errorEl.classList.remove('hidden');
        }
    };

    window.toggleUserMenu = function() {
        const menu = document.getElementById('userMenu');
        if (menu) menu.classList.toggle('hidden');
    };

    window.togglePasswordVisibility = function(inputId, btn) {
        const input = document.getElementById(inputId);
        if (!input) return;
        const isHidden = input.type === 'password';
        input.type = isHidden ? 'text' : 'password';
        const icon = btn.querySelector('i');
        if (icon) {
            icon.className = isHidden ? 'fas fa-eye-slash' : 'fas fa-eye';
        }
    };

    function createChangePasswordModal() {
        if (document.getElementById('changePasswordModal')) return;
        const modal = document.createElement('div');
        modal.id = 'changePasswordModal';
        // 使用 Tailwind hidden 类确保默认隐藏，不依赖 .modal CSS
        modal.className = 'fixed inset-0 bg-black/50 items-center justify-center z-50 hidden';
        modal.innerHTML = `
            <div class="bg-white rounded-xl w-[400px] p-6 shadow-xl">
                <div class="flex items-center justify-between mb-4">
                    <h3 class="font-bold text-lg"><i class="fas fa-key text-blue-600 mr-2"></i>修改密码</h3>
                    <button onclick="hideChangePasswordModal()" class="text-gray-400 hover:text-gray-600"><i class="fas fa-times"></i></button>
                </div>
                <div class="space-y-4">
                    <div>
                        <label class="block text-sm font-medium text-gray-700 mb-1">旧密码</label>
                        <div class="relative">
                            <input type="password" id="cp-old" class="w-full px-3 py-2 pr-10 border rounded-lg text-sm focus:outline-none focus:border-blue-500">
                            <button type="button" class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600" onclick="togglePasswordVisibility('cp-old',this)" tabindex="-1"><i class="fas fa-eye"></i></button>
                        </div>
                    </div>
                    <div>
                        <label class="block text-sm font-medium text-gray-700 mb-1">新密码</label>
                        <div class="relative">
                            <input type="password" id="cp-new" class="w-full px-3 py-2 pr-10 border rounded-lg text-sm focus:outline-none focus:border-blue-500" placeholder="至少6位">
                            <button type="button" class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600" onclick="togglePasswordVisibility('cp-new',this)" tabindex="-1"><i class="fas fa-eye"></i></button>
                        </div>
                    </div>
                    <div>
                        <label class="block text-sm font-medium text-gray-700 mb-1">确认新密码</label>
                        <div class="relative">
                            <input type="password" id="cp-confirm" class="w-full px-3 py-2 pr-10 border rounded-lg text-sm focus:outline-none focus:border-blue-500">
                            <button type="button" class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600" onclick="togglePasswordVisibility('cp-confirm',this)" tabindex="-1"><i class="fas fa-eye"></i></button>
                        </div>
                    </div>
                    <div id="cp-error" class="hidden text-red-500 text-sm text-center"></div>
                </div>
                <div class="flex justify-end gap-3 mt-6">
                    <button onclick="hideChangePasswordModal()" class="px-4 py-2 border rounded-lg text-sm hover:bg-gray-50">取消</button>
                    <button onclick="doChangePassword()" class="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm hover:bg-blue-700">确认修改</button>
                </div>
            </div>`;
        document.body.appendChild(modal);
    }

    // 初始化
    // OAuth 回调处理：URL 带有 ?token=xxx 时自动保存
    async function handleOAuthCallback() {
        const urlParams = new URLSearchParams(window.location.search);
        const token = urlParams.get('token');
        if (token) {
            localStorage.setItem(TOKEN_KEY, token);
            // 清理 URL 中的 token
            window.history.replaceState({}, document.title, window.location.pathname);
            // 刷新用户信息（必须等待完成后再渲染菜单）
            await refreshUserInfo();
            return true;
        }
        return false;
    }

    // 从后端拉取最新用户信息（含 role / display_name）
    async function refreshUserInfo() {
        try {
            const token = getToken();
            if (!token) return;
            const res = await _origFetch('/api/v1/users/me', {
                headers: { 'Authorization': 'Bearer ' + token }
            });
            if (res.ok) {
                const data = await res.json();
                if (data.data) {
                    // 合并现有缓存（保留 token）
                    const existing = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
                    const merged = Object.assign({}, existing, data.data);
                    merged.token = token; // 确保 token 不被覆盖
                    localStorage.setItem(USER_KEY, JSON.stringify(merged));
                    // 隐藏所有 admin-only 元素
                    applyAdminVisibility();
                    // 重新渲染侧边栏（确保角色过滤生效）
                    if (typeof window.renderSidebar === 'function') {
                        window.renderSidebar(window.activePageId);
                    }
                }
            }
        } catch(e) {}
    }

    async function init() {
        if (window.location.pathname === '/login.html') return;

        // 优先处理 OAuth 回调（异步等待用户信息刷新）
        const isOAuth = await handleOAuthCallback();
        if (!isOAuth && !checkAuth()) {
            return;
        }

        // 每次页面加载时刷新用户信息
        await refreshUserInfo();

        try {
            const info = JSON.parse(localStorage.getItem(USER_KEY) || '{}');
            const el = document.getElementById('currentUser');
            if (el) {
                // sidebar.js 已用 innerHTML 渲染过（含标签）就不再覆盖
                if (!el.innerHTML.includes('<')) {
                    el.textContent = info.display_name || info.username || '管理员';
                }
            }

            // 更新数据洞察页面的当前组织标签
            const orgLabel = document.getElementById('currentOrgLabel');
            if (orgLabel && window.getCurrentOrg) {
                const org = window.getCurrentOrg();
                if (org && org.name) {
                    orgLabel.textContent = org.name;
                }
            }

            // 角色与页面权限控制：管理员默认进入管理员控制台，非管理员不能访问管理员控制台
            const path = window.location.pathname;
            if (isAdmin() && (path === '/developer-dashboard.html' || path === '/' || path === '/index.html')) {
                window.location.href = '/admin-dashboard.html';
                return;
            }
            if (!isAdmin() && path === '/admin-dashboard.html') {
                window.location.href = '/developer-dashboard.html';
                return;
            }
            // MCP 密钥管理与调用日志仅对管理员开放
            if (!isAdmin() && (path === '/mcp-keys.html' || path === '/mcp-logs.html')) {
                window.location.href = '/mcp-capabilities.html';
                return;
            }
        } catch(e) {}
        // ========== 多租户改造：动态加载组织上下文与 API 封装 ==========
        (function loadOrgScripts() {
            function injectScript(src) {
                return new Promise(function(resolve, reject) {
                    var s = document.createElement('script');
                    s.src = src;
                    s.async = false;
                    s.onload = resolve;
                    s.onerror = reject;
                    document.head.appendChild(s);
                });
            }
            injectScript('/js/api-fetch.js?v=9')
                .then(function() { return injectScript('/js/org-context.js?v=9'); })
                .then(function() {
                    if (window.loadUserOrganizations) {
                        return window.loadUserOrganizations();
                    }
                })
                .then(function() {
                    // 通知 sidebar 重新渲染（权限可能因组织角色变化）
                    if (typeof window.renderSidebar === 'function') {
                        window.renderSidebar(window.activePageId);
                    }
                })
                .catch(function(e) { console.warn('org-scripts load failed:', e); });
        })();

        createChangePasswordModal();
        // 若当前在通知管理子页面，自动展开通知管理菜单
        const path = window.location.pathname;
        if (path === '/notifiers.html' || path === '/mail.html' || path === '/report.html') {
            const menu = document.getElementById('notifyMenu');
            const icon = document.getElementById('notifyMenuIcon');
            if (menu) menu.classList.remove('hidden');
            if (icon) icon.style.transform = 'rotate(180deg)';
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();

// ========== 通知管理菜单折叠（全局作用域） ==========
function toggleNotifyMenu() {
    const menu = document.getElementById('notifyMenu');
    const icon = document.getElementById('notifyMenuIcon');
    if (!menu) return;
    if (menu.classList.contains('hidden')) {
        menu.classList.remove('hidden');
        if (icon) icon.style.transform = 'rotate(180deg)';
    } else {
        menu.classList.add('hidden');
        if (icon) icon.style.transform = '';
    }
}

// ========== 全局工具函数 ==========
/**
 * 将文本转义为安全的 HTML，防止 XSS
 */
function escapeHtml(text) {
    if (text == null) return '';
    const str = String(text);
    return str
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#039;');
}
