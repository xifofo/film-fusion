package service

import (
	"context"
	"errors"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

// Review closes a local review reminder after explicit administrator confirmation.
// The upstream outcome, original payload/reason, cursor and success receipt are
// unchanged. A later succeeded event for this transfer can still be processed.
func (s *PlutoFishService) Review(ctx context.Context, id, userID uint) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.businessMu.Lock()
	defer s.businessMu.Unlock()
	value, err := s.load(ctx)
	if err != nil {
		return err
	}
	if userID == 0 || value.UserID != userID || value.Config.OwnerUID == "" {
		return errors.New("只能核对当前管理员配置的 PlutoFish 通知")
	}
	scope := plutoFishScope(value.Config.BaseURL, value.Config.OwnerUID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.PlutoFishEvent
		if err := tx.Where("id = ? AND scope = ?", id, scope).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("当前账号下没有对应的待核对通知")
			}
			return errors.New("无法读取待核对通知，请稍后重试")
		}
		if row.State == "reviewed" {
			return nil // Repeated clicks/replayed requests preserve the first audit record.
		}
		if row.State != "review" {
			return errors.New("仅待人工核对的通知可以标记已核对，请刷新列表")
		}
		result := tx.Model(&model.PlutoFishEvent{}).Where("id = ? AND scope = ? AND state = ?", row.ID, scope, "review").Updates(map[string]any{
			"state": "reviewed", "reviewed_at": time.Now().UTC(), "reviewed_by": userID,
		})
		if result.Error != nil {
			return errors.New("核对记录保存失败，通知仍待核对")
		}
		if result.RowsAffected != 1 {
			return errors.New("通知状态已变化，请刷新列表")
		}
		return nil
	})
}
