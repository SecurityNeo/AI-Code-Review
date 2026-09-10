/**
 * org-select.js — 企业微信通讯录风格的组织选择器
 * 兼容所有调用 loadOrgSelect / getOrgOverrideHeaders / injectOrgId 的页面
 */
(function () {
    'use strict';

    const ORG_LIST_KEY       = 'org_list';
    const PICKER_CLASS       = 'org-picker';
    const TRIGGER_CLASS      = 'org-picker-trigger';
    const POPOVER_CLASS      = 'org-picker-popover';

    /* ────────────── 基础工具 ────────────── */

    function escapeHtml(t) {
        if (!t) return '';
        return t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    }

    function isSuperAdmin() {
        const userInfo = (() => {
            try { return JSON.parse(localStorage.getItem('user_info') || '{}'); }
            catch (e) { return {}; }
        })();
        return userInfo.current_org_role === 'super_admin';
    }

    function getCurrentUserInfo() {
        try { return JSON.parse(localStorage.getItem('user_info') || '{}'); }
        catch (e) { return {}; }
    }

    async function waitForApiReady() {
        for (let i = 0; i < 50; i++) {
            if (typeof window.apiFetch === 'function') return true;
            await new Promise(function (resolve) { setTimeout(resolve, 100); });
        }
        return false;
    }

    function walkTreeNodes(nodes, level, result) {
        result = result || [];
        level  = level  || 0;
        nodes.forEach(function (n) {
            result.push({
                id:        n.id,
                name:      n.name,
                code:      n.code,
                status:    n.status,
                parent_id: n.parent_id,
                level:     level,
                role:      n.role || 'developer',
                children:  n.children || []
            });
            if (n.children && n.children.length > 0) {
                walkTreeNodes(n.children, level + 1, result);
            }
        });
        return result;
    }

    /* ────────────── 样式注入（仅一次） ────────────── */

    function injectStyles() {
        if (document.getElementById('org-picker-styles')) return;
        const style = document.createElement('style');
        style.id = 'org-picker-styles';
        style.textContent =
            '.org-picker { position: relative; display: inline-block; width: 100%; font-size: 14px; }' +
            '.org-picker-trigger {' +
            '  display: flex; align-items: center; justify-content: space-between;' +
            '  width: 100%; padding: 8px 12px; border: 1px solid #d1d5db; border-radius: 8px;' +
            '  background: #fff; cursor: pointer; transition: border-color .15s, box-shadow .15s;' +
            '  min-height: 38px; box-sizing: border-box;' +
            '}' +
            '.org-picker-trigger:hover { border-color: #9ca3af; }' +
            '.org-picker-trigger.active { border-color: #3b82f6; box-shadow: 0 0 0 2px rgba(59,130,246,.2); }' +
            '.org-picker-text {' +
            '  flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;' +
            '  color: #111827; font-size: 14px;' +
            '}' +
            '.org-picker-text.placeholder { color: #9ca3af; }' +
            '.org-picker-arrow {' +
            '  width: 0; height: 0; margin-left: 8px;' +
            '  border-left: 4px solid transparent; border-right: 4px solid transparent;' +
            '  border-top: 5px solid #6b7280; transition: transform .2s;' +
            '}' +
            '.org-picker-trigger.active .org-picker-arrow { transform: rotate(180deg); }' +
            '.org-picker-clear {' +
            '  display: none; margin-left: 6px; padding: 2px; color: #9ca3af; cursor: pointer; font-size: 12px;' +
            '}' +
            '.org-picker-trigger:hover .org-picker-clear { display: inline-block; }' +
            '.org-picker-clear:hover { color: #ef4444; }' +
            '.org-picker-popover {' +
            '  position: absolute; top: calc(100% + 4px); left: 0; z-index: 9999;' +
            '  width: 100%; min-width: 260px; max-height: 380px;' +
            '  background: #fff; border: 1px solid #e5e7eb; border-radius: 8px;' +
            '  box-shadow: 0 10px 25px -5px rgba(0,0,0,.1), 0 8px 10px -6px rgba(0,0,0,.1);' +
            '  display: none; flex-direction: column; overflow: hidden;' +
            '}' +
            '.org-picker-popover.open { display: flex; }' +
            '.org-picker-search {' +
            '  padding: 10px 12px 6px; border-bottom: 1px solid #f3f4f6;' +
            '}' +
            '.org-picker-search input {' +
            '  width: 100%; padding: 6px 10px; border: 1px solid #e5e7eb;' +
            '  border-radius: 6px; font-size: 13px; outline: none;' +
            '  box-sizing: border-box;' +
            '}' +
            '.org-picker-search input:focus { border-color: #3b82f6; }' +
            '.org-picker-tree { flex: 1; overflow-y: auto; padding: 4px 0; }' +
            '.org-picker-node {' +
            '  display: flex; align-items: center; padding: 7px 12px 7px 0;' +
            '  cursor: pointer; transition: background .1s; user-select: none;' +
            '}' +
            '.org-picker-node:hover { background: #f3f4f6; }' +
            '.org-picker-node.selected { background: #eff6ff; color: #1d4ed8; }' +
            '.org-picker-node.selected .org-picker-label { font-weight: 600; }' +
            '.org-picker-node.hidden { display: none; }' +
            '.org-picker-toggle {' +
            '  width: 18px; height: 18px; display: flex; align-items: center; justify-content: center;' +
            '  margin-right: 2px; cursor: pointer; color: #9ca3af; font-size: 10px; flex-shrink: 0;' +
            '}' +
            '.org-picker-toggle:hover { color: #4b5563; }' +
            '.org-picker-indent { display: inline-block; width: 18px; flex-shrink: 0; }' +
            '.org-picker-icon { margin-right: 6px; color: #f59e0b; font-size: 14px; flex-shrink: 0; }' +
            '.org-picker-label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }' +
            '.org-picker-empty { padding: 24px; text-align: center; color: #9ca3af; font-size: 13px; }';
        document.head.appendChild(style);
    }

    /* ────────────── 组件核心 ────────────── */

    function ensureWrapped(sel) {
        if (sel.dataset.orgPickerWrapped === '1') return sel.closest('.' + PICKER_CLASS);

        injectStyles();

        const wrapper   = document.createElement('div');
        wrapper.className = PICKER_CLASS;
        /* 把原始的 w-* 宽度 class 搬运到 wrapper，让弹框/表单里的 w-full、w-48 等生效 */
        const widthCls = Array.from(sel.classList).find(c => /^w-/.test(c));
        if (widthCls) wrapper.classList.add(widthCls);
        sel.parentNode.insertBefore(wrapper, sel);
        wrapper.appendChild(sel);

        sel.style.display = 'none';
        sel.dataset.orgPickerWrapped = '1';

        // 触发器
        const trigger = document.createElement('div');
        trigger.className = TRIGGER_CLASS;
        trigger.innerHTML =
            '<span class="org-picker-text placeholder">请选择所属组织</span>' +
            '<span class="org-picker-clear">&times;</span>' +
            '<span class="org-picker-arrow"></span>';
        wrapper.insertBefore(trigger, sel);

        // 弹出面板
        const popover = document.createElement('div');
        popover.className = POPOVER_CLASS;
        popover.innerHTML =
            '<div class="org-picker-search"><input type="text" placeholder="搜索组织名称..."></div>' +
            '<div class="org-picker-tree"></div>';
        wrapper.appendChild(popover);

        /* 事件绑定 */
        trigger.addEventListener('click', function (e) {
            if (e.target.closest('.org-picker-clear')) {
                e.stopPropagation();
                setPickerValue(sel, '', '');
                return;
            }
            togglePopover(wrapper);
        });

        // 搜索
        const searchInput = popover.querySelector('.org-picker-search input');
        searchInput.addEventListener('input', function () {
            filterTree(wrapper, this.value.trim().toLowerCase());
        });

        // 点击外部关闭
        document.addEventListener('click', function (e) {
            if (!wrapper.contains(e.target)) closePopover(wrapper);
        });

        // Esc 关闭
        document.addEventListener('keydown', function (e) {
            if (e.key === 'Escape' && isOpen(wrapper)) closePopover(wrapper);
        });

        return wrapper;
    }

    function togglePopover(wrapper) {
        if (isOpen(wrapper)) closePopover(wrapper);
        else openPopover(wrapper);
    }

    function isOpen(wrapper) {
        return wrapper.querySelector('.' + POPOVER_CLASS).classList.contains('open');
    }

    function openPopover(wrapper) {
        // 先关闭其它的
        document.querySelectorAll('.' + POPOVER_CLASS + '.open').forEach(function (p) {
            if (p !== wrapper.querySelector('.' + POPOVER_CLASS)) p.classList.remove('open');
        });
        document.querySelectorAll('.' + TRIGGER_CLASS + '.active').forEach(function (t) {
            if (t !== wrapper.querySelector('.' + TRIGGER_CLASS)) t.classList.remove('active');
        });

        wrapper.querySelector('.' + POPOVER_CLASS).classList.add('open');
        wrapper.querySelector('.' + TRIGGER_CLASS).classList.add('active');
        const inp = wrapper.querySelector('.org-picker-search input');
        setTimeout(function () { inp.focus(); inp.select(); }, 10);
    }

    function closePopover(wrapper) {
        wrapper.querySelector('.' + POPOVER_CLASS).classList.remove('open');
        wrapper.querySelector('.' + TRIGGER_CLASS).classList.remove('active');
    }

    /* ────────────── 树渲染 ────────────── */

    function buildTreeHTML(nodes, selectedId, level) {
        level = level || 0;
        var html = '';
        nodes.forEach(function (n) {
            var hasChildren = n.children && n.children.length > 0;
            var isSelected  = String(n.id) === String(selectedId);
            var indentW     = level * 18;

            html += '<div class="org-picker-node' + (isSelected ? ' selected' : '') + '" data-id="' + n.id + '" data-parent-id="' + (n.parent_id || 0) + '" data-level="' + level + '" data-has-children="' + (hasChildren ? '1' : '0') + '">';
            // 同行缩进
            html += '<span class="org-picker-indent" style="width:' + indentW + 'px"></span>';
            // 展开/折叠箭头
            if (hasChildren) {
                html += '<span class="org-picker-toggle" onclick="event.stopPropagation();window.__orgPickerToggle(this)"><i class="fas fa-chevron-right"></i></span>';
            } else {
                html += '<span class="org-picker-indent" style="width:18px"></span>';
            }
            // 图标
            html += '<span class="org-picker-icon"><i class="far ' + (hasChildren ? 'fa-folder' : 'fa-file-alt') + '"></i></span>';
            // 名称
            html += '<span class="org-picker-label">' + escapeHtml(n.name) + '</span>';
            html += '</div>';

            if (hasChildren) {
                html += '<div class="org-picker-children" style="display:none">';
                html += buildTreeHTML(n.children, selectedId, level + 1);
                html += '</div>';
            }
        });
        return html;
    }

    window.__orgPickerToggle = function (el) {
        var node   = el.closest('.org-picker-node');
        var nextEl = node.nextElementSibling;
        if (!nextEl || !nextEl.classList.contains('org-picker-children')) return;
        var icon   = el.querySelector('i');
        if (nextEl.style.display === 'none') {
            nextEl.style.display = 'block';
            icon.className = 'fas fa-chevron-down';
        } else {
            nextEl.style.display = 'none';
            icon.className = 'fas fa-chevron-right';
        }
    };

    /* ────────────── 搜索过滤 ────────────── */

    function filterTree(wrapper, kw) {
        var tree   = wrapper.querySelector('.org-picker-tree');
        var nodes  = tree.querySelectorAll('.org-picker-node');
        if (!kw) {
            nodes.forEach(function (n) { n.classList.remove('hidden'); });
            tree.querySelectorAll('.org-picker-children').forEach(function (c) { c.style.display = 'none'; });
            tree.querySelectorAll('.org-picker-toggle i').forEach(function (i) { i.className = 'fas fa-chevron-right'; });
            // 展开选中路径
            var selected = tree.querySelector('.org-picker-node.selected');
            if (selected) expandPath(selected);
            return;
        }

        // 先全部隐藏
        nodes.forEach(function (n) { n.classList.add('hidden'); });
        tree.querySelectorAll('.org-picker-children').forEach(function (c) { c.style.display = 'none'; });

        // 匹配名称的节点显示
        var matchedIds = new Set();
        nodes.forEach(function (n) {
            var label = n.querySelector('.org-picker-label').textContent.toLowerCase();
            if (label.indexOf(kw) !== -1) {
                n.classList.remove('hidden');
                matchedIds.add(n.dataset.id);
            }
        });

        // 显示匹配节点的所有祖先
        nodes.forEach(function (n) {
            if (matchedIds.has(n.dataset.id)) {
                var pid = n.dataset.parentId;
                while (pid && pid !== '0') {
                    var parentNode = tree.querySelector('.org-picker-node[data-id="' + pid + '"]');
                    if (parentNode) {
                        parentNode.classList.remove('hidden');
                        var childWrap = parentNode.nextElementSibling;
                        if (childWrap && childWrap.classList.contains('org-picker-children')) {
                            childWrap.style.display = 'block';
                            var toggle = parentNode.querySelector('.org-picker-toggle i');
                            if (toggle) toggle.className = 'fas fa-chevron-down';
                        }
                        pid = parentNode.dataset.parentId;
                    } else {
                        break;
                    }
                }
            }
        });
    }

    function expandPath(node) {
        var wrapper = node.closest('.' + PICKER_CLASS);
        var tree    = wrapper.querySelector('.org-picker-tree');
        var pid     = node.dataset.parentId;
        while (pid && pid !== '0') {
            var parentNode = tree.querySelector('.org-picker-node[data-id="' + pid + '"]');
            if (parentNode) {
                var childWrap = parentNode.nextElementSibling;
                if (childWrap && childWrap.classList.contains('org-picker-children')) {
                    childWrap.style.display = 'block';
                    var toggle = parentNode.querySelector('.org-picker-toggle i');
                    if (toggle) toggle.className = 'fas fa-chevron-down';
                }
                pid = parentNode.dataset.parentId;
            } else { break; }
        }
    }

    /* ────────────── 值读写 ────────────── */

    function getNodePath(flatList, nodeId) {
        if (!nodeId || nodeId === '0') return '';
        var map = {};
        flatList.forEach(function (n) { map[n.id] = n; });
        var parts = [];
        var cur   = map[nodeId];
        while (cur) {
            parts.unshift(cur.name);
            cur = map[cur.parent_id];
        }
        return parts.join(' / ');
    }

    function setPickerValue(sel, id, displayText) {
        sel.value = id;
        var wrapper = sel.closest('.' + PICKER_CLASS);
        var textEl  = wrapper.querySelector('.org-picker-text');
        if (!id) {
            textEl.textContent = '请选择所属组织';
            textEl.classList.add('placeholder');
        } else {
            textEl.textContent = displayText || id;
            textEl.classList.remove('placeholder');
        }
        // 同步 select 的 option（供 injectOrgId / getOrgOverrideHeaders 读取）
        var exist = sel.querySelector('option[value="' + id + '"]');
        if (!exist && id) {
            exist = document.createElement('option');
            exist.value = id;
            exist.textContent = displayText || id;
            sel.appendChild(exist);
        }
        if (exist) exist.selected = true;

        // 更新树的高亮
        var tree = wrapper.querySelector('.org-picker-tree');
        if (tree) {
            tree.querySelectorAll('.org-picker-node.selected').forEach(function (n) { n.classList.remove('selected'); });
            var target = tree.querySelector('.org-picker-node[data-id="' + id + '"]');
            if (target) {
                target.classList.add('selected');
                expandPath(target);
            }
        }
        // 触发自定义 change 事件，供外部监听联动
        sel.dispatchEvent(new CustomEvent('change', { bubbles: true }));
    }

    /* ────────────── 数据加载 ────────────── */

    async function fetchTree() {
        const token = localStorage.getItem('auth_token') || '';
        try {
            const res = await fetch('/api/v1/organizations', {
                headers: { 'Authorization': 'Bearer ' + token },
                cache: 'no-cache'
            });
            if (res.ok) {
                const payload = await res.json();
                return payload.data || [];
            }
        } catch (e) { console.error('fetchTree failed', e); }
        return [];
    }

    function flattenTree(nodes, level, result) {
        result = result || [];
        level  = level  || 0;
        nodes.forEach(function (n) {
            result.push({ id: n.id, name: n.name, parent_id: n.parent_id || 0, level: level });
            if (n.children && n.children.length) flattenTree(n.children, level + 1, result);
        });
        return result;
    }

    /* ═══════════════════════════════════════
       对外 API（保持与旧版完全一致）
       ═══════════════════════════════════════ */

    /**
     * 初始化/刷新组织选择器
     * @param {string} selectId   原始 <select> 的 id
     * @param {string|number} defaultValue  默认选中的组织 ID
     */
    window.loadOrgSelect = async function (selectId, defaultValue) {
        selectId = selectId || 'orgSelect';
        var sel = document.getElementById(selectId);
        if (!sel) return;

        var wrapper = ensureWrapped(sel);
        var userInfo = getCurrentUserInfo();
        var isSuper = userInfo.current_org_role === 'super_admin';

        // 只有 super_admin 才显示组织选择器
        // org_admin / developer 完全隐藏（不占空间），自动绑定当前组织
        if (!isSuper) {
            wrapper.style.display = 'none';
            var readOnlyWrap = wrapper.querySelector('.org-picker-readonly');
            if (readOnlyWrap) readOnlyWrap.style.display = 'none';
            if (userInfo.current_org_id) {
                sel.value = String(userInfo.current_org_id);
            }
            return;
        }

        // super_admin：正常加载组织树
        var readOnlyWrap = wrapper.querySelector('.org-picker-readonly');
        if (readOnlyWrap) readOnlyWrap.style.display = 'none';

        // 加载树数据
        var treeData = await fetchTree();
        if (!treeData.length) {
            wrapper.querySelector('.org-picker-tree').innerHTML =
                '<div class="org-picker-empty">无法获取组织列表</div>';
            return;
        }

        var flatList = flattenTree(treeData);
        wrapper.dataset.flatList = JSON.stringify(flatList);

        // 渲染树
        var treeEl = wrapper.querySelector('.org-picker-tree');
        treeEl.innerHTML = buildTreeHTML(treeData, defaultValue);

        // 展开到选中项
        var selectedNode = treeEl.querySelector('.org-picker-node.selected');
        if (selectedNode) expandPath(selectedNode);

        // 绑定节点点击
        treeEl.querySelectorAll('.org-picker-node').forEach(function (node) {
            node.addEventListener('click', function () {
                var id   = this.dataset.id;
                // 只取当前节点名称，不展示完整路径
                var name = this.querySelector('.org-picker-label').textContent;
                setPickerValue(sel, id, name);
                closePopover(wrapper);
            });
        });

        // 回显默认值（只显示名称）
        if (defaultValue) {
            var node = flatList.find(function (n) { return String(n.id) === String(defaultValue); });
            setPickerValue(sel, defaultValue, node ? node.name : '');
        } else {
            setPickerValue(sel, '', '');
        }
    };

    /**
     * 获取组织覆盖请求头
     */
    window.getOrgOverrideHeaders = function (selectId) {
        selectId = selectId || 'orgSelect';
        var userInfo = getCurrentUserInfo();

        // super_admin：直接以选择器为准（localStorage 不会随页面选择同步更新）
        if (userInfo.current_org_role === 'super_admin') {
            var sel = document.getElementById(selectId);
            if (sel && sel.value) {
                return { 'X-Org-Id': String(sel.value) };
            }
            // 选择器无值时 fallback 到 localStorage
            if (window.getCurrentOrg) {
                var currentOrg = window.getCurrentOrg();
                if (currentOrg && currentOrg.id) {
                    return { 'X-Org-Id': String(currentOrg.id) };
                }
            }
            return {};
        }

        // org_admin / developer：以当前组织上下文为准（页面通常隐藏选择器）
        if (window.getCurrentOrg) {
            var currentOrg = window.getCurrentOrg();
            if (currentOrg && currentOrg.id) {
                return { 'X-Org-Id': String(currentOrg.id) };
            }
        }
        if (userInfo.current_org_id) {
            return { 'X-Org-Id': String(userInfo.current_org_id) };
        }
        var sel = document.getElementById(selectId);
        if (sel && sel.value) {
            return { 'X-Org-Id': String(sel.value) };
        }
        return {};
    };

    /**
     * 将选中的组织ID原地注入到 payload 对象
     * 原则：非 super_admin 强制使用当前组织ID，忽略选择器值
     */
    window.injectOrgId = function (payload, selectId) {
        selectId = selectId || 'orgSelect';
        var userInfo = getCurrentUserInfo();
        if (!userInfo.current_org_role) return;

        // super_admin：优先从选择器读取
        if (userInfo.current_org_role === 'super_admin') {
            var sel = document.getElementById(selectId);
            if (sel && sel.value) {
                if (payload) payload.org_id = parseInt(sel.value);
            }
            return;
        }

        // org_admin / developer：强制绑定当前组织
        if (payload && userInfo.current_org_id) {
            payload.org_id = parseInt(userInfo.current_org_id);
        }
    };
})();
