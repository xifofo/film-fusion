package directorymonitor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func safeDirectory(root *os.Root, relative string) (bool, error) {
	part := ""
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		part = path.Join(part, component)
		info, err := root.Lstat(filepath.FromSlash(part))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, nil
		}
	}
	return true, nil
}

func (m *Monitor) observe(tx *gorm.DB, config Config, state model.LocalDirectoryState, relative string, info os.FileInfo) error {
	var item model.LocalDirectoryItem
	hash := digest(relative)
	err := tx.First(&item, "source_id = ? AND path_hash = ?", config.SourceID, hash).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	newItem := errors.Is(err, gorm.ErrRecordNotFound)
	if err := m.fillObservedItem(&item, config, state, relative, info, newItem); err != nil {
		return err
	}
	return tx.Save(&item).Error
}

func (m *Monitor) fillObservedItem(item *model.LocalDirectoryItem, config Config, state model.LocalDirectoryState, relative string, info os.FileInfo, newItem bool) error {
	epochChanged := item.SnapshotEpoch != state.Epoch && (item.SnapshotEpoch != "" || !state.Initialized)
	created := newItem || !item.Present || item.IsDirectory != info.IsDir() || epochChanged
	eligible := selected(info.Name(), info.IsDir(), config.Options)
	baseline := !state.Initialized && config.Options.InitialScan == "baseline"
	ownSignature := digest(fmt.Sprintf("%d|%d|%s", info.Size(), info.ModTime().UnixNano(), info.Mode()))
	now := m.now()
	if newItem {
		*item = model.LocalDirectoryItem{SourceID: config.SourceID, PathHash: digest(relative), Relative: relative}
	}
	if item.Incarnation == "" {
		identity := make([]byte, 16)
		if _, err := rand.Read(identity); err != nil {
			return err
		}
		item.Incarnation = hex.EncodeToString(identity)
	}
	if created {
		item.Revision++
		item.Signature = ownSignature
		item.StableSince, item.Observations = now, 1
		item.PendingType = ""
		if eligible && !baseline && hasEvent(config.Options, "created") {
			item.PendingType = "created"
		}
	} else if !info.IsDir() || !eligible {
		if item.Generation != state.Generation || item.Signature != ownSignature {
			m.updateSignature(item, ownSignature, now, eligible && !baseline && hasEvent(config.Options, "modified"))
		}
	}
	if baseline {
		item.PendingType = ""
	}
	item.Name, item.Relative, item.IsDirectory, item.Size, item.ModTime = info.Name(), relative, info.IsDir(), info.Size(), info.ModTime()
	item.OwnSignature, item.Generation, item.Present = ownSignature, state.Generation, true
	item.MissingSince = nil
	item.SnapshotEpoch = state.Epoch
	item.ParentHash, item.Depth = digest(path.Dir(relative)), strings.Count(relative, "/")+1
	return nil
}

func (m *Monitor) updateSignature(item *model.LocalDirectoryItem, signature string, now time.Time, allowModified bool) {
	if item.Signature != signature {
		item.Revision++
		item.Signature, item.StableSince, item.Observations = signature, now, 1
		if item.PendingType == "" && allowModified {
			item.PendingType = "modified"
		}
	} else if item.Observations < 2 {
		item.Observations++
	}
}
