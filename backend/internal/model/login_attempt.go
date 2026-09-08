package model

import (
	"time"

	"go.uber.org/zap"
)

const (
	LoginMaxAttempts  = 5                    // 最大失败次数
	LoginLockDuration = 30 * time.Minute     // 锁定时长
	LoginResetWindow  = 15 * time.Minute     // 失败计数重置窗口（连续失败超过此时间则重新计数）
)

// CheckLoginLock 检查指定用户名/IP是否被锁定
// 返回 (isLocked bool, remainingSecs int, err error)
func CheckLoginLock(username, ip string) (bool, int, error) {
	var attempt LoginAttempt
	err := DB.Where("username = ?", username).First(&attempt).Error
	if err != nil {
		// 无记录 = 未锁定
		return false, 0, nil
	}

	// 如果距离上次失败超过重置窗口，自动清零
	if time.Since(attempt.LastFailedAt) > LoginResetWindow {
		attempt.FailedCount = 0
		attempt.LockedUntil = nil
		DB.Save(&attempt)
		return false, 0, nil
	}

	// 检查是否处于锁定状态
	if attempt.LockedUntil != nil && time.Now().Before(*attempt.LockedUntil) {
		remaining := int(time.Until(*attempt.LockedUntil).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		return true, remaining, nil
	}

	return false, 0, nil
}

// RecordFailedLogin 记录一次失败登录
func RecordFailedLogin(username, ip string) {
	var attempt LoginAttempt
	err := DB.Where("username = ?", username).First(&attempt).Error
	if err != nil {
		// 创建新记录
		now := time.Now()
		lockUntil := now.Add(LoginLockDuration)
		attempt = LoginAttempt{
			Username:     username,
			IP:           ip,
			FailedCount:  1,
			LastFailedAt: now,
			LockedUntil:  nil,
		}
		if attempt.FailedCount >= LoginMaxAttempts {
			attempt.LockedUntil = &lockUntil
		}
		if err := DB.Create(&attempt).Error; err != nil {
			zap.L().Error("create login attempt failed", zap.Error(err))
		}
		return
	}

	// 更新失败记录
	attempt.FailedCount++
	attempt.LastFailedAt = time.Now()
	attempt.IP = ip
	if attempt.FailedCount >= LoginMaxAttempts {
		lockUntil := time.Now().Add(LoginLockDuration)
		attempt.LockedUntil = &lockUntil
	}
	if err := DB.Save(&attempt).Error; err != nil {
		zap.L().Error("save login attempt failed", zap.Error(err))
	}
}

// ClearFailedLogin 登录成功后清除失败记录
func ClearFailedLogin(username string) {
	DB.Where("username = ?", username).Delete(&LoginAttempt{})
}

// CleanupExpiredLoginAttempts 清理过期的登录尝试记录（保留7天用于审计）
func CleanupExpiredLoginAttempts() {
	cutoff := time.Now().AddDate(0, 0, -7)
	if err := DB.Where("last_failed_at < ?", cutoff).Delete(&LoginAttempt{}).Error; err != nil {
		zap.L().Error("cleanup login attempts failed", zap.Error(err))
	}
}
