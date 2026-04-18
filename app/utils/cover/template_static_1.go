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

	"github.com/disintegration/imaging"
	"github.com/fogleman/gg"
	"golang.org/x/image/font"
)

// 复刻 MoviePilot mediacovergenerator 的 style_static_1.py
//
// 风格特点：
//   - 输入 1 张主海报，画布右侧叠 3 张正方形卡片（旋转 0/18/36 度）
//   - 顶层是原图圆角；中间层=原图模糊+第二主色 50% 混合；底层=更深模糊+第三主色 60% 混合
//   - 每张卡片各自有阴影配置（顶层最深最大）
//   - 背景=原图模糊 + 主色（默认 80%）混合 + 胶片噪点
//   - 文字在画布左 1/4 处垂直居中：中文(170)+ 英文(75) + 模糊文字阴影
type staticTemplate1 struct{}

func (t *staticTemplate1) ID() string   { return "static_1" }
func (t *staticTemplate1) Name() string { return "单图三卡堆叠（static_1）" }

func init() {
	Register(&staticTemplate1{})
}

// 默认参数（对照 Python 版）
const (
	st1ZHFontSizeBase = 170.0
	st1ENFontSizeBase = 75.0
	st1BlurSize       = 50.0
	st1ColorRatio     = 0.80
	st1ZHFontOffset   = 0.0  // 中文标题相对垂直中心的额外偏移（像素，按 1080p 基准）
	st1TitleSpacing   = 40.0 // 中英文标题间距
	st1ENLineSpacing  = 40.0 // 英文行间距
	st1FilmGrain      = 0.03 // 胶片颗粒强度
	st1ShadowOffset   = 12   // 文字阴影最大偏移
	st1ShadowAlpha    = 75   // 文字阴影透明度
)

func (t *staticTemplate1) Render(ctx context.Context, in RenderInput) (RenderOutput, error) {
	if in.Width <= 0 || in.Height <= 0 {
		in.Width, in.Height = 1920, 1080
	}
	if in.JPEGQuality <= 0 || in.JPEGQuality > 100 {
		in.JPEGQuality = 88
	}
	if len(in.Posters) == 0 {
		return RenderOutput{}, errors.New("没有可用的海报")
	}

	// 用第一张海报作为主图
	mainImg, err := DecodePoster(in.Posters[0])
	if err != nil {
		return RenderOutput{}, fmt.Errorf("解码主海报失败: %w", err)
	}
	mainRGB := imaging.Clone(mainImg)
	scale := float64(in.Height) / 1080.0

	// ===== 1. 颜色提取 =====
	macarons := ExtractMacaronColors(mainRGB, 6)
	if len(macarons) == 0 {
		// 兜底色
		macarons = []color.RGBA{
			{R: 237, G: 159, B: 77, A: 255},
			{R: 186, G: 225, B: 255, A: 255},
			{R: 255, G: 223, B: 186, A: 255},
		}
	}
	// Python 版会 shuffle 后取前 num_colors，这里也做（增加多样性）
	rand.Shuffle(len(macarons), func(i, j int) { macarons[i], macarons[j] = macarons[j], macarons[i] })
	bgColor := DarkenColor(macarons[0], 0.85)

	// 卡片颜色：从图里取 3 个有差异的主色，取 [1] [2] 给辅助卡 1/2
	cardColors := ExtractMacaronColors(mainRGB, 3)
	for len(cardColors) < 3 {
		cardColors = append(cardColors, color.RGBA{R: 186, G: 225, B: 255, A: 255})
	}
	card1Color := cardColors[1] // 辅助卡 1 混色
	card2Color := cardColors[2] // 辅助卡 2 混色

	// ===== 2. 背景：原图 fit → 模糊 → 与主色混合 → 加噪点 =====
	bgFit := imaging.Fill(mainRGB, in.Width, in.Height, imaging.Center, imaging.Lanczos)
	bgBlur := imaging.Blur(bgFit, st1BlurSize)
	bgMixed := blendImageWithColor(bgBlur, bgColor, st1ColorRatio)
	bgWithGrain := addFilmGrain(bgMixed, st1FilmGrain)

	// ===== 3. 主画布 + 把背景画上去 =====
	dc := gg.NewContext(in.Width, in.Height)
	dc.DrawImage(bgWithGrain, 0, 0)

	// ===== 4. 三张卡片：底→中→顶 =====
	// 卡片是正方形，边长 = canvas.h * 0.7
	cardSize := int(float64(in.Height) * 0.7)
	cornerRadius := cardSize / 8

	mainSquare := imaging.Fill(mainRGB, cardSize, cardSize, imaging.Center, imaging.Lanczos)

	// 顶层：原图 + 圆角
	mainCard := applyRoundedCorners(mainSquare, cornerRadius)

	// 中间层：模糊 8 + 与 card1Color 50/50 混合 → 圆角
	aux1Blur := imaging.Blur(mainSquare, 8)
	aux1Mix := blendImageWithColor(aux1Blur, card1Color, 0.5)
	aux1Card := applyRoundedCorners(aux1Mix, cornerRadius)

	// 底层：模糊 16 + 与 card2Color 40/60 混合（颜色占 60%）→ 圆角
	aux2Blur := imaging.Blur(mainSquare, 16)
	aux2Mix := blendImageWithColor(aux2Blur, card2Color, 0.6)
	aux2Card := applyRoundedCorners(aux2Mix, cornerRadius)

	// 卡片中心位置：右侧（canvas.w - canvas.h*0.5, canvas.h/2）
	centerX := in.Width - int(float64(in.Height)*0.5)
	centerY := in.Height / 2

	// 阴影/旋转配置（自下而上）
	type cardLayer struct {
		img      image.Image
		angleDeg float64
		shadow   shadowConfig
	}
	layers := []cardLayer{
		{aux2Card, 36, shadowConfig{offsetX: 10, offsetY: 16, blur: 12, opacity: 0.4}},
		{aux1Card, 18, shadowConfig{offsetX: 15, offsetY: 22, blur: 15, opacity: 0.5}},
		{mainCard, 0, shadowConfig{offsetX: 20, offsetY: 26, blur: 18, opacity: 0.6}},
	}

	for _, l := range layers {
		if ctx.Err() != nil {
			return RenderOutput{}, ctx.Err()
		}
		drawCardWithShadowAndRotate(dc, l.img, l.angleDeg, centerX, centerY, l.shadow)
	}

	// ===== 5. 文字层 =====
	if err := drawTextLayer_static1(ctx, dc, in, scale, bgColor); err != nil {
		return RenderOutput{}, err
	}

	// ===== 6. 输出 =====
	buf := &bytes.Buffer{}
	if err := jpeg.Encode(buf, dc.Image(), &jpeg.Options{Quality: in.JPEGQuality}); err != nil {
		return RenderOutput{}, fmt.Errorf("JPEG 编码失败: %w", err)
	}
	return RenderOutput{
		JPEG:             buf.Bytes(),
		Width:            in.Width,
		Height:           in.Height,
		BackgroundColors: []color.RGBA{bgColor, card1Color, card2Color},
	}, nil
}

// shadowConfig 单个卡片的阴影参数
type shadowConfig struct {
	offsetX, offsetY int
	blur             float64
	opacity          float64
}

// drawCardWithShadowAndRotate 复刻 add_shadow_and_rotate：
// 先画一个比卡片稍大、模糊、按 opacity 半透明的暗块作为阴影；旋转阴影后贴画布；
// 再旋转卡片自身，居中贴在 (cx, cy)。
func drawCardWithShadowAndRotate(dc *gg.Context, card image.Image, angleDeg float64, cx, cy int, sc shadowConfig) {
	w := card.Bounds().Dx()
	h := card.Bounds().Dy()

	// 1) 创建带阴影的临时画布（padding 提供模糊空间）
	pad := int(math.Max(sc.blur*4, 100))
	shadowBuf := image.NewRGBA(image.Rect(0, 0, w+pad*2, h+pad*2))

	// 用卡片自身的 alpha 通道作为蒙版填充黑色（这样阴影形状就是带圆角的）
	shadowAlpha := uint8(sc.opacity * 255)
	cardAlpha := extractAlphaMask(card)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := cardAlpha.AlphaAt(x, y).A
			if a == 0 {
				continue
			}
			// 输出 alpha = 卡片 alpha * shadow opacity
			finalA := uint8(int(a) * int(shadowAlpha) / 255)
			shadowBuf.SetRGBA(x+pad, y+pad, color.RGBA{0, 0, 0, finalA})
		}
	}
	// 2) 高斯模糊
	blurred := imaging.Blur(shadowBuf, sc.blur)

	// 3) 旋转阴影
	rotatedShadow := imaging.Rotate(blurred, angleDeg, color.NRGBA{0, 0, 0, 0})
	// 4) 居中贴上（带 offset）
	sx := cx - rotatedShadow.Bounds().Dx()/2 + sc.offsetX
	sy := cy - rotatedShadow.Bounds().Dy()/2 + sc.offsetY
	dc.DrawImage(rotatedShadow, sx, sy)

	// 5) 旋转卡片本体并居中贴
	rotatedCard := imaging.Rotate(card, angleDeg, color.NRGBA{0, 0, 0, 0})
	rx := cx - rotatedCard.Bounds().Dx()/2
	ry := cy - rotatedCard.Bounds().Dy()/2
	dc.DrawImage(rotatedCard, rx, ry)
}

// extractAlphaMask 从图像取 alpha 通道；非 RGBA 图像视为完全不透明
func extractAlphaMask(img image.Image) *image.Alpha {
	b := img.Bounds()
	out := image.NewAlpha(b)
	switch src := img.(type) {
	case *image.NRGBA:
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				out.SetAlpha(x, y, color.Alpha{A: src.NRGBAAt(x, y).A})
			}
		}
	case *image.RGBA:
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				out.SetAlpha(x, y, color.Alpha{A: src.RGBAAt(x, y).A})
			}
		}
	default:
		// 假设完全不透明
		draw.Draw(out, b, &image.Uniform{C: color.Alpha{A: 255}}, image.Point{}, draw.Src)
	}
	return out
}

// applyRoundedCorners 用 fogleman/gg 给 image 加圆角，返回 image.NRGBA
func applyRoundedCorners(src image.Image, radius int) image.Image {
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	dc := gg.NewContext(w, h)
	dc.DrawRoundedRectangle(0, 0, float64(w), float64(h), float64(radius))
	dc.Clip()
	dc.DrawImage(src, 0, 0)
	return dc.Image()
}

// blendImageWithColor 把图与单一颜色按 colorRatio 混合
// out = src * (1 - colorRatio) + color * colorRatio
func blendImageWithColor(src image.Image, c color.RGBA, colorRatio float64) *image.NRGBA {
	if colorRatio < 0 {
		colorRatio = 0
	}
	if colorRatio > 1 {
		colorRatio = 1
	}
	srcRatio := 1 - colorRatio
	b := src.Bounds()
	out := image.NewNRGBA(b)
	cr, cg, cb := float64(c.R), float64(c.G), float64(c.B)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r32, g32, b32, _ := src.At(x, y).RGBA()
			r := float64(uint8(r32>>8))*srcRatio + cr*colorRatio
			g := float64(uint8(g32>>8))*srcRatio + cg*colorRatio
			bv := float64(uint8(b32>>8))*srcRatio + cb*colorRatio
			out.SetNRGBA(x, y, color.NRGBA{
				R: clampUint8(r),
				G: clampUint8(g),
				B: clampUint8(bv),
				A: 255,
			})
		}
	}
	return out
}

// addFilmGrain 加胶片颗粒（高斯噪声）
func addFilmGrain(src image.Image, intensity float64) image.Image {
	if intensity <= 0 {
		return src
	}
	b := src.Bounds()
	out := image.NewNRGBA(b)
	stddev := intensity * 255
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r32, g32, b32, _ := src.At(x, y).RGBA()
			noise := rand.NormFloat64() * stddev
			out.SetNRGBA(x, y, color.NRGBA{
				R: clampUint8(float64(uint8(r32>>8)) + noise),
				G: clampUint8(float64(uint8(g32>>8)) + noise),
				B: clampUint8(float64(uint8(b32>>8)) + noise),
				A: 255,
			})
		}
	}
	return out
}

func clampUint8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// drawTextLayer_static1 在画布左 1/4 处垂直居中绘制中英文标题（带模糊阴影）
func drawTextLayer_static1(ctx context.Context, dc *gg.Context, in RenderInput, scale float64, bgColor color.RGBA) error {
	if in.CNTitle == "" && in.ENSubtitle == "" {
		return nil
	}

	// ----- 字体 -----
	fontCN, err := LoadSfntFont(in.FontCNPath)
	if err != nil {
		return fmt.Errorf("加载中文字体失败: %w", err)
	}
	zhFace, err := NewFace(fontCN, st1ZHFontSizeBase*scale)
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
		f, errFace := NewFace(fontEN, st1ENFontSizeBase*scale)
		if errFace != nil {
			return errFace
		}
		enFace = f
		defer enFace.Close()
	}

	// ----- 测量 -----
	leftCenterX := float64(in.Width) * 0.25
	leftCenterY := float64(in.Height) * 0.5

	dc.SetFontFace(zhFace)
	zhTextW, zhTextH := dc.MeasureString(in.CNTitle)

	titleSpacing := st1TitleSpacing * scale
	if in.ENSubtitle == "" || enFace == nil {
		titleSpacing = 0
	}

	// 英文分行：如果英文比中文宽并且包含空格，则按词分行
	enLines := []string{}
	enLineH := 0.0
	totalEnH := 0.0
	if in.ENSubtitle != "" && enFace != nil {
		dc.SetFontFace(enFace)
		_, enLineH = dc.MeasureString("Ag")
		enFullW, _ := dc.MeasureString(in.ENSubtitle)
		if enFullW > zhTextW && containsSpace(in.ENSubtitle) {
			enLines = wrapEnglishToWidth(dc, in.ENSubtitle, zhTextW)
		} else {
			enLines = []string{in.ENSubtitle}
		}
		totalEnH = float64(len(enLines))*enLineH + float64(len(enLines)-1)*st1ENLineSpacing*scale
	}

	totalH := zhTextH + titleSpacing + totalEnH
	totalY := leftCenterY - totalH/2

	zhX := leftCenterX - zhTextW/2
	zhY := totalY + st1ZHFontOffset*scale

	// ----- 文字阴影：先在临时层画暗色文字 → 高斯模糊 → 贴回 -----
	shadowDC := gg.NewContext(in.Width, in.Height)
	shadowDC.SetFontFace(zhFace)
	shCol := DarkenColor(bgColor, 0.8)
	shadowDC.SetRGBA(float64(shCol.R)/255, float64(shCol.G)/255, float64(shCol.B)/255, float64(st1ShadowAlpha)/255)
	for off := 3; off <= st1ShadowOffset; off += 2 {
		shadowDC.DrawString(in.CNTitle, zhX+float64(off), zhY+float64(off)+zhTextH)
	}
	if len(enLines) > 0 {
		shadowDC.SetFontFace(enFace)
		enY := zhY + zhTextH + titleSpacing
		for i, line := range enLines {
			lineW, _ := shadowDC.MeasureString(line)
			enX := leftCenterX - lineW/2
			y := enY + float64(i)*(enLineH+st1ENLineSpacing*scale) + enLineH
			for off := 2; off <= st1ShadowOffset/2; off++ {
				shadowDC.DrawString(line, enX+float64(off), y+float64(off))
			}
		}
	}
	blurredShadow := imaging.Blur(shadowDC.Image(), float64(st1ShadowOffset))
	dc.DrawImage(blurredShadow, 0, 0)

	// ----- 主文字 -----
	dc.SetFontFace(zhFace)
	dc.SetRGBA(1, 1, 1, 229.0/255.0) // 与 Python 版一致
	dc.DrawString(in.CNTitle, zhX, zhY+zhTextH)

	if len(enLines) > 0 {
		dc.SetFontFace(enFace)
		enY := zhY + zhTextH + titleSpacing
		for i, line := range enLines {
			lineW, _ := dc.MeasureString(line)
			enX := leftCenterX - lineW/2
			y := enY + float64(i)*(enLineH+st1ENLineSpacing*scale) + enLineH
			dc.DrawString(line, enX, y)
		}
	}
	return nil
}

func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' {
			return true
		}
	}
	return false
}

// wrapEnglishToWidth 按词把英文标题分行，每行不超过 maxWidth
func wrapEnglishToWidth(dc *gg.Context, s string, maxWidth float64) []string {
	out := []string{}
	if s == "" {
		return out
	}
	words := splitWords(s)
	if len(words) == 0 {
		return []string{s}
	}
	current := words[0]
	for i := 1; i < len(words); i++ {
		test := current + " " + words[i]
		w, _ := dc.MeasureString(test)
		if w > maxWidth {
			out = append(out, current)
			current = words[i]
		} else {
			current = test
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func splitWords(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
