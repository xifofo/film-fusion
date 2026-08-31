package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	automationGuardMaxKeyBytes       = 4096
	automationGuardMaxNamespaceRunes = 80
	automationGuardMinTTLSeconds     = 60
	automationGuardMaxTTLSeconds     = 365 * 24 * 60 * 60
	automationGuardDefaultTTLSeconds = 7 * 24 * 60 * 60
	automationGuardMaxWindowSeconds  = 30 * 24 * 60 * 60
	automationGuardMaxRateLimit      = 10000
	automationRateAlgorithm          = "fixed_window"
	automationDedupRetryCommitted    = "retry_committed"
)

var errAutomationRateContention = errors.New("限流计数并发更新，请重试")

// SQLite cannot reliably upgrade many concurrent deferred read transactions
// to writers. A small striped lock removes that local-process lock storm while
// the database uniqueness and compare-and-swap checks remain authoritative
// across restarts (and across processes, should the deployment ever add one).
var automationRateLimitStripes [256]sync.Mutex

type automationGuardIdentity struct {
	ScopeType string
	ScopeID   uint
	Namespace string
	KeyHash   string
}

func validateAutomationDeduplicateConfig(config map[string]any) error {
	if _, exists := config["key"]; !exists {
		return errors.New("必须配置去重键")
	}
	if err := validateAutomationGuardScopeConfig(config); err != nil {
		return err
	}
	if err := validateAutomationGuardNormalize(config); err != nil {
		return err
	}
	if _, err := automationGuardConfigInt(config, "ttl_seconds", automationGuardDefaultTTLSeconds, automationGuardMinTTLSeconds, automationGuardMaxTTLSeconds); err != nil {
		return err
	}
	if value, exists := config["refresh_on_duplicate"]; exists {
		if _, ok := value.(bool); !ok {
			return errors.New("重复命中刷新有效期配置必须是布尔值")
		}
	}
	return nil
}

func validateAutomationRateLimitConfig(config map[string]any) error {
	if _, exists := config["key"]; !exists {
		return errors.New("必须配置限流键")
	}
	if err := validateAutomationGuardScopeConfig(config); err != nil {
		return err
	}
	if err := validateAutomationGuardNormalize(config); err != nil {
		return err
	}
	if _, err := automationGuardConfigInt(config, "limit", 1, 1, automationGuardMaxRateLimit); err != nil {
		return err
	}
	windowSeconds, err := automationGuardConfigInt(config, "window_seconds", 60, 1, automationGuardMaxWindowSeconds)
	if err != nil {
		return err
	}
	if _, err := automationGuardConfigInt(config, "max_wait_seconds", windowSeconds, 1, automationGuardMaxWindowSeconds); err != nil {
		return err
	}
	behavior := strings.ToLower(automationConfigString(config, "behavior"))
	if behavior == "" {
		behavior = "defer"
	}
	if behavior != "defer" && behavior != "branch" {
		return errors.New("限流行为必须是 defer/branch")
	}
	return nil
}

func validateAutomationGuardScopeConfig(config map[string]any) error {
	scope := strings.ToLower(automationConfigString(config, "scope"))
	if scope == "" {
		scope = "workflow"
	}
	if scope != "source" && scope != "workflow" && scope != "global" {
		return errors.New("作用域必须是 source/workflow/global")
	}
	namespace := strings.TrimSpace(automationConfigString(config, "namespace"))
	if scope == "global" && namespace == "" {
		return errors.New("全局作用域必须配置命名空间")
	}
	if namespace != "" {
		return validateAutomationGuardNamespace(namespace)
	}
	return nil
}

func validateAutomationGuardNormalize(config map[string]any) error {
	normalize := strings.ToLower(automationConfigString(config, "normalize"))
	if normalize == "" {
		normalize = "trim"
	}
	if normalize != "none" && normalize != "trim" && normalize != "trim_lower" {
		return errors.New("键标准化方式必须是 none/trim/trim_lower")
	}
	return nil
}

func validateAutomationGuardNamespace(namespace string) error {
	runes := []rune(strings.TrimSpace(namespace))
	if len(runes) == 0 || len(runes) > automationGuardMaxNamespaceRunes {
		return fmt.Errorf("命名空间长度必须在 1 到 %d 个字符之间", automationGuardMaxNamespaceRunes)
	}
	for _, char := range runes {
		if unicode.IsControl(char) {
			return errors.New("命名空间不能包含控制字符")
		}
	}
	return nil
}

func automationGuardConfigInt(config map[string]any, key string, defaultValue, minValue, maxValue int) (int, error) {
	value, exists := config[key]
	if !exists || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return defaultValue, nil
	}
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("%s 超出整数范围", key)
		}
		parsed = int64(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, fmt.Errorf("%s 必须是整数", key)
		}
		parsed = int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s 必须是整数", key)
		}
		parsed = value
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s 必须是整数", key)
		}
		parsed = value
	default:
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	if parsed < int64(minValue) || parsed > int64(maxValue) {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间", key, minValue, maxValue)
	}
	return int(parsed), nil
}

func resolveAutomationGuardIdentity(run model.AutomationRun, node AutomationNode, runContext map[string]any) (automationGuardIdentity, error) {
	scope := strings.ToLower(automationConfigString(node.Config, "scope"))
	if scope == "" {
		scope = "workflow"
	}
	identity := automationGuardIdentity{ScopeType: scope}
	switch scope {
	case "workflow":
		if run.WorkflowID == 0 {
			return identity, errors.New("当前运行缺少流程 ID")
		}
		identity.ScopeID = run.WorkflowID
	case "source":
		sourceID, err := automationGuardUint(runContext["source_id"])
		if err != nil || sourceID == 0 {
			return identity, errors.New("当前运行缺少触发源 ID")
		}
		identity.ScopeID = sourceID
	case "global":
		identity.ScopeID = 0
	default:
		return identity, errors.New("作用域必须是 source/workflow/global")
	}

	identity.Namespace = strings.TrimSpace(automationConfigString(node.Config, "namespace"))
	if identity.Namespace == "" {
		if scope == "global" {
			return identity, errors.New("全局作用域必须配置命名空间")
		}
		identity.Namespace = strings.TrimSpace(node.ID)
	}
	if err := validateAutomationGuardNamespace(identity.Namespace); err != nil {
		return identity, err
	}

	key, err := resolveAutomationGuardKey(runContext, node.Config)
	if err != nil {
		return identity, err
	}
	digest := sha256.Sum256([]byte(key))
	identity.KeyHash = hex.EncodeToString(digest[:])
	return identity, nil
}

func automationGuardUint(value any) (uint, error) {
	var parsed uint64
	switch typed := value.(type) {
	case uint:
		return typed, nil
	case int:
		if typed < 0 {
			return 0, errors.New("值不能为负数")
		}
		parsed = uint64(typed)
	case int64:
		if typed < 0 {
			return 0, errors.New("值不能为负数")
		}
		parsed = uint64(typed)
	case float64:
		if typed < 0 || math.Trunc(typed) != typed || typed > float64(^uint(0)) {
			return 0, errors.New("值不是有效 ID")
		}
		parsed = uint64(typed)
	case json.Number:
		value, err := strconv.ParseUint(typed.String(), 10, 64)
		if err != nil {
			return 0, err
		}
		parsed = value
	case string:
		value, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, err
		}
		parsed = value
	default:
		return 0, errors.New("值不是有效 ID")
	}
	if uint64(uint(parsed)) != parsed {
		return 0, errors.New("ID 超出范围")
	}
	return uint(parsed), nil
}

func resolveAutomationGuardKey(runContext map[string]any, config map[string]any) (string, error) {
	raw, exists := config["key"]
	if !exists {
		return "", errors.New("必须配置键")
	}
	value, _, _, err := resolveAutomationVariableValue(runContext, raw)
	if err != nil {
		return "", err
	}
	var key string
	switch typed := value.(type) {
	case string:
		key = typed
	case json.Number:
		key = typed.String()
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		encoded, marshalErr := json.Marshal(typed)
		if marshalErr != nil {
			return "", fmt.Errorf("键不是有效标量: %w", marshalErr)
		}
		key = string(encoded)
	default:
		return "", fmt.Errorf("键必须是文本、数字或布尔值，不能是 %T", value)
	}
	normalize := strings.ToLower(automationConfigString(config, "normalize"))
	if normalize == "" {
		normalize = "trim"
	}
	switch normalize {
	case "none":
	case "trim":
		key = strings.TrimSpace(key)
	case "trim_lower":
		key = strings.ToLower(strings.TrimSpace(key))
	default:
		return "", errors.New("键标准化方式必须是 none/trim/trim_lower")
	}
	if key == "" {
		return "", errors.New("键不能为空")
	}
	if len(key) > automationGuardMaxKeyBytes {
		return "", fmt.Errorf("键不能超过 %d 字节", automationGuardMaxKeyBytes)
	}
	return key, nil
}

func automationGuardFailure(output map[string]any, err error) (map[string]any, error) {
	if output == nil {
		output = map[string]any{}
	}
	output["selected_port"] = "failure"
	output["reason"] = err.Error()
	return output, err
}

func (s *AutomationService) executeAutomationDeduplicateNode(run model.AutomationRun, nodeRun model.AutomationNodeRun, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	return s.executeAutomationDeduplicateNodeAt(run, nodeRun, node, runContext, time.Now())
}

func (s *AutomationService) executeAutomationDeduplicateNodeAt(run model.AutomationRun, nodeRun model.AutomationNodeRun, node AutomationNode, runContext map[string]any, now time.Time) (map[string]any, error) {
	if s.db == nil {
		return automationGuardFailure(nil, errors.New("自动化数据库未初始化"))
	}
	if err := validateAutomationDeduplicateConfig(node.Config); err != nil {
		return automationGuardFailure(nil, err)
	}
	identity, err := resolveAutomationGuardIdentity(run, node, runContext)
	if err != nil {
		return automationGuardFailure(nil, err)
	}
	ttlSeconds, _ := automationGuardConfigInt(node.Config, "ttl_seconds", automationGuardDefaultTTLSeconds, automationGuardMinTTLSeconds, automationGuardMaxTTLSeconds)
	refresh := automationConfigBool(node.Config, "refresh_on_duplicate")
	record, duplicate, err := s.reserveAutomationDedup(identity, run.ID, nodeRun.NodeID, int64(ttlSeconds), refresh, now.UTC())
	if err != nil {
		return automationGuardFailure(map[string]any{"key_hash": "sha256:" + identity.KeyHash}, err)
	}
	output := automationDedupOutput(record, duplicate)
	if duplicate {
		output["selected_port"] = "duplicate"
	} else {
		output["selected_port"] = "new"
	}
	return output, nil
}

func (s *AutomationService) reserveAutomationDedup(identity automationGuardIdentity, runID uint, nodeID string, ttlSeconds int64, refresh bool, now time.Time) (model.AutomationDedupRecord, bool, error) {
	var selected model.AutomationDedupRecord
	duplicate := false
	err := retryAutomationGuardDatabase(func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			var err error
			selected, duplicate, err = reserveAutomationDedupWithDB(tx, identity, runID, nodeID, ttlSeconds, refresh, now)
			return err
		})
	})
	return selected, duplicate, err
}

func reserveAutomationDedupWithDB(tx *gorm.DB, identity automationGuardIdentity, runID uint, nodeID string, ttlSeconds int64, refresh bool, now time.Time) (model.AutomationDedupRecord, bool, error) {
	selected := model.AutomationDedupRecord{}
	duplicate := false
	candidate := model.AutomationDedupRecord{
		ScopeType: identity.ScopeType, ScopeID: identity.ScopeID, Namespace: identity.Namespace, KeyHash: identity.KeyHash,
		State: model.AutomationDedupReserved, OwnerRunID: runID, OwnerNodeID: nodeID, TTLSeconds: ttlSeconds,
		FirstSeenAt: now, LastSeenAt: now,
	}
	created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
	if created.Error != nil {
		return selected, false, created.Error
	}
	if created.RowsAffected == 1 {
		return candidate, false, nil
	}

	if err := tx.Where("scope_type = ? AND scope_id = ? AND namespace = ? AND key_hash = ?", identity.ScopeType, identity.ScopeID, identity.Namespace, identity.KeyHash).First(&selected).Error; err != nil {
		return selected, false, err
	}
	if selected.OwnerRunID == runID && selected.OwnerNodeID == nodeID {
		return selected, false, nil
	}
	if selected.State == automationDedupRetryCommitted {
		var owner model.AutomationRun
		ownerErr := tx.Select("id", "status", "completed_at").First(&owner, selected.OwnerRunID).Error
		switch {
		case ownerErr == nil && (owner.Status == model.AutomationRunPending || owner.Status == model.AutomationRunRunning):
			return selected, true, nil
		case ownerErr == nil && (owner.Status == model.AutomationRunSucceeded || owner.Status == model.AutomationRunPartial):
			ttl := selected.TTLSeconds
			if ttl < automationGuardMinTTLSeconds || ttl > automationGuardMaxTTLSeconds {
				ttl = automationGuardDefaultTTLSeconds
			}
			expiresAt := now.Add(time.Duration(ttl) * time.Second)
			if err := tx.Model(&model.AutomationDedupRecord{}).
				Where("id = ? AND state = ?", selected.ID, automationDedupRetryCommitted).
				Updates(map[string]any{"state": model.AutomationDedupCommitted, "last_seen_at": now, "expires_at": expiresAt}).Error; err != nil {
				return selected, false, err
			}
		case errors.Is(ownerErr, gorm.ErrRecordNotFound) || owner.Status == model.AutomationRunFailed || owner.Status == model.AutomationRunCancelled:
			updates := map[string]any{"state": model.AutomationDedupCommitted}
			if selected.ExpiresAt == nil {
				updates["expires_at"] = now
			}
			if err := tx.Model(&model.AutomationDedupRecord{}).
				Where("id = ? AND state = ?", selected.ID, automationDedupRetryCommitted).
				Updates(updates).Error; err != nil {
				return selected, false, err
			}
		case ownerErr != nil:
			return selected, false, ownerErr
		default:
			return selected, true, nil
		}
		if err := tx.First(&selected, selected.ID).Error; err != nil {
			return selected, false, err
		}
	}

	if selected.State == model.AutomationDedupCommitted && selected.ExpiresAt != nil && !selected.ExpiresAt.After(now) {
		reclaimed, err := replaceAutomationDedupReservation(tx, selected, runID, nodeID, ttlSeconds, now, "state = ? AND expires_at IS NOT NULL AND expires_at <= ?", model.AutomationDedupCommitted, now)
		if err != nil {
			return selected, false, err
		}
		if reclaimed {
			err := tx.First(&selected, selected.ID).Error
			return selected, false, err
		}
		if err := tx.First(&selected, selected.ID).Error; err != nil {
			return selected, false, err
		}
		return selected, selected.OwnerRunID != runID || selected.OwnerNodeID != nodeID, nil
	}

	if selected.State == model.AutomationDedupReserved {
		var owner model.AutomationRun
		ownerErr := tx.Select("id", "status", "completed_at").First(&owner, selected.OwnerRunID).Error
		if errors.Is(ownerErr, gorm.ErrRecordNotFound) || owner.Status == model.AutomationRunFailed || owner.Status == model.AutomationRunCancelled {
			reclaimed, err := replaceAutomationDedupReservation(tx, selected, runID, nodeID, ttlSeconds, now, "state = ? AND owner_run_id = ? AND owner_node_id = ?", model.AutomationDedupReserved, selected.OwnerRunID, selected.OwnerNodeID)
			if err != nil {
				return selected, false, err
			}
			if reclaimed {
				err := tx.First(&selected, selected.ID).Error
				return selected, false, err
			}
		} else if ownerErr != nil {
			return selected, false, ownerErr
		} else if owner.Status == model.AutomationRunSucceeded || owner.Status == model.AutomationRunPartial {
			expiresAt := now.Add(time.Duration(selected.TTLSeconds) * time.Second)
			if selected.TTLSeconds < automationGuardMinTTLSeconds {
				expiresAt = now.Add(automationGuardDefaultTTLSeconds * time.Second)
			}
			if err := tx.Model(&model.AutomationDedupRecord{}).Where("id = ? AND state = ?", selected.ID, model.AutomationDedupReserved).Updates(map[string]any{
				"state": model.AutomationDedupCommitted, "last_seen_at": now, "expires_at": expiresAt,
			}).Error; err != nil {
				return selected, false, err
			}
			if err := tx.First(&selected, selected.ID).Error; err != nil {
				return selected, false, err
			}
		}
	}

	duplicate = true
	if refresh && selected.State == model.AutomationDedupCommitted {
		expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)
		if err := tx.Model(&selected).Updates(map[string]any{"last_seen_at": now, "ttl_seconds": ttlSeconds, "expires_at": expiresAt}).Error; err != nil {
			return selected, false, err
		}
		selected.LastSeenAt = now
		selected.TTLSeconds = ttlSeconds
		selected.ExpiresAt = &expiresAt
	}
	return selected, duplicate, nil
}

func replaceAutomationDedupReservation(tx *gorm.DB, current model.AutomationDedupRecord, runID uint, nodeID string, ttlSeconds int64, now time.Time, predicate string, values ...any) (bool, error) {
	queryValues := append([]any{current.ID}, values...)
	result := tx.Model(&model.AutomationDedupRecord{}).
		Where("id = ? AND "+predicate, queryValues...).
		Updates(map[string]any{
			"state": model.AutomationDedupReserved, "owner_run_id": runID, "owner_node_id": nodeID,
			"ttl_seconds": ttlSeconds, "first_seen_at": now, "last_seen_at": now, "expires_at": nil,
		})
	return result.RowsAffected == 1, result.Error
}

func automationDedupOutput(record model.AutomationDedupRecord, duplicate bool) map[string]any {
	output := map[string]any{
		"duplicate": duplicate, "key_hash": "sha256:" + record.KeyHash,
		"scope": record.ScopeType, "scope_id": record.ScopeID, "namespace": record.Namespace,
		"first_seen_at": record.FirstSeenAt.UTC().Format(time.RFC3339Nano),
		"last_seen_at":  record.LastSeenAt.UTC().Format(time.RFC3339Nano),
		"owner_run_id":  record.OwnerRunID, "state": record.State,
	}
	if record.ExpiresAt != nil {
		output["expires_at"] = record.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return output
}

type automationDedupRetryConflictError struct {
	NodeName   string
	OwnerRunID uint
}

func (err *automationDedupRetryConflictError) Error() string {
	name := strings.TrimSpace(err.NodeName)
	if name == "" {
		name = "去重节点"
	}
	if err.OwnerRunID == 0 {
		return fmt.Sprintf("%s 的去重 reservation 已被其他运行接管，无法安全重试", name)
	}
	return fmt.Sprintf("%s 的去重 reservation 已被运行 #%d 接管，无法安全重试", name, err.OwnerRunID)
}

// restoreAutomationDedupReservationsForRetryWithDB re-acquires reservations
// produced by successful deduplicate nodes that RetryRun intentionally keeps
// completed. It must run in the same transaction that conditionally changes
// the run from a terminal state back to pending. Any conflict aborts that whole
// transaction, so successful intermediate actions remain untouched and the
// retry cannot continue without its original dedup protection.
func restoreAutomationDedupReservationsForRetryWithDB(tx *gorm.DB, run model.AutomationRun, definition AutomationDefinition, nodeRuns []model.AutomationNodeRun, resetNodes map[string]struct{}, now time.Time) error {
	if tx == nil || run.ID == 0 {
		return nil
	}
	nodesByID := make(map[string]AutomationNode, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodesByID[node.ID] = node
	}
	candidates := make([]model.AutomationNodeRun, 0)
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status != model.AutomationNodeSucceeded {
			continue
		}
		if _, resets := resetNodes[nodeRun.NodeID]; resets {
			continue
		}
		node, exists := nodesByID[nodeRun.NodeID]
		if exists && node.Type == AutomationNodeDeduplicate {
			candidates = append(candidates, nodeRun)
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ID == candidates[right].ID {
			return candidates[left].NodeID < candidates[right].NodeID
		}
		return candidates[left].ID < candidates[right].ID
	})

	var baseContext map[string]any
	for _, nodeRun := range candidates {
		node := nodesByID[nodeRun.NodeID]
		output, err := decodeAutomationGuardOutput(nodeRun.OutputJSON)
		if err != nil {
			return fmt.Errorf("读取去重节点 %s 的历史输出失败: %w", automationGuardNodeName(node), err)
		}
		selectedPort, _ := output["selected_port"].(string)
		if selectedPort == "duplicate" {
			continue
		}
		if selectedPort != "new" {
			return fmt.Errorf("去重节点 %s 缺少可验证的 new 历史输出，无法安全重试", automationGuardNodeName(node))
		}
		if baseContext == nil {
			baseContext, err = decodeAutomationGuardRunContext(run.ContextJSON)
			if err != nil {
				return err
			}
		}
		identity, ttlSeconds, err := automationDedupRetryIdentity(run, node, output, baseContext)
		if err != nil {
			return fmt.Errorf("恢复去重节点 %s 失败: %w", automationGuardNodeName(node), err)
		}
		record, duplicate, err := restoreAutomationDedupReservationWithDB(tx, identity, run.ID, nodeRun.NodeID, ttlSeconds, now.UTC())
		if err != nil {
			return err
		}
		if duplicate {
			return &automationDedupRetryConflictError{NodeName: automationGuardNodeName(node), OwnerRunID: record.OwnerRunID}
		}
	}
	return nil
}

// restoreAutomationDedupReservationWithDB distinguishes a reservation created
// for a previously failed/cancelled run from a committed record produced by a
// partial run. The latter is temporarily marked retry_committed: it cannot
// expire during the active retry, and a later failed/cancelled finalization can
// restore the exact prior committed expiry instead of deleting that history.
func restoreAutomationDedupReservationWithDB(tx *gorm.DB, identity automationGuardIdentity, runID uint, nodeID string, ttlSeconds int64, now time.Time) (model.AutomationDedupRecord, bool, error) {
	record, duplicate, err := reserveAutomationDedupWithDB(tx, identity, runID, nodeID, ttlSeconds, false, now)
	if err != nil || duplicate {
		return record, duplicate, err
	}
	if record.OwnerRunID != runID || record.OwnerNodeID != nodeID {
		return record, true, nil
	}
	switch record.State {
	case model.AutomationDedupReserved, automationDedupRetryCommitted:
		return record, false, nil
	case model.AutomationDedupCommitted:
		updated := tx.Model(&model.AutomationDedupRecord{}).
			Where("id = ? AND state = ? AND owner_run_id = ? AND owner_node_id = ?", record.ID, model.AutomationDedupCommitted, runID, nodeID).
			Update("state", automationDedupRetryCommitted)
		if updated.Error != nil {
			return record, false, updated.Error
		}
		if err := tx.First(&record, record.ID).Error; err != nil {
			return record, false, err
		}
		if updated.RowsAffected != 1 {
			if record.OwnerRunID != runID || record.OwnerNodeID != nodeID {
				return record, true, nil
			}
			if record.State != automationDedupRetryCommitted && record.State != model.AutomationDedupReserved {
				return record, false, errors.New("去重 reservation 状态在重试时发生变化")
			}
		}
		return record, false, nil
	default:
		return record, false, fmt.Errorf("无法从状态 %q 恢复去重 reservation", record.State)
	}
}

func decodeAutomationGuardOutput(outputJSON string) (map[string]any, error) {
	if strings.TrimSpace(outputJSON) == "" {
		return nil, errors.New("历史输出为空")
	}
	output := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(outputJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		return nil, err
	}
	return output, nil
}

func decodeAutomationGuardRunContext(contextJSON string) (map[string]any, error) {
	contextValue := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(contextJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&contextValue); err != nil {
		return nil, fmt.Errorf("读取运行上下文失败: %w", err)
	}
	return contextValue, nil
}

func automationDedupRetryIdentity(run model.AutomationRun, node AutomationNode, output, baseContext map[string]any) (automationGuardIdentity, int64, error) {
	if err := validateAutomationDeduplicateConfig(node.Config); err != nil {
		return automationGuardIdentity{}, 0, err
	}
	scope := strings.ToLower(automationConfigString(node.Config, "scope"))
	if scope == "" {
		scope = "workflow"
	}
	expectedScopeID := uint(0)
	switch scope {
	case "workflow":
		expectedScopeID = run.WorkflowID
	case "source":
		value, err := automationGuardUint(baseContext["source_id"])
		if err != nil || value == 0 {
			return automationGuardIdentity{}, 0, errors.New("当前运行缺少触发源 ID")
		}
		expectedScopeID = value
	case "global":
	default:
		return automationGuardIdentity{}, 0, errors.New("作用域必须是 source/workflow/global")
	}
	expectedNamespace := strings.TrimSpace(automationConfigString(node.Config, "namespace"))
	if expectedNamespace == "" {
		expectedNamespace = strings.TrimSpace(node.ID)
	}
	if err := validateAutomationGuardNamespace(expectedNamespace); err != nil {
		return automationGuardIdentity{}, 0, err
	}

	outputScope, _ := output["scope"].(string)
	outputNamespace, _ := output["namespace"].(string)
	outputScopeID, err := automationGuardUint(output["scope_id"])
	if err != nil || outputScope != scope || outputScopeID != expectedScopeID || outputNamespace != expectedNamespace {
		return automationGuardIdentity{}, 0, errors.New("历史去重作用域与运行定义不一致")
	}
	ownerRunID, err := automationGuardUint(output["owner_run_id"])
	if err != nil || ownerRunID != run.ID {
		return automationGuardIdentity{}, 0, errors.New("历史去重 reservation 不属于当前运行")
	}
	keyHash, _ := output["key_hash"].(string)
	keyHash = strings.TrimPrefix(strings.TrimSpace(keyHash), "sha256:")
	decodedHash, err := hex.DecodeString(keyHash)
	if err != nil || len(decodedHash) != sha256.Size {
		return automationGuardIdentity{}, 0, errors.New("历史去重键摘要无效")
	}
	ttlSeconds, err := automationGuardConfigInt(node.Config, "ttl_seconds", automationGuardDefaultTTLSeconds, automationGuardMinTTLSeconds, automationGuardMaxTTLSeconds)
	if err != nil {
		return automationGuardIdentity{}, 0, err
	}
	return automationGuardIdentity{
		ScopeType: scope, ScopeID: expectedScopeID, Namespace: expectedNamespace, KeyHash: strings.ToLower(keyHash),
	}, int64(ttlSeconds), nil
}

func automationGuardNodeName(node AutomationNode) string {
	if name := strings.TrimSpace(node.Name); name != "" {
		return name
	}
	return node.ID
}

// finalizeAutomationGuardRun must be called after an AutomationRun reaches a
// terminal status. Successful/partial runs commit active reservations. Failed
// and cancelled runs release reservations created for that attempt, but turn a
// retry_committed reservation back into its prior committed record so retrying
// a partial run cannot erase protection earned by the earlier partial result.
func (s *AutomationService) finalizeAutomationGuardRun(runID uint, status string) error {
	return s.finalizeAutomationGuardRunAt(runID, status, time.Now())
}

func (s *AutomationService) finalizeAutomationGuardRunAt(runID uint, status string, now time.Time) error {
	if s.db == nil || runID == 0 {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return finalizeAutomationGuardRunWithDB(tx, runID, status, now)
	})
}

// finalizeAutomationGuardRunWithDB lets terminal run updates and dedup
// reservation finalization share one transaction. Callers must only invoke it
// after they have actually transitioned the run to the supplied terminal
// status; a stale terminal decision must never commit another outcome's claim.
func finalizeAutomationGuardRunWithDB(tx *gorm.DB, runID uint, status string, now time.Time) error {
	if tx == nil || runID == 0 {
		return nil
	}
	now = now.UTC()
	switch status {
	case model.AutomationRunSucceeded, model.AutomationRunPartial:
		var records []model.AutomationDedupRecord
		activeStates := []string{model.AutomationDedupReserved, automationDedupRetryCommitted}
		if err := tx.Where("owner_run_id = ? AND state IN ?", runID, activeStates).Find(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			ttl := record.TTLSeconds
			if ttl < automationGuardMinTTLSeconds || ttl > automationGuardMaxTTLSeconds {
				ttl = automationGuardDefaultTTLSeconds
			}
			expiresAt := now.Add(time.Duration(ttl) * time.Second)
			if err := tx.Model(&model.AutomationDedupRecord{}).Where("id = ? AND state IN ?", record.ID, activeStates).Updates(map[string]any{
				"state": model.AutomationDedupCommitted, "last_seen_at": now, "expires_at": expiresAt,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	case model.AutomationRunFailed, model.AutomationRunCancelled:
		if err := tx.Model(&model.AutomationDedupRecord{}).
			Where("owner_run_id = ? AND state = ?", runID, automationDedupRetryCommitted).
			Update("state", model.AutomationDedupCommitted).Error; err != nil {
			return err
		}
		return tx.Where("owner_run_id = ? AND state = ?", runID, model.AutomationDedupReserved).Delete(&model.AutomationDedupRecord{}).Error
	default:
		return nil
	}
}

// deleteAutomationGuardStateForRuns removes per-run idempotency claims before
// run history is deleted. Global rate-limit buckets and their consumed counts
// deliberately remain, while uncommitted global dedup reservations are freed.
func deleteAutomationGuardStateForRuns(tx *gorm.DB, runIDs *gorm.DB) error {
	if tx == nil || runIDs == nil {
		return nil
	}
	if err := tx.Where("run_id IN (?)", runIDs).Delete(&model.AutomationRateLimitClaim{}).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.AutomationDedupRecord{}).
		Where("owner_run_id IN (?) AND state = ?", runIDs, automationDedupRetryCommitted).
		Update("state", model.AutomationDedupCommitted).Error; err != nil {
		return err
	}
	return tx.Where("owner_run_id IN (?) AND state = ?", runIDs, model.AutomationDedupReserved).Delete(&model.AutomationDedupRecord{}).Error
}

func deleteAutomationGuardsForWorkflow(tx *gorm.DB, workflowID uint) error {
	return deleteAutomationGuardsForScope(tx, "workflow", workflowID)
}

func deleteAutomationGuardsForSource(tx *gorm.DB, sourceID uint) error {
	return deleteAutomationGuardsForScope(tx, "source", sourceID)
}

func deleteAutomationGuardsForScope(tx *gorm.DB, scopeType string, scopeID uint) error {
	if tx == nil || scopeID == 0 {
		return nil
	}
	if scopeType != "workflow" && scopeType != "source" {
		return errors.New("只能清理 workflow/source 作用域的自动化 guard")
	}
	if err := tx.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Delete(&model.AutomationRateLimitClaim{}).Error; err != nil {
		return err
	}
	if err := tx.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Delete(&model.AutomationRateLimitBucket{}).Error; err != nil {
		return err
	}
	return tx.Where("scope_type = ? AND scope_id = ?", scopeType, scopeID).Delete(&model.AutomationDedupRecord{}).Error
}

func (s *AutomationService) cleanupExpiredAutomationDedupRecords(now time.Time, limit int) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var ids []uint
	if err := s.db.Model(&model.AutomationDedupRecord{}).
		Where("state = ? AND expires_at IS NOT NULL AND expires_at <= ?", model.AutomationDedupCommitted, now.UTC()).
		Order("expires_at ASC").Limit(limit).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return 0, err
	}
	result := s.db.Where("id IN ? AND state = ? AND expires_at <= ?", ids, model.AutomationDedupCommitted, now.UTC()).Delete(&model.AutomationDedupRecord{})
	return result.RowsAffected, result.Error
}

func (s *AutomationService) executeAutomationRateLimitNode(run model.AutomationRun, nodeRun model.AutomationNodeRun, node AutomationNode, runContext map[string]any) (map[string]any, error) {
	return s.executeAutomationRateLimitNodeAt(run, nodeRun, node, runContext, time.Now())
}

func (s *AutomationService) executeAutomationRateLimitNodeAt(run model.AutomationRun, nodeRun model.AutomationNodeRun, node AutomationNode, runContext map[string]any, now time.Time) (map[string]any, error) {
	if s.db == nil {
		return automationGuardFailure(nil, errors.New("自动化数据库未初始化"))
	}
	if err := validateAutomationRateLimitConfig(node.Config); err != nil {
		return automationGuardFailure(nil, err)
	}
	identity, err := resolveAutomationGuardIdentity(run, node, runContext)
	if err != nil {
		return automationGuardFailure(nil, err)
	}
	limit, _ := automationGuardConfigInt(node.Config, "limit", 1, 1, automationGuardMaxRateLimit)
	windowSeconds, _ := automationGuardConfigInt(node.Config, "window_seconds", 60, 1, automationGuardMaxWindowSeconds)
	maxWaitSeconds, _ := automationGuardConfigInt(node.Config, "max_wait_seconds", windowSeconds, 1, automationGuardMaxWindowSeconds)
	claim, bucket, allowed, err := s.claimAutomationRateLimit(identity, run.ID, nodeRun.NodeID, limit, windowSeconds, now.UTC())
	if err != nil {
		return automationGuardFailure(map[string]any{"key_hash": "sha256:" + identity.KeyHash}, err)
	}
	if allowed {
		output := automationRateClaimOutput(claim)
		output["selected_port"] = "allowed"
		return output, nil
	}

	output := automationRateBucketOutput(identity, bucket)
	behavior := strings.ToLower(automationConfigString(node.Config, "behavior"))
	if behavior == "" {
		behavior = "defer"
	}
	if behavior == "branch" {
		output["selected_port"] = "throttled"
		return output, nil
	}

	firstWait := automationRateFirstWait(nodeRun, now.UTC())
	deadline := firstWait.Add(time.Duration(maxWaitSeconds) * time.Second)
	if !now.UTC().Before(deadline) {
		return automationGuardFailure(output, fmt.Errorf("限流等待超过 %d 秒", maxWaitSeconds))
	}
	retryAt := bucket.WindowEnd.UTC()
	if deadline.Before(retryAt) {
		retryAt = deadline
	}
	delay := retryAt.Sub(now.UTC())
	if delay <= 0 {
		delay = time.Second
	}
	output["first_wait_at"] = firstWait.Format(time.RFC3339Nano)
	output["retry_at"] = retryAt.Format(time.RFC3339Nano)
	delete(output, "selected_port")
	return output, &automationNodeDeferred{delay: delay, message: fmt.Sprintf("限流中，将于 %s 后重试", retryAt.Format(time.RFC3339))}
}

func (s *AutomationService) claimAutomationRateLimit(identity automationGuardIdentity, runID uint, nodeID string, limit, windowSeconds int, now time.Time) (model.AutomationRateLimitClaim, model.AutomationRateLimitBucket, bool, error) {
	var claim model.AutomationRateLimitClaim
	var bucket model.AutomationRateLimitBucket
	policyHash := automationRatePolicyHash(limit, windowSeconds)
	localLock := automationRateLimitLock(identity, policyHash)
	localLock.Lock()
	defer localLock.Unlock()
	for attempt := 0; attempt < 7; attempt++ {
		claim = model.AutomationRateLimitClaim{}
		bucket = model.AutomationRateLimitBucket{}
		err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("run_id = ? AND node_id = ?", runID, nodeID).First(&claim).Error; err == nil {
				return validateAutomationRateClaimIdentity(claim, identity, policyHash, limit, windowSeconds)
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}

			windowStart, windowEnd := automationRateWindow(now, windowSeconds)
			candidate := model.AutomationRateLimitBucket{
				ScopeType: identity.ScopeType, ScopeID: identity.ScopeID, Namespace: identity.Namespace, KeyHash: identity.KeyHash,
				PolicyHash: policyHash, Algorithm: automationRateAlgorithm, Limit: limit, WindowSeconds: windowSeconds,
				WindowStart: windowStart, WindowEnd: windowEnd, Used: 0,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
				return err
			}
			if err := tx.Where("scope_type = ? AND scope_id = ? AND namespace = ? AND key_hash = ? AND policy_hash = ?", identity.ScopeType, identity.ScopeID, identity.Namespace, identity.KeyHash, policyHash).First(&bucket).Error; err != nil {
				return err
			}
			if bucket.Algorithm != automationRateAlgorithm || bucket.Limit != limit || bucket.WindowSeconds != windowSeconds {
				return errors.New("限流桶策略与当前节点配置不一致")
			}
			if !bucket.WindowEnd.After(now) {
				reset := tx.Model(&model.AutomationRateLimitBucket{}).
					Where("id = ? AND window_end <= ?", bucket.ID, now).
					Updates(map[string]any{"window_start": windowStart, "window_end": windowEnd, "used": 0})
				if reset.Error != nil {
					return reset.Error
				}
				if err := tx.First(&bucket, bucket.ID).Error; err != nil {
					return err
				}
			}
			if bucket.Used >= limit {
				return nil
			}
			incremented := tx.Model(&model.AutomationRateLimitBucket{}).
				Where("id = ? AND used = ? AND window_end = ? AND used < ?", bucket.ID, bucket.Used, bucket.WindowEnd, limit).
				Update("used", gorm.Expr("used + 1"))
			if incremented.Error != nil {
				return incremented.Error
			}
			if incremented.RowsAffected != 1 {
				return errAutomationRateContention
			}
			bucket.Used++
			claim = model.AutomationRateLimitClaim{
				RunID: runID, NodeID: nodeID, BucketID: bucket.ID,
				ScopeType: identity.ScopeType, ScopeID: identity.ScopeID, Namespace: identity.Namespace, KeyHash: identity.KeyHash,
				PolicyHash: policyHash, Algorithm: automationRateAlgorithm, Limit: limit, WindowSeconds: windowSeconds,
				Used: bucket.Used, WindowStart: bucket.WindowStart, WindowEnd: bucket.WindowEnd, ClaimedAt: now,
			}
			return tx.Create(&claim).Error
		})
		if err == nil {
			if claim.ID != 0 {
				return claim, bucket, true, nil
			}
			return claim, bucket, false, nil
		}
		var existing model.AutomationRateLimitClaim
		if lookupErr := s.db.Where("run_id = ? AND node_id = ?", runID, nodeID).First(&existing).Error; lookupErr == nil {
			if identityErr := validateAutomationRateClaimIdentity(existing, identity, policyHash, limit, windowSeconds); identityErr != nil {
				return claim, bucket, false, identityErr
			}
			return existing, bucket, true, nil
		}
		if errors.Is(err, errAutomationRateContention) || isAutomationGuardTransientDatabaseError(err) {
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			continue
		}
		return claim, bucket, false, err
	}
	return claim, bucket, false, errAutomationRateContention
}

func automationRatePolicyHash(limit, windowSeconds int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", automationRateAlgorithm, limit, windowSeconds)))
	return hex.EncodeToString(digest[:])
}

func automationRateLimitLock(identity automationGuardIdentity, policyHash string) *sync.Mutex {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s:%s", identity.ScopeType, identity.ScopeID, identity.Namespace, identity.KeyHash, policyHash)))
	return &automationRateLimitStripes[int(digest[0])]
}

func validateAutomationRateClaimIdentity(claim model.AutomationRateLimitClaim, identity automationGuardIdentity, policyHash string, limit, windowSeconds int) error {
	if claim.ScopeType != identity.ScopeType || claim.ScopeID != identity.ScopeID || claim.Namespace != identity.Namespace || claim.KeyHash != identity.KeyHash || claim.PolicyHash != policyHash || claim.Algorithm != automationRateAlgorithm || claim.Limit != limit || claim.WindowSeconds != windowSeconds {
		return errors.New("当前节点运行已按不同的限流键或策略消费额度")
	}
	return nil
}

func retryAutomationGuardDatabase(operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 7; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
			if !isAutomationGuardTransientDatabaseError(err) {
				return err
			}
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return lastErr
}

func isAutomationGuardTransientDatabaseError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "database is busy")
}

func automationRateWindow(now time.Time, windowSeconds int) (time.Time, time.Time) {
	seconds := int64(windowSeconds)
	startUnix := now.UTC().Unix() - now.UTC().Unix()%seconds
	start := time.Unix(startUnix, 0).UTC()
	return start, start.Add(time.Duration(windowSeconds) * time.Second)
}

func automationRateFirstWait(nodeRun model.AutomationNodeRun, fallback time.Time) time.Time {
	if strings.TrimSpace(nodeRun.OutputJSON) == "" {
		return fallback
	}
	var output map[string]any
	if json.Unmarshal([]byte(nodeRun.OutputJSON), &output) != nil {
		return fallback
	}
	value, _ := output["first_wait_at"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed.UTC()
}

func automationRateClaimOutput(claim model.AutomationRateLimitClaim) map[string]any {
	remaining := claim.Limit - claim.Used
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"allowed": true, "algorithm": claim.Algorithm, "limit": claim.Limit, "window_seconds": claim.WindowSeconds,
		"used": claim.Used, "remaining": remaining,
		"window_start": claim.WindowStart.UTC().Format(time.RFC3339Nano),
		"window_end":   claim.WindowEnd.UTC().Format(time.RFC3339Nano),
		"retry_at":     nil, "key_hash": "sha256:" + claim.KeyHash,
		"policy_hash": "sha256:" + claim.PolicyHash,
		"scope":       claim.ScopeType, "scope_id": claim.ScopeID, "namespace": claim.Namespace,
		"claim_id": claim.ID,
	}
}

func automationRateBucketOutput(identity automationGuardIdentity, bucket model.AutomationRateLimitBucket) map[string]any {
	remaining := bucket.Limit - bucket.Used
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"allowed": false, "algorithm": bucket.Algorithm, "limit": bucket.Limit, "window_seconds": bucket.WindowSeconds,
		"used": bucket.Used, "remaining": remaining,
		"window_start": bucket.WindowStart.UTC().Format(time.RFC3339Nano),
		"window_end":   bucket.WindowEnd.UTC().Format(time.RFC3339Nano),
		"retry_at":     bucket.WindowEnd.UTC().Format(time.RFC3339Nano),
		"key_hash":     "sha256:" + identity.KeyHash,
		"policy_hash":  "sha256:" + bucket.PolicyHash,
		"scope":        identity.ScopeType, "scope_id": identity.ScopeID, "namespace": identity.Namespace,
	}
}
