// Package cover 提供 Emby 媒体库封面图的合成能力。
//
// 设计要点：
//   - 与 Emby/HTTP 解耦：本包只做"输入海报字节 + 标题 → 输出封面字节"
//   - 模板化：每套样式实现 Template 接口注册到全局 Registry
//   - 色彩自适应：从海报抽主色作为背景渐变（参考 MoviePilot mediacovergenerator 的算法）
package cover

import (
	"context"
	"image/color"
)

// RenderInput 渲染单张封面所需的全部输入
type RenderInput struct {
	Width       int      // 输出图宽
	Height      int      // 输出图高
	JPEGQuality int      // JPEG 输出质量 1-100；<=0 取默认 88
	CNTitle     string   // 中文主标题
	ENSubtitle  string   // 英文副标题（可空）
	Posters     [][]byte // 海报字节（N 张，按推荐顺序排列）
	FontCNPath  string   // 中文字体路径（OTF/TTF）
	FontENPath  string   // 英文字体路径（OTF/TTF）
}

// RenderOutput 渲染输出（JPEG 字节 + 元信息）
type RenderOutput struct {
	JPEG       []byte
	Width      int
	Height     int
	BackgroundColors []color.RGBA // 用到的背景色（debug 用）
}

// Template 模板接口
type Template interface {
	ID() string
	Name() string
	// Render 执行合成。失败时返回 error；输出 JPEG 字节通过 RenderOutput 返回。
	Render(ctx context.Context, in RenderInput) (RenderOutput, error)
}
