package cover

import (
	"bytes"
	"image"
	"image/color"
	_ "image/jpeg" // 注册 JPEG 解码
	_ "image/png"  // 注册 PNG 解码
	"math"
	"sort"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // 支持 WebP 海报
)

// DecodePoster 把字节解码成 image.Image（自动识别格式）
func DecodePoster(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// ExtractMacaronColors 从图像里提取若干"马卡龙风格"主色。
//
// 算法移植自 MoviePilot mediacovergenerator (color_helper.py)：
//  1. 缩小图像到 100x100 加速统计
//  2. 量化到 RGB 5bit/通道（32 级），过滤黑/白/灰
//  3. 频率排序取候选
//  4. 在 HSV 空间调整到马卡龙范围（饱和度 0.3-0.7，亮度 0.6-0.85）
//  5. 用 HSV 距离去重（>0.15 才算不同色）
//
// 返回最多 max 个色；图像几乎全黑/全白时返回空切片。
func ExtractMacaronColors(img image.Image, max int) []color.RGBA {
	if max <= 0 {
		max = 5
	}
	small := imaging.Resize(img, 100, 0, imaging.Lanczos)
	bounds := small.Bounds()

	// 量化桶 (R<<10) | (G<<5) | B  —— 5bit 精度
	type bucket struct {
		r, g, b uint8
		count   int
	}
	histogram := make(map[uint16]*bucket, 1024)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r32, g32, b32, a32 := small.At(x, y).RGBA()
			if a32 < 0x8000 {
				continue
			}
			r := uint8(r32 >> 8)
			g := uint8(g32 >> 8)
			b := uint8(b32 >> 8)
			if !isColorful(r, g, b, 20, 10) {
				continue
			}
			qr := r >> 3
			qg := g >> 3
			qb := b >> 3
			key := uint16(qr)<<10 | uint16(qg)<<5 | uint16(qb)
			if existing, ok := histogram[key]; ok {
				existing.count++
				existing.r = (existing.r + r) / 2
				existing.g = (existing.g + g) / 2
				existing.b = (existing.b + b) / 2
			} else {
				histogram[key] = &bucket{r: r, g: g, b: b, count: 1}
			}
		}
	}

	if len(histogram) == 0 {
		return nil
	}

	candidates := make([]*bucket, 0, len(histogram))
	for _, b := range histogram {
		candidates = append(candidates, b)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].count > candidates[j].count })

	const maxCandidates = 32
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	const minHSVDistance = 0.15
	picked := make([]color.RGBA, 0, max)
	for _, c := range candidates {
		adj := adjustToMacaron(color.RGBA{R: c.r, G: c.g, B: c.b, A: 255})
		duplicate := false
		for _, p := range picked {
			if hsvDistance(adj, p) < minHSVDistance {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		picked = append(picked, adj)
		if len(picked) >= max {
			break
		}
	}
	return picked
}

// PickPrimaryAndAccent 从主色集合里挑出"主色"和"辅助色"用于背景渐变。
// 主色取频率最高的，辅助色取与主色色相差最大的；不足时返回兜底深色。
func PickPrimaryAndAccent(colors []color.RGBA) (primary, accent color.RGBA) {
	switch len(colors) {
	case 0:
		// 兜底：深酒红，类似参考图
		return color.RGBA{R: 90, G: 24, B: 36, A: 255}, color.RGBA{R: 30, G: 12, B: 18, A: 255}
	case 1:
		// 用 darken 的同色作为渐变终点
		return colors[0], DarkenColor(colors[0], 0.45)
	}
	primary = colors[0]
	bestDist := -1.0
	for _, c := range colors[1:] {
		d := hsvDistance(primary, c)
		if d > bestDist {
			bestDist = d
			accent = c
		}
	}
	// 让 accent 更暗一些，做出深浅对比
	accent = DarkenColor(accent, 0.55)
	return primary, accent
}

// DarkenColor 按比例加深颜色
func DarkenColor(c color.RGBA, factor float64) color.RGBA {
	if factor < 0 {
		factor = 0
	}
	if factor > 1 {
		factor = 1
	}
	return color.RGBA{
		R: uint8(float64(c.R) * factor),
		G: uint8(float64(c.G) * factor),
		B: uint8(float64(c.B) * factor),
		A: c.A,
	}
}

// adjustToMacaron 把颜色调整到马卡龙风格的 HSV 范围
func adjustToMacaron(c color.RGBA) color.RGBA {
	h, s, v := rgbToHSV(c)
	const (
		minS = 0.30
		maxS = 0.70
		minV = 0.60
		maxV = 0.85
	)
	if s < minS {
		s = minS
	} else if s > maxS {
		s = maxS
	}
	if v < minV {
		v = minV
	} else if v > maxV {
		v = maxV
	}
	return hsvToRGB(h, s, v)
}

// hsvDistance HSV 空间距离（色相加权 5 倍）
func hsvDistance(a, b color.RGBA) float64 {
	h1, s1, v1 := rgbToHSV(a)
	h2, s2, v2 := rgbToHSV(b)
	dh := math.Abs(h1 - h2)
	if dh > 0.5 {
		dh = 1 - dh
	}
	return dh*5 + math.Abs(s1-s2) + math.Abs(v1-v2)
}

// isColorful 判断颜色是否为有色（既不是黑/白接近色，也不是灰阶）
func isColorful(r, g, b uint8, edgeThreshold, grayThreshold int) bool {
	rr, gg, bb := int(r), int(g), int(b)
	if (rr < edgeThreshold && gg < edgeThreshold && bb < edgeThreshold) ||
		(rr > 255-edgeThreshold && gg > 255-edgeThreshold && bb > 255-edgeThreshold) {
		return false
	}
	if absInt(rr-gg) < grayThreshold && absInt(gg-bb) < grayThreshold && absInt(rr-bb) < grayThreshold {
		return false
	}
	return true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// rgbToHSV RGB(0-255) -> HSV(0-1, 0-1, 0-1)
func rgbToHSV(c color.RGBA) (h, s, v float64) {
	r := float64(c.R) / 255.0
	g := float64(c.G) / 255.0
	b := float64(c.B) / 255.0
	maxV := math.Max(r, math.Max(g, b))
	minV := math.Min(r, math.Min(g, b))
	v = maxV
	delta := maxV - minV
	if maxV == 0 {
		s = 0
	} else {
		s = delta / maxV
	}
	if delta == 0 {
		h = 0
		return
	}
	switch maxV {
	case r:
		h = (g - b) / delta
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/delta + 2
	case b:
		h = (r-g)/delta + 4
	}
	h /= 6
	return
}

// hsvToRGB HSV(0-1, 0-1, 0-1) -> RGBA(0-255)
func hsvToRGB(h, s, v float64) color.RGBA {
	if s == 0 {
		c := uint8(v * 255)
		return color.RGBA{R: c, G: c, B: c, A: 255}
	}
	h6 := h * 6
	i := int(math.Floor(h6))
	f := h6 - float64(i)
	p := v * (1 - s)
	q := v * (1 - s*f)
	t := v * (1 - s*(1-f))
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	case 5:
		r, g, b = v, p, q
	}
	return color.RGBA{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255), A: 255}
}
