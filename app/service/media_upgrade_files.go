package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const UpgradeRecoveryRoot = "/FilmFusion-Recovery"

type UpgradeFileSnapshot struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
}

type UpgradeFileChange struct {
	Before           UpgradeFileSnapshot `json:"before"`
	Write            bool                `json:"write"`
	Data             []byte              `json:"data,omitempty"`
	DownloadPickCode string              `json:"download_pickcode,omitempty"`
	DownloadSize     int64               `json:"download_size,omitempty"`
}

var upgradeFileLocks sync.Map

// Shared with background downloads so the final ownership check and rename
// cannot interleave with an upgrade's compare-and-write in this process.
func LockUpgradeLocalPath(target string) func() {
	lock, _ := upgradeFileLocks.LoadOrStore(filepath.Clean(target), &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func ListUpgradeSiblings(root, target string) ([]string, error) {
	rel, err := upgradeRelative(root, target)
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	folder, err := dir.Open(filepath.Dir(rel))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	entries, err := folder.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func upgradeRelative(root, target string) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return "", errors.New("洗版 STRM 路径必须为绝对路径")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("洗版路径越过 STRM 根目录")
	}
	return rel, nil
}

func ReadUpgradeFile(root, target string) (UpgradeFileSnapshot, error) {
	result := UpgradeFileSnapshot{Root: filepath.Clean(root), Path: filepath.Clean(target)}
	rel, err := upgradeRelative(result.Root, result.Path)
	if err != nil {
		return result, err
	}
	dir, err := os.OpenRoot(result.Root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer dir.Close()
	stat, err := dir.Lstat(rel)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 2*1024*1024 {
		return result, fmt.Errorf("关联文件不是普通文件或超过 2 MiB: %s", target)
	}
	file, err := dir.Open(rel)
	if err != nil {
		return result, err
	}
	defer file.Close()
	result.Data, err = io.ReadAll(io.LimitReader(file, 2*1024*1024+1))
	if len(result.Data) > 2*1024*1024 {
		return result, errors.New("关联文件在读取时变化")
	}
	result.Exists = err == nil
	return result, err
}

func SameUpgradeFile(left, right UpgradeFileSnapshot) bool {
	return left.Exists == right.Exists && bytes.Equal(left.Data, right.Data)
}

// Idempotent compare-and-write. Neither a symlink outside Root nor an unrelated
// edit is followed/overwritten. A journal retains Before for compensation.
func ApplyUpgradeFile(change UpgradeFileChange, restore bool) error {
	unlock := LockUpgradeLocalPath(change.Before.Path)
	defer unlock()
	before := change.Before
	after := UpgradeFileSnapshot{Root: before.Root, Path: before.Path, Exists: change.Write, Data: change.Data}
	expected, desired := before, after
	if restore {
		expected, desired = after, before
	}
	current, err := ReadUpgradeFile(before.Root, before.Path)
	if err != nil {
		return err
	}
	if SameUpgradeFile(current, desired) {
		return nil
	}
	if !SameUpgradeFile(current, expected) {
		return fmt.Errorf("关联文件已被其他操作修改，已停止: %s", before.Path)
	}
	if err := os.MkdirAll(before.Root, 0755); err != nil {
		return err
	}
	dir, err := os.OpenRoot(before.Root)
	if err != nil {
		return err
	}
	defer dir.Close()
	rel, err := upgradeRelative(before.Root, before.Path)
	if err != nil {
		return err
	}
	if !desired.Exists {
		err = dir.Remove(rel)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := dir.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return err
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(rel), ".filmfusion-"+hex.EncodeToString(token))
	file, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer dir.Remove(temp)
	if _, err = file.Write(desired.Data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return dir.Rename(temp, rel)
}

func IsUpgradeRecoveryPath(value string) bool {
	value = "/" + strings.TrimLeft(filepath.ToSlash(value), "/")
	return value == UpgradeRecoveryRoot || strings.HasPrefix(value, UpgradeRecoveryRoot+"/")
}
