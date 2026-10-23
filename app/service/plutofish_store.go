package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const plutoFishMaxPending = 10000

func plutoFishScope(baseURL, ownerUID string) string {
	sum := sha256.Sum256([]byte(baseURL + "\x00" + ownerUID))
	return hex.EncodeToString(sum[:])
}

type PlutoFishStore struct {
	db    *gorm.DB
	scope string
}

func NewPlutoFishStore(db *gorm.DB, baseURL, ownerUID string) *PlutoFishStore {
	return &PlutoFishStore{db: db, scope: plutoFishScope(baseURL, ownerUID)}
}

func (s *PlutoFishStore) Cursor(ctx context.Context) (string, error) {
	var row model.PlutoFishCursor
	err := s.db.WithContext(ctx).Where("scope = ?", s.scope).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("无法读取 PlutoFish 持久化游标")
	}
	return row.Cursor, nil
}

// Commit atomically stores every event, the dedup records and page progress.
// SSE event commits intentionally leave the cursor alone until a cursor frame.
func (s *PlutoFishStore) Commit(ctx context.Context, events []plutofish.Event, cursor string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var pending int64
		if err := tx.Model(&model.PlutoFishEvent{}).Where("scope = ? AND state IN ?", s.scope, []string{"pending", "review"}).Count(&pending).Error; err != nil {
			return err
		}
		for _, event := range events {
			if strings.TrimSpace(event.ID) == "" {
				return errors.New("事件缺少 ID")
			}
			var count int64
			if err := tx.Model(&model.PlutoFishEvent{}).Where("scope = ? AND event_id = ?", s.scope, event.ID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if len(event.Raw) > 0 {
				payload = event.Raw
			}
			state, reason := "review", "无法识别的事件类型或 schemaVersion，需要人工检查原始事件"
			if event.SchemaVersion == 1 {
				switch event.Type {
				case "transfer.succeeded":
					if event.Status == "succeeded" && event.TransferID != "" {
						state, reason = "pending", "等待本地 STRM 处理"
					}
				case "transfer.failed":
					if event.Status == "failed" {
						state, reason = "recorded", "转存失败；未执行成功流程或重试网盘转存"
					}
				case "transfer.unknown":
					if event.Status == "unknown" {
						state, reason = "review", "转存结果待核对；未执行成功流程或重试网盘转存"
					}
				}
			}
			if state == "pending" {
				var receipts int64
				if err := tx.Model(&model.PlutoFishReceipt{}).Where("scope = ? AND transfer_id = ?", s.scope, event.TransferID).Count(&receipts).Error; err != nil {
					return err
				}
				if receipts > 0 {
					state, reason = "duplicate", "该转存任务的本地业务已经完成"
				}
			}
			if state == "pending" || state == "review" {
				pending++
				if pending > plutoFishMaxPending {
					return errors.New("待处理队列已满")
				}
			}
			row := model.PlutoFishEvent{Scope: s.scope, EventID: event.ID, TransferID: event.TransferID, Type: event.Type, SchemaVersion: event.SchemaVersion, Status: event.Status, WorkTitle: event.WorkTitle, FileName: event.FileName, OccurredAt: event.OccurredAt, Payload: string(payload), State: state, Reason: reason}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		if cursor != "" {
			return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}}, DoUpdates: clause.AssignmentColumns([]string{"cursor", "updated_at"})}).Create(&model.PlutoFishCursor{Scope: s.scope, Cursor: cursor}).Error
		}
		return nil
	})
	if err != nil {
		return errors.New("PlutoFish 收件箱提交失败或待处理队列已满；游标未推进")
	}
	return nil
}

// processOne is safe to replay after a crash between the filesystem side effect
// and this transaction: the adapter publishes deterministic content idempotently.
func (s *PlutoFishStore) processOne(ctx context.Context, handle func(context.Context, string, plutofish.Event) (PlutoFishBusinessResult, error)) (bool, error) {
	var row model.PlutoFishEvent
	err := s.db.WithContext(ctx).Where("scope = ? AND state = ?", s.scope, "pending").Order("id ASC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("无法读取 PlutoFish 待处理任务")
	}
	var receipt model.PlutoFishReceipt
	err = s.db.WithContext(ctx).Where("scope = ? AND transfer_id = ?", s.scope, row.TransferID).Take(&receipt).Error
	if err == nil {
		return true, s.db.WithContext(ctx).Model(&row).Updates(map[string]any{"state": "duplicate", "reason": "该转存任务的本地业务已经完成"}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, errors.New("无法读取 PlutoFish 业务去重记录")
	}
	var event plutofish.Event
	if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
		return false, errors.New("待处理事件无法解析；未标记完成")
	}
	result, handleErr := handle(ctx, s.scope, event)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if handleErr != nil {
		// Business errors are locally defined, sanitized reasons, never provider bodies.
		err = s.db.WithContext(ctx).Model(&row).Updates(map[string]any{"state": "review", "reason": handleErr.Error()}).Error
		return true, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.PlutoFishReceipt{Scope: s.scope, TransferID: row.TransferID, EventID: row.EventID, OutputPath: result.LocalPath}).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"state": "completed", "reason": result.Message, "output_path": result.LocalPath}).Error; err != nil {
			return err
		}
		return tx.Model(&model.PlutoFishEvent{}).Where("scope = ? AND transfer_id = ? AND id <> ? AND type = ? AND schema_version = 1 AND status = ? AND state IN ?", s.scope, row.TransferID, row.ID, "transfer.succeeded", "succeeded", []string{"pending", "review"}).Updates(map[string]any{"state": "duplicate", "reason": "该转存任务的本地业务已经完成"}).Error
	})
	return true, err
}
