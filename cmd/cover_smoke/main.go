// 独立的封面合成冒烟测试
//
//	go run ./cmd/cover_smoke
//
// 不依赖 Emby，用程序生成的彩色矩形当"假海报"，验证 cover 包能跑通。
// 输出文件：./data/cover_smoke.jpg
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"os"

	"film-fusion/app/utils/cover"
)

func main() {
	posters := make([][]byte, 0, 9)
	colors := []color.RGBA{
		{R: 220, G: 80, B: 100, A: 255},
		{R: 230, G: 150, B: 100, A: 255},
		{R: 110, G: 180, B: 220, A: 255},
		{R: 90, G: 200, B: 160, A: 255},
		{R: 240, G: 200, B: 90, A: 255},
		{R: 180, G: 130, B: 220, A: 255},
		{R: 220, G: 120, B: 170, A: 255},
		{R: 100, G: 160, B: 240, A: 255},
		{R: 240, G: 100, B: 80, A: 255},
	}
	for i, c := range colors {
		posters = append(posters, makeFakePoster(c))
		fmt.Printf("生成假海报 %d: %v\n", i+1, c)
	}

	in := cover.RenderInput{
		Width:       1920,
		Height:      1080,
		JPEGQuality: 88,
		CNTitle:     "动漫",
		ENSubtitle:  "ANIME",
		Posters:     posters,
		FontCNPath:  "data/assets/fonts/SourceHanSansCN-Bold.otf",
		FontENPath:  "data/assets/fonts/Inter-Bold.ttf",
	}

	out, err := cover.RenderWithTemplate(context.Background(), cover.DefaultTemplateID, in)
	if err != nil {
		log.Fatalf("合成失败: %v", err)
	}

	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("创建 data 目录失败: %v", err)
	}
	outPath := "data/cover_smoke.jpg"
	if err := os.WriteFile(outPath, out.JPEG, 0o644); err != nil {
		log.Fatalf("写文件失败: %v", err)
	}
	fmt.Printf("\n✅ 合成完成: %s (%d bytes)\n", outPath, len(out.JPEG))
	fmt.Printf("   背景色: %v\n", out.BackgroundColors)
}

// makeFakePoster 生成一张 600x900 单色 JPG，用于代替真实海报
func makeFakePoster(c color.RGBA) []byte {
	const w, h = 600, 900
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	buf := &bytes.Buffer{}
	_ = jpeg.Encode(buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}
