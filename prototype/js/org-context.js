/**
 * OrgContext - 多租户组织上下文管理
 * 负责：组织信息加载、切换、同步、X-Org-Id 注入
 */
(function() {
    'use strict';

    const ORG_KEY = 'current_org';
    const ORG_LIST_KEY = 'org_list';

    // ========== 组织信息 API ==========

    /**
     * 加载当前用户关联的组织列表
     */
    window.loadUserOrganizations = async function() {
        try {
            const token = window.getToken();
            if (!token) return [];

            const res = await window.apiFetch('/api/v1/users/me');
            if (!res.ok) {
                if (res.status === 403) {
                    // 用户未分配组织
                    window.showToast('用户未分配组织，请联系管理员', 'error');
                    setTimeout(() => {
                        window.location.href = '/pending-assignment.html';
                    }, 2000);
                }
                return [];
            }

            const payload = await res.json();
            const data = payload.data || {};
            if (!data.organizations || !Array.isArray(data.organizations)) {
                return [];
            }

            // 缓存组织列表
            localStorage.setItem(ORG_LIST_KEY, JSON.stringify(data.organizations));

            // 如果没设置当前组织，使用 default_org_id
            const current = window.getCurrentOrg();
            if (!current && data.organizations.length > 0) {
                const defaultOrg = data.organizations.find(o => o.is_default) || data.organizations[0];
                window.setCurrentOrg(defaultOrg);
            }

            return data.organizations;
        } catch (e) {
            console.error('loadUserOrganizations failed:', e);
            return [];
        }
    };

    /**
     * 获取当前选中的组织
     */
    window.getCurrentOrg = function() {
        try {
            return JSON.parse(localStorage.getItem(ORG_KEY) || 'null');
        } catch (e) {
            return null;
        }
    };

    /**
     * 设置当前组织
     */
    window.setCurrentOrg = function(org) {
        if (!org || !org.id) return;
        localStorage.setItem(ORG_KEY, JSON.stringify(org));
        // 广播组织变更事件（多标签页同步）
        try {
            localStorage.setItem('_org_change_event', JSON.stringify({ id: org.id, ts: Date.now() }));
        } catch (e) {}
    };

    /**
     * 切换组织并刷新页面
     */
    window.switchOrganization = async function(orgID) {
        const orgList = JSON.parse(localStorage.getItem(ORG_LIST_KEY) || '[]');
        const target = orgList.find(o => o.id === orgID);
        if (!target) {
            window.showToast('组织不存在', 'error');
            return;
        }

        // 检查是否有未保存的表单
        const dirtyForms = document.querySelectorAll('form[data-dirty="true"], .unsaved-changes');
        if (dirtyForms.length > 0) {
            if (!confirm('您有未保存的更改，切换组织将丢失这些更改，是否继续？')) {
                return;
            }
        }

        window.setCurrentOrg(target);
        window.showToast(`已切换到组织：${escapeHtml(target.name)}`, 'success');

        // 刷新页面以加载新组织的数据
        setTimeout(() => {
            window.location.reload();
        }, 500);
    };

    /**
     * 渲染组织选择器到下拉菜单
     */
    window.renderOrgSwitcher = function() {
        const container = document.getElementById('orgSwitcher');
        if (!container) return;

        const current = window.getCurrentOrg();
        const orgList = JSON.parse(localStorage.getItem(ORG_LIST_KEY) || '[]');

        if (orgList.length <= 1) {
            container.style.display = 'none';
            return;
        }

        container.style.display = 'block';

        // 使用 createElement 避免 XSS
        const select = document.createElement('select');
        select.className = 'bg-gray-800 text-white text-sm rounded px-2 py-1 border border-gray-700 focus:outline-none focus:border-blue-500';
        select.onchange = function() {
            window.switchOrganization(parseInt(this.value));
        };

        orgList.forEach(org => {
            const option = document.createElement('option');
            option.value = org.id;
            option.textContent = org.name || `组织 ${org.id}`;
            if (current && org.id === current.id) {
                option.selected = true;
            }
            select.appendChild(option);
        });

        container.innerHTML = '';
        container.appendChild(select);
    };

    // ========== 多标签页同步 ==========

    // 监听 storage 事件，同步组织切换
    window.addEventListener('storage', function(e) {
        if (e.key === '_org_change_event') {
            try {
                const event = JSON.parse(e.newValue || '{}');
                const current = window.getCurrentOrg();
                if (current && event.id !== current.id) {
                    window.showToast('检测到其他标签页切换了组织，即将刷新同步', 'info');
                    setTimeout(() => window.location.reload(), 2000);
                }
            } catch (err) {}
        }
    });

    // ========== 初始化 ==========

    // 页面加载后自动加载组织信息
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function() {
            if (window.location.pathname !== '/login.html') {
                window.loadUserOrganizations().then(() => {
                    window.renderOrgSwitcher();
                });
            }
        });
    } else {
        if (window.location.pathname !== '/login.html') {
            window.loadUserOrganizations().then(() => {
                window.renderOrgSwitcher();
            });
        }
    }

})();
