package cover

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// fontCache 字体文件解析后的全局缓存（按路径），避免重复读取
var (
	fontCache   = make(map[string]*sfnt.Font)
	fontCacheMu sync.RWMutex
)

// LoadSfntFont 解析 OTF/TTF 字体文件，结果会被缓存
func LoadSfntFont(path string) (*sfnt.Font, error) {
	if path == "" {
		return nil, fmt.Errorf("字体路径为空")
	}
	fontCacheMu.RLock()
	if f, ok := fontCache[path]; ok {
		fontCacheMu.RUnlock()
		return f, nil
	}
	fontCacheMu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取字体文件失败 %s: %w", path, err)
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("解析字体失败 %s: %w", path, err)
	}

	fontCacheMu.Lock()
	fontCache[path] = f
	fontCacheMu.Unlock()
	return f, nil
}

// NewFace 根据 sfnt.Font 创建指定字号的 face
func NewFace(f *sfnt.Font, sizePx float64) (font.Face, error) {
	if f == nil {
		return nil, fmt.Errorf("字体为空")
	}
	if sizePx <= 0 {
		sizePx = 32
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    sizePx,
		DPI:     72, // 用 72 时 Size 就是像素
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("创建字体 face 失败 (size=%.1f): %w", sizePx, err)
	}
	return face, nil
}
