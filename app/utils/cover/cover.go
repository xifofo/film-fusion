package cover

import (
	"context"
	"fmt"
)

// RenderWithTemplate 用指定模板 ID 渲染封面（业务侧统一入口）
func RenderWithTemplate(ctx context.Context, templateID string, in RenderInput) (RenderOutput, error) {
	if templateID == "" {
		templateID = DefaultTemplateID
	}
	tpl, err := Get(templateID)
	if err != nil {
		return RenderOutput{}, fmt.Errorf("获取模板失败: %w", err)
	}
	return tpl.Render(ctx, in)
}
