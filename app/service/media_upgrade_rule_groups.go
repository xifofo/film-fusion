package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

var (
	ErrUpgradeRuleGroupNotFound = errors.New("洗版规则组不存在或无权限")
	ErrUpgradeRuleGroupConflict = errors.New("洗版规则组已被修改，请刷新后重试")
	ErrUpgradeRuleGroupInUse    = errors.New("洗版规则组仍被目录引用，请先解除引用")
)

type MediaUpgradeRuleGroupView struct {
	model.MediaUpgradeRuleGroup
	Policy         MediaUpgradePolicy `json:"policy"`
	DirectoryCount int                `json:"directory_count"`
}

type MediaUpgradeRuleGroupInput struct {
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Policy           MediaUpgradePolicy `json:"policy"`
	ExpectedRevision uint               `json:"expected_revision"`
}

// upgradeRuleGroupReferences 仅读取配置中的引用标识，坏的独立规则不会影响其他规则组的管理。
func upgradeRuleGroupReferences(db *gorm.DB, userID uint) (map[uint]int, error) {
	var directories []model.CloudDirectory
	if err := db.Select("upgrade_policy").Where("user_id = ? AND upgrade_policy <> ?", userID, "").Find(&directories).Error; err != nil {
		return nil, err
	}
	counts := map[uint]int{}
	for _, dir := range directories {
		var ref struct {
			GroupID uint `json:"group_id"`
		}
		if json.Unmarshal([]byte(dir.UpgradePolicy), &ref) == nil && ref.GroupID != 0 {
			counts[ref.GroupID]++
		}
	}
	return counts, nil
}

// ListMediaUpgradeRuleGroups 返回当前用户的规则组及引用目录数量。
func ListMediaUpgradeRuleGroups(db *gorm.DB, userID uint) ([]MediaUpgradeRuleGroupView, error) {
	var rows []model.MediaUpgradeRuleGroup
	if err := db.Where("user_id = ?", userID).Order("name ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts, err := upgradeRuleGroupReferences(db, userID)
	if err != nil {
		return nil, err
	}
	result := make([]MediaUpgradeRuleGroupView, 0, len(rows))
	for _, row := range rows {
		policy, err := ParseMediaUpgradePolicy(row.PolicyJSON)
		if err != nil {
			return nil, fmt.Errorf("规则组 %s 配置无效: %w", row.Name, err)
		}
		result = append(result, MediaUpgradeRuleGroupView{MediaUpgradeRuleGroup: row, Policy: policy, DirectoryCount: counts[row.ID]})
	}
	return result, nil
}

// SaveMediaUpgradeRuleGroup 使用版本号防止覆盖别处刚保存的规则；组本身不控制目录的启用开关。
func SaveMediaUpgradeRuleGroup(db *gorm.DB, userID, id uint, input MediaUpgradeRuleGroupInput) (MediaUpgradeRuleGroupView, error) {
	var result MediaUpgradeRuleGroupView
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 || utf8.RuneCountInString(input.Description) > 2000 {
		return result, errors.New("规则组名称不能为空且不超过 120 个字符，说明不超过 2000 个字符")
	}
	if input.Policy.GroupID != 0 || input.Policy.GroupRevision != 0 {
		return result, errors.New("规则组必须保存独立规则，不能引用其他规则组")
	}
	input.Policy.Enabled, input.Policy.Name = true, input.Name
	if err := ValidateMediaUpgradePolicy(input.Policy); err != nil {
		return result, err
	}
	data, err := json.Marshal(input.Policy)
	if err != nil {
		return result, err
	}
	if len(data) > 64*1024 {
		return result, errors.New("洗版策略超过 64 KiB")
	}
	row := model.MediaUpgradeRuleGroup{UserID: userID, Name: input.Name, Description: input.Description, PolicyJSON: string(data), Revision: 1}
	err = db.Transaction(func(tx *gorm.DB) error {
		if id == 0 {
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("保存规则组失败，请检查名称是否重复: %w", err)
			}
		} else {
			if input.ExpectedRevision == 0 {
				return ErrUpgradeRuleGroupConflict
			}
			row.ID, row.UpdatedAt, row.Revision = id, time.Now(), input.ExpectedRevision+1
			changed := tx.Model(&model.MediaUpgradeRuleGroup{}).Where("id = ? AND user_id = ? AND revision = ?", id, userID, input.ExpectedRevision).Updates(map[string]any{"name": row.Name, "description": row.Description, "policy_json": row.PolicyJSON, "revision": row.Revision, "updated_at": row.UpdatedAt})
			if changed.Error != nil {
				return fmt.Errorf("保存规则组失败，请检查名称是否重复: %w", changed.Error)
			}
			if changed.RowsAffected != 1 {
				var existing model.MediaUpgradeRuleGroup
				if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&existing).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrUpgradeRuleGroupNotFound
					}
					return err
				}
				return ErrUpgradeRuleGroupConflict
			}
			if err := tx.Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
				return err
			}
		}
		counts, err := upgradeRuleGroupReferences(tx, userID)
		if err != nil {
			return err
		}
		result = MediaUpgradeRuleGroupView{MediaUpgradeRuleGroup: row, Policy: input.Policy, DirectoryCount: counts[row.ID]}
		return nil
	})
	return result, err
}

// validateUpgradeRuleReference 禁止客户端用伪造快照或内联覆盖改变共享规则的含义。
func validateUpgradeRuleReference(policy MediaUpgradePolicy) error {
	if policy.GroupID == 0 {
		if policy.GroupRevision != 0 {
			return errors.New("独立策略不能包含规则组版本")
		}
		return nil
	}
	copy := policy
	copy.GroupID, copy.Enabled = 0, false
	data, _ := json.Marshal(copy)
	zero, _ := json.Marshal(MediaUpgradePolicy{})
	if string(data) != string(zero) {
		return errors.New("引用规则组时只保存启用状态和规则组 ID，请重新选择规则组")
	}
	return nil
}

// ResolveMediaUpgradePolicy 将当前用户的目录引用解析为不可变计划所需的完整规则快照。
func ResolveMediaUpgradePolicy(db *gorm.DB, userID uint, raw string) (MediaUpgradePolicy, error) {
	policy, err := ParseMediaUpgradePolicy(raw)
	if err != nil {
		return policy, err
	}
	if err = validateUpgradeRuleReference(policy); err != nil {
		return policy, err
	}
	if policy.GroupID == 0 {
		return policy, nil
	}
	var row model.MediaUpgradeRuleGroup
	if err := db.Where("id = ? AND user_id = ?", policy.GroupID, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return policy, ErrUpgradeRuleGroupNotFound
		}
		return policy, err
	}
	resolved, err := ParseMediaUpgradePolicy(row.PolicyJSON)
	if err != nil {
		return resolved, err
	}
	if resolved.GroupID != 0 || resolved.GroupRevision != 0 {
		return resolved, errors.New("规则组包含无效的嵌套引用")
	}
	resolved.Enabled, resolved.GroupID, resolved.GroupRevision = policy.Enabled, row.ID, row.Revision
	return resolved, nil
}

// LockMediaUpgradeRuleGroupReference 与目录写入处于同一事务，避免删除检查与新增引用竞态。
func LockMediaUpgradeRuleGroupReference(tx *gorm.DB, userID uint, raw string) error {
	policy, err := ParseMediaUpgradePolicy(raw)
	if err != nil {
		return err
	}
	if err = validateUpgradeRuleReference(policy); err != nil {
		return err
	}
	if policy.GroupID == 0 {
		return nil
	}
	locked := tx.Model(&model.MediaUpgradeRuleGroup{}).Where("id = ? AND user_id = ?", policy.GroupID, userID).UpdateColumn("revision", gorm.Expr("revision"))
	if locked.Error != nil {
		return locked.Error
	}
	if locked.RowsAffected != 1 {
		return ErrUpgradeRuleGroupNotFound
	}
	_, err = ResolveMediaUpgradePolicy(tx, userID, raw)
	return err
}

// DeleteMediaUpgradeRuleGroup 保留仍被目录引用的规则组，包括停用目录中的引用。
func DeleteMediaUpgradeRuleGroup(db *gorm.DB, userID, id, revision uint) error {
	if revision == 0 {
		return ErrUpgradeRuleGroupConflict
	}
	return db.Transaction(func(tx *gorm.DB) error {
		locked := tx.Model(&model.MediaUpgradeRuleGroup{}).Where("id = ? AND user_id = ? AND revision = ?", id, userID, revision).UpdateColumn("revision", gorm.Expr("revision"))
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&model.MediaUpgradeRuleGroup{}).Where("id = ? AND user_id = ?", id, userID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrUpgradeRuleGroupNotFound
			}
			return ErrUpgradeRuleGroupConflict
		}
		counts, err := upgradeRuleGroupReferences(tx, userID)
		if err != nil {
			return err
		}
		if counts[id] > 0 {
			return ErrUpgradeRuleGroupInUse
		}
		return tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.MediaUpgradeRuleGroup{}).Error
	})
}

// ValidateMediaUpgradeRuleGroupSnapshot 要求未执行计划重新确认已变更的规则，运行中任务仍使用原快照恢复。
func ValidateMediaUpgradeRuleGroupSnapshot(db *gorm.DB, userID uint, policy *MediaUpgradePolicy) error {
	if policy == nil || policy.GroupID == 0 {
		return nil
	}
	var row model.MediaUpgradeRuleGroup
	if err := db.Where("id = ? AND user_id = ?", policy.GroupID, userID).First(&row).Error; err != nil {
		return errors.New("洗版规则组已不可用，请重新预览")
	}
	if policy.GroupRevision == 0 || row.Revision != policy.GroupRevision {
		return errors.New("洗版规则组已改变，请重新预览")
	}
	return nil
}

// LockMediaUpgradeRuleGroupSnapshot 与计划认领共用事务，封闭校验期间规则被修改的窗口。
func LockMediaUpgradeRuleGroupSnapshot(tx *gorm.DB, userID uint, policy *MediaUpgradePolicy) error {
	if policy == nil || policy.GroupID == 0 {
		return nil
	}
	if policy.GroupRevision == 0 {
		return errors.New("洗版规则组缺少版本，请重新预览")
	}
	locked := tx.Model(&model.MediaUpgradeRuleGroup{}).Where("id = ? AND user_id = ? AND revision = ?", policy.GroupID, userID, policy.GroupRevision).UpdateColumn("revision", gorm.Expr("revision"))
	if locked.Error != nil {
		return locked.Error
	}
	if locked.RowsAffected != 1 {
		return errors.New("洗版规则组已改变或不可用，请重新预览")
	}
	return nil
}
