package cover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand"
	"sort"

	"github.com/disintegration/imaging"
	"github.com/fogleman/gg"
	"golang.org/x/image/font"
)

// 复刻 MoviePilot mediacovergenerator 的 style_static_3.py（多图九宫格）
//
// 视觉要素（1080p 基准）：
//   - 3 列 × 3 行共 9 张海报
//   - 每张海报 410×610，圆角 46.1
//   - 海报自带右/下阴影（偏移 20×20，模糊 20，深黑 216）
//   - 列间距 100，行间距 22
//   - 三列整体 -15.8° 倾斜，中列向右错位，右列向上偏移
//   - 海报排序：posters[0] 放最显眼的中间 (col=0,row=1)，posters[8] 放最边角
//   - 背景：is_blur=true 时模糊主图 + 主色混合 + 渐变变亮 + 胶片噪点
//   - 左下文字：中文主标 + 彩色竖条 + 英文副标（模糊阴影）
//
// 画布固定 1080p 渲染，其余尺寸通过 scale = height/1080 缩放。
type staticTemplate3 struct{}

func (t *staticTemplate3) ID() string   { return "multi_grid" }
func (t *staticTemplate3) Name() string { return "多图九宫格（multi_grid）" }

func init() {
	Register(&staticTemplate3{})
}

// 1080p 基准布局常量（与 Python 版 POSTER_GEN_CONFIG 对齐）
const (
	mgRows            = 3
	mgCols            = 3
	mgMargin          = 22.0   // 行间距
	mgCornerRadius    = 46.1   // 海报圆角
	mgRotationAngle   = -15.8  // 每列的旋转角度（Pillow 逆时针为正，我们传给 imaging.Rotate 的是逆时针角度）
	mgStartX          = 835.0  // 第一列参考 x
	mgStartY          = -362.0 // 第一列参考 y（可为负，表示上方超出画布）
	mgColumnSpacing   = 100.0  // 列间距
	mgCellWidth       = 410.0
	mgCellHeight      = 610.0
	mgShadowOffset    = 18.0 // 海报本体阴影
	mgShadowBlur      = 30.0
	mgShadowAlpha     = 130 // 0-255，约 50% 透明，更柔和
	mgBlurSize        = 50.0
	mgColorRatio      = 0.80 // 背景色占比
	mgFilmGrain       = 0.03
	mgZHFontBase      = 170.0
	mgENFontBase      = 75.0
	mgTitleSpacing    = 110.0 // 中英文标题间距
	mgENLineSpacing   = 40.0
	mgTextShadowBlur  = 12.0
	mgTextShadowAlpha = 75
	// 文字与色块基准坐标（1080p 画布左下）
	mgZHBaseX    = 73.32
	mgZHBaseY    = 427.34
	mgENBaseX    = 124.68
	mgENBaseY    = 624.55
	mgBlockBaseX = 84.38
	mgBlockBaseY = 620.06
	mgBlockWidth = 21.51
)

func (t *staticTemplate3) Render(ctx context.Context, in RenderInput) (RenderOutput, error) {
	if in.Width <= 0 || in.Height <= 0 {
		in.Width, in.Height = 1920, 1080
	}
	if in.JPEGQuality <= 0 || in.JPEGQuality > 100 {
		in.JPEGQuality = 88
	}
	if len(in.Posters) == 0 {
		return RenderOutput{}, errors.New("没有可用的海报")
	}

	scale := float64(in.Height) / 1080.0
	s := func(v float64) float64 { return v * scale }

	// ===== 1. 解码海报 =====
	decoded := make([]image.Image, 0, len(in.Posters))
	for i, p := range in.Posters {
		img, err := DecodePoster(p)
		if err != nil {
			// 单张失败不致命，跳过
			continue
		}
		decoded = append(decoded, img)
		if i >= mgRows*mgCols-1 {
			break
		}
	}
	if len(decoded) == 0 {
		return RenderOutput{}, errors.New("所有海报均解码失败")
	}

	// ===== 2. 颜色：主色（用于背景混合）+ 文字阴影色 =====
	first := decoded[0]
	vibrant := ExtractMacaronColors(first, 6)
	var blurColor color.RGBA
	if len(vibrant) > 0 {
		blurColor = vibrant[0]
	} else {
		blurColor = color.RGBA{R: 237, G: 159, B: 77, A: 255}
	}

	// ===== 3. 背景：模糊原图 + 主色混合 + 左深右浅的变亮渐变 + 胶片噪点 =====
	bgImg := drawBlurredBackground(first, in.Width, in.Height, blurColor, s(mgBlurSize), mgColorRatio, 0.6)

	// ===== 4. 主画布 =====
	dc := gg.NewContext(in.Width, in.Height)
	dc.DrawImage(bgImg, 0, 0)

	// ===== 5. 按 custom_order 放置九宫格 =====
	// custom_order "315426987" → index -> posters 下标
	// 位置 (col, row)：col*3 + row 映射到 order 下标 i → posters[orderValue-1]
	//   i=0,(0,0)=3   i=1,(0,1)=1   i=2,(0,2)=5
	//   i=3,(1,0)=4   i=4,(1,1)=2   i=5,(1,2)=6
	//   i=6,(2,0)=9   i=7,(2,1)=8   i=8,(2,2)=7
	grid := [mgCols][mgRows]image.Image{}
	order := []int{3, 1, 5, 4, 2, 6, 9, 8, 7} // 1-based
	for i, v := range order {
		col := i / mgRows
		row := i % mgRows
		idx := v - 1
		if idx < len(decoded) {
			grid[col][row] = decoded[idx]
		}
	}

	// 每列独立渲染：先排 3 张海报+阴影 → 放大画布 → 旋转 → 贴主画布
	cellW := int(s(mgCellWidth))
	cellH := int(s(mgCellHeight))
	corner := int(s(mgCornerRadius))
	marginPx := int(s(mgMargin))
	// 海报阴影：用户反馈「阴影造成断层感」，已关闭。
	// 如果以后想开回海报阴影，下面一行 shadowEnabled 改为 true 即可。
	const shadowEnabled = false
	shOffset := int(math.Max(1, s(mgShadowOffset)))
	shBlur := int(math.Max(1, s(mgShadowBlur)))
	shExtraW := 0
	shExtraH := 0
	if shadowEnabled {
		shExtraW = shOffset + shBlur*2
		shExtraH = shOffset + shBlur*2
	}

	columnH := mgRows*cellH + (mgRows-1)*marginPx
	colXStep := int(math.Round(float64(cellW) - s(50)))
	col23Extra := int(math.Round(s(40)))

	for col := 0; col < mgCols; col++ {
		if ctx.Err() != nil {
			return RenderOutput{}, ctx.Err()
		}

		// 列画布（关闭阴影后无需额外 padding）
		colCanvas := image.NewNRGBA(image.Rect(0, 0, cellW+shExtraW, columnH+shExtraH))
		for row := 0; row < mgRows; row++ {
			src := grid[col][row]
			if src == nil {
				continue
			}
			poster := imaging.Fill(src, cellW, cellH, imaging.Center, imaging.Lanczos)
			rounded := applyRoundedCorners(poster, corner)
			var toPaste image.Image = rounded
			if shadowEnabled {
				toPaste = addRectShadow(rounded, shOffset, shOffset, color.NRGBA{0, 0, 0, mgShadowAlpha}, float64(shBlur))
			}
			py := row * (cellH + marginPx)
			drawImageAt(colCanvas, toPaste, 0, py)
		}

		// 列放入正方形大画布旋转
		diag := math.Sqrt(math.Pow(float64(cellW+shExtraW), 2) + math.Pow(float64(columnH+shExtraH), 2))
		rotationSize := int(diag * 1.5)
		rotCanvas := image.NewNRGBA(image.Rect(0, 0, rotationSize, rotationSize))
		pasteX := (rotationSize - cellW) / 2
		pasteY := (rotationSize - columnH) / 2
		drawImageAt(rotCanvas, colCanvas, pasteX, pasteY)

		// 旋转（imaging.Rotate 以逆时针为正；Python 的 Image.rotate 也是逆时针为正角度）
		rotated := imaging.Rotate(rotCanvas, mgRotationAngle, color.NRGBA{0, 0, 0, 0})

		// 计算列中心位置
		columnX := int(math.Round(s(mgStartX) + float64(col)*s(mgColumnSpacing)))
		columnCenterY := int(math.Round(s(mgStartY))) + columnH/2
		columnCenterX := columnX
		switch col {
		case 1:
			columnCenterX += colXStep
		case 2:
			columnCenterY += int(math.Round(s(-155)))
			columnCenterX += colXStep*2 + col23Extra
		}
		finalX := columnCenterX - rotated.Bounds().Dx()/2 + cellW/2
		finalY := columnCenterY - rotated.Bounds().Dy()/2

		dc.DrawImage(rotated, finalX, finalY)
	}

	// ===== 6. 左下文字层 =====
	// 随机点颜色（色块用）
	blockColor := getRandomPixelColor(first)

	if err := drawTextLayer_static3(dc, in, scale, blurColor, blockColor); err != nil {
		return RenderOutput{}, err
	}

	// ===== 7. 输出 =====
	buf := &bytes.Buffer{}
	if err := jpeg.Encode(buf, dc.Image(), &jpeg.Options{Quality: in.JPEGQuality}); err != nil {
		return RenderOutput{}, fmt.Errorf("JPEG 编码失败: %w", err)
	}
	return RenderOutput{
		JPEG:             buf.Bytes(),
		Width:            in.Width,
		Height:           in.Height,
		BackgroundColors: []color.RGBA{blurColor},
	}, nil
}

// drawBlurredBackground 模仿 create_blur_background：
//
//	bg = fit(src, W, H) → blur(blurSize) → blend(darken(blurColor,0.85), colorRatio) → lighten gradient → film grain
func drawBlurredBackground(src image.Image, w, h int, blurColor color.RGBA, blurSize, colorRatio, lightenStrength float64) image.Image {
	fit := imaging.Fill(src, w, h, imaging.Center, imaging.Lanczos)
	blurred := imaging.Blur(fit, blurSize)
	darkened := DarkenColor(blurColor, 0.85)
	mixed := blendImageWithColor(blurred, darkened, colorRatio)

	// 左深右浅的变亮渐变：x 越大，叠加白色的 alpha 越大
	if lightenStrength > 0 {
		maxAlpha := int(math.Min(1, math.Max(0, lightenStrength)) * 255)
		out := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				alphaOverlay := int(float64(x) / float64(w) * float64(maxAlpha))
				// 原色
				bc := mixed.NRGBAAt(x, y)
				// 白色叠加 alpha-blending：out = src * (1-a) + white * a
				a := float64(alphaOverlay) / 255.0
				r := float64(bc.R)*(1-a) + 255*a
				g := float64(bc.G)*(1-a) + 255*a
				b := float64(bc.B)*(1-a) + 255*a
				out.SetNRGBA(x, y, color.NRGBA{clampUint8(r), clampUint8(g), clampUint8(b), 255})
			}
		}
		return addFilmGrain(out, mgFilmGrain)
	}
	return addFilmGrain(mixed, mgFilmGrain)
}

// addRectShadow 给带 alpha 的图像添加右下投影
// 返回一张比原图大（offsetX+blur*2, offsetY+blur*2）的 image，左上放原图，右下偏移位置放模糊阴影。
func addRectShadow(src image.Image, offsetX, offsetY int, shadowColor color.NRGBA, blurRadius float64) image.Image {
	sw := src.Bounds().Dx()
	sh := src.Bounds().Dy()
	blurInt := int(blurRadius)
	outW := sw + offsetX + blurInt*2
	outH := sh + offsetY + blurInt*2

	// 阴影层：与原图等大，填充 shadowColor + 原图 alpha 作为 mask
	shadowLayer := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	switch s := src.(type) {
	case *image.NRGBA:
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				srcA := s.NRGBAAt(x, y).A
				if srcA == 0 {
					continue
				}
				a := uint8(int(srcA) * int(shadowColor.A) / 255)
				shadowLayer.SetNRGBA(x, y, color.NRGBA{shadowColor.R, shadowColor.G, shadowColor.B, a})
			}
		}
	case *image.RGBA:
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				srcA := s.RGBAAt(x, y).A
				if srcA == 0 {
					continue
				}
				a := uint8(int(srcA) * int(shadowColor.A) / 255)
				shadowLayer.SetNRGBA(x, y, color.NRGBA{shadowColor.R, shadowColor.G, shadowColor.B, a})
			}
		}
	default:
		b := src.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				_, _, _, a32 := src.At(x, y).RGBA()
				if a32 == 0 {
					continue
				}
				a := uint8(int(uint8(a32>>8)) * int(shadowColor.A) / 255)
				shadowLayer.SetNRGBA(x, y, color.NRGBA{shadowColor.R, shadowColor.G, shadowColor.B, a})
			}
		}
	}

	// 把阴影层放到 (blur+offsetX, blur+offsetY) 位置的大画布
	shadowCanvas := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	drawImageAt(shadowCanvas, shadowLayer, blurInt+offsetX, blurInt+offsetY)
	// 模糊整个画布
	blurredShadow := imaging.Blur(shadowCanvas, blurRadius)

	// 最终合成：blurredShadow 为底，原图放 (blur, blur)
	result := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	drawImageAt(result, blurredShadow, 0, 0)
	drawImageAt(result, src, blurInt, blurInt)
	return result
}

// drawImageAt 把 src 以 alpha-over 模式贴到 dst 的 (dx, dy) 位置
// 用标准库 draw.Draw 的 draw.Over 模式，它自带 premultiplied 处理，避免手写合成出错
func drawImageAt(dst *image.NRGBA, src image.Image, dx, dy int) {
	sb := src.Bounds()
	r := image.Rect(dx, dy, dx+sb.Dx(), dy+sb.Dy())
	draw.Draw(dst, r, src, sb.Min, draw.Over)
}

// getRandomPixelColor 从 src 的中心偏右区域 (50%~80%) 随机取一个像素
func getRandomPixelColor(src image.Image) color.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	x := int(float64(w)*0.5) + rand.Intn(int(float64(w)*0.3)+1)
	y := int(float64(h)*0.5) + rand.Intn(int(float64(h)*0.3)+1)
	r32, g32, b32, _ := src.At(x, y).RGBA()
	return color.RGBA{uint8(r32 >> 8), uint8(g32 >> 8), uint8(b32 >> 8), 255}
}

// drawTextLayer_static3 按 1080p 坐标 + scale 绘制左下文字 + 色块
func drawTextLayer_static3(dc *gg.Context, in RenderInput, scale float64, blurColor, blockColor color.RGBA) error {
	if in.CNTitle == "" && in.ENSubtitle == "" {
		return nil
	}
	s := func(v float64) float64 { return v * scale }

	// ----- 字体 -----
	fontCN, err := LoadSfntFont(in.FontCNPath)
	if err != nil {
		return fmt.Errorf("加载中文字体失败: %w", err)
	}
	zhFace, err := NewFace(fontCN, mgZHFontBase*scale)
	if err != nil {
		return err
	}
	defer zhFace.Close()

	var enFace font.Face
	if in.ENSubtitle != "" && in.FontENPath != "" {
		fontEN, errEN := LoadSfntFont(in.FontENPath)
		if errEN != nil {
			return fmt.Errorf("加载英文字体失败: %w", errEN)
		}
		f, errFace := NewFace(fontEN, mgENFontBase*scale)
		if errFace != nil {
			return errFace
		}
		enFace = f
		defer enFace.Close()
	}

	// ----- 中文阴影 + 主字 -----
	shCol := DarkenColor(blurColor, 0.8)
	shadowLayer := gg.NewContext(in.Width, in.Height)
	shadowLayer.SetFontFace(zhFace)
	shadowLayer.SetRGBA(float64(shCol.R)/255, float64(shCol.G)/255, float64(shCol.B)/255, float64(mgTextShadowAlpha)/255)

	zhX := s(mgZHBaseX)
	_, zhH := shadowLayer.MeasureString(in.CNTitle)
	zhBaselineY := s(mgZHBaseY) + zhH // gg 的 DrawString y 是 baseline，这里把左上角转成 baseline
	for off := 3; off <= int(mgTextShadowBlur); off += 2 {
		shadowLayer.DrawString(in.CNTitle, zhX+float64(off), zhBaselineY+float64(off))
	}

	// ----- 英文分行（可选）+ 色块 -----
	var enLines []string
	var enFontSize float64
	var enLineH float64
	if in.ENSubtitle != "" && enFace != nil {
		enFontSize = mgENFontBase * scale
		// Python 版根据单词数/最长单词自适应缩放
		enFontSize = adjustENFontSize(in.ENSubtitle, enFontSize)
		if enFontSize != mgENFontBase*scale {
			// 重新构造合适大小的 en face
			fontEN, errEN := LoadSfntFont(in.FontENPath)
			if errEN == nil {
				if f, errFace := NewFace(fontEN, enFontSize); errFace == nil {
					enFace.Close()
					enFace = f
				}
			}
		}

		shadowLayer.SetFontFace(enFace)
		_, enLineH = shadowLayer.MeasureString("Ag")

		// 英文是否需要分行：当英文宽 > 中文宽
		zhWidth, _ := shadowLayer.MeasureString(in.CNTitle)
		enWidth, _ := (func() (float64, float64) {
			shadowLayer.SetFontFace(enFace)
			w, h := shadowLayer.MeasureString(in.ENSubtitle)
			return w, h
		})()
		isMultiline := enWidth > zhWidth && containsSpace(in.ENSubtitle)
		if isMultiline {
			enLines = splitWords(in.ENSubtitle) // 每词一行（与 Python 版一致）
		} else {
			enLines = []string{in.ENSubtitle}
		}

		// 英文阴影
		enBaseX := s(mgENBaseX)
		enBaseY := s(mgENBaseY) + s(mgTitleSpacing) + enLineH
		shadowLayer.SetFontFace(enFace)
		shadowLayer.SetRGBA(float64(shCol.R)/255, float64(shCol.G)/255, float64(shCol.B)/255, float64(mgTextShadowAlpha)/255)
		for i, line := range enLines {
			ly := enBaseY + float64(i)*(enLineH+s(mgENLineSpacing))
			for off := 3; off <= int(mgTextShadowBlur); off += 2 {
				shadowLayer.DrawString(line, enBaseX+float64(off), ly+float64(off))
			}
		}
		_ = zhWidth
	}

	// 模糊 shadow 层并贴回
	blurredShadow := imaging.Blur(shadowLayer.Image(), mgTextShadowBlur)
	dc.DrawImage(blurredShadow, 0, 0)

	// ----- 中文主字 -----
	dc.SetFontFace(zhFace)
	dc.SetRGBA(1, 1, 1, 229.0/255.0)
	dc.DrawString(in.CNTitle, zhX, zhBaselineY)

	// ----- 英文主字 + 色块 -----
	if len(enLines) > 0 && enFace != nil {
		dc.SetFontFace(enFace)
		dc.SetRGBA(1, 1, 1, 229.0/255.0)
		enBaseX := s(mgENBaseX)
		enBaseY := s(mgENBaseY) + s(mgTitleSpacing) + enLineH
		for i, line := range enLines {
			ly := enBaseY + float64(i)*(enLineH+s(mgENLineSpacing))
			dc.DrawString(line, enBaseX, ly)
		}

		// 色块：左侧竖条（短版，只包住英文文字本身）
		blockW := s(mgBlockWidth)
		blockX := s(mgBlockBaseX)
		// 色块高 = N行文字高 + (N-1)行间距，不加额外 padding
		blockH := float64(len(enLines))*enLineH + float64(len(enLines)-1)*s(mgENLineSpacing)
		// 色块顶 ≈ 第一行英文 baseline - enLineH*0.85（对齐字体顶部）
		blockY := enBaseY - enLineH*0.85
		dc.SetRGBA(float64(blockColor.R)/255, float64(blockColor.G)/255, float64(blockColor.B)/255, 1)
		dc.DrawRectangle(blockX, blockY, blockW, blockH)
		dc.Fill()
	}
	return nil
}

// adjustENFontSize 复刻 Python 版自适应英文字体大小逻辑
func adjustENFontSize(enTitle string, baseFontSize float64) float64 {
	words := splitWords(enTitle)
	if len(words) == 0 {
		return baseFontSize
	}
	wordCount := len(words)
	maxLen := 0
	for _, w := range words {
		if len(w) > maxLen {
			maxLen = len(w)
		}
	}
	if maxLen > 10 || wordCount > 3 {
		// scale_factor = (10 / max(maxLen, wordCount * 3)) ** 0.8
		denom := float64(maxLen)
		if float64(wordCount)*3 > denom {
			denom = float64(wordCount) * 3
		}
		scaleFactor := math.Pow(10/denom, 0.8)
		if scaleFactor < 0.4 {
			scaleFactor = 0.4
		}
		size := baseFontSize * scaleFactor
		if size < 30 {
			size = 30
		}
		return size
	}
	return baseFontSize
}

// sortPosters 按给定的 1-based index 顺序抽取海报（不足的位置返回 nil）
// 用于验证排序映射——当前未直接使用，保留可读性
func sortPosters(posters []image.Image, order []int) []image.Image {
	out := make([]image.Image, len(order))
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	for i, v := range order {
		if v-1 < len(posters) {
			out[i] = posters[v-1]
		}
	}
	return out
}
