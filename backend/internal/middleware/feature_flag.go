package middleware

import (
	"sync/atomic"

	"github.com/ai-optimizer/backend/config"
)

// FeatureFlag 全局 Feature Flag 状态（原子操作，支持运行时热切换）
// 多租户改造：6 层渐进式开关，Layer 0 = 全局关闭（默认）
type FeatureFlag struct {
	layer int32 // 原子变量，使用 atomic.LoadInt32/StoreInt32
}

// SetLayer 设置 Feature Flag 层（线程安全）
func (ff *FeatureFlag) SetLayer(layer int) {
	atomic.StoreInt32(&ff.layer, int32(layer))
}

// Layer 获取当前 Feature Flag 层（线程安全）
func (ff *FeatureFlag) Layer() int {
	return int(atomic.LoadInt32(&ff.layer))
}

// IsEnabled 判断指定层是否已启用（>= targetLayer 视为启用）
func (ff *FeatureFlag) IsEnabled(targetLayer int) bool {
	return ff.Layer() >= targetLayer
}

// IsReadIsolationEnabled Layer 1+: 读隔离（查询带 org_id WHERE）
func (ff *FeatureFlag) IsReadIsolationEnabled() bool {
	return ff.IsEnabled(1)
}

// IsWriteInterceptEnabled Layer 2+: 写拦截（拒绝 org_id=0 的写入）
func (ff *FeatureFlag) IsWriteInterceptEnabled() bool {
	return ff.IsEnabled(2)
}

// IsOrgMgmtEnabled Layer 3+: 组织管理 API（创建/切换组织）
func (ff *FeatureFlag) IsOrgMgmtEnabled() bool {
	return ff.IsEnabled(3)
}

// IsOrgUISwitcherEnabled Layer 4+: 组织切换 UI
func (ff *FeatureFlag) IsOrgUISwitcherEnabled() bool {
	return ff.IsEnabled(4)
}

// IsFullMultitenancyEnabled Layer 5: 完全启用
func (ff *FeatureFlag) IsFullMultitenancyEnabled() bool {
	return ff.IsEnabled(5)
}

// IsOrgIDRequired Layer 2+: org_id 必须非零
func (ff *FeatureFlag) IsOrgIDRequired() bool {
	return ff.IsWriteInterceptEnabled()
}

// GlobalFeatureFlag 全局 Feature Flag 实例
var GlobalFeatureFlag = &FeatureFlag{}

// InitFeatureFlag 从配置初始化 Feature Flag
func InitFeatureFlag(cfg *config.Config) {
	if cfg == nil {
		GlobalFeatureFlag.SetLayer(0)
		return
	}
	layer := cfg.MultiTenancyLayer
	if layer < 0 {
		layer = 0
	}
	if layer > 5 {
		layer = 5
	}
	GlobalFeatureFlag.SetLayer(layer)
}
