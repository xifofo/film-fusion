package service

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"time"

	"film-fusion/app/utils/cover"

	"github.com/disintegration/imaging"
	"github.com/fogleman/gg"
	"golang.org/x/image/font/sfnt"
)

// 分享图尺寸（竖版，适合手机分享）
const (
	shareW = 1080
	shareH = 1920
)

// renderAnnualShareImage 把年度报告渲染成一张竖版 PNG 分享图。
// 复用封面模块的中文字体加载与 fogleman/gg 绘制能力，不引入新依赖。
// heroPoster 为可选的背景海报字节（取用户年度最常看的剧/片），用于满铺模糊底图。
func renderAnnualShareImage(rep *AnnualReport, userName string, heroPoster []byte, fontCNPath, fontENPath string) ([]byte, error) {
	if rep == nil {
		return nil, fmt.Errorf("年度报告为空")
	}
	if fontCNPath == "" {
		return nil, fmt.Errorf("未配置中文字体(emby.cover.font_cn)，无法生成分享图")
	}
	fontCN, err := cover.LoadSfntFont(fontCNPath)
	if err != nil {
		return nil, fmt.Errorf("加载中文字体失败: %w", err)
	}
	fontEN := fontCN
	if fontENPath != "" {
		if f, e := cover.LoadSfntFont(fontENPath); e == nil {
			fontEN = f
		}
	}

	dc := gg.NewContext(shareW, shareH)

	// ===== 背景：优先用海报满铺模糊 + 暗色渐变叠层，拿不到海报时回退纯色渐变 =====
	drewPoster := false
	if len(heroPoster) > 0 {
		if img, derr := imaging.Decode(bytes.NewReader(heroPoster)); derr == nil {
			bg := imaging.Fill(img, shareW, shareH, imaging.Center, imaging.Lanczos)
			bg = imaging.Blur(bg, 30)
			dc.DrawImage(bg, 0, 0)
			// 暗色渐变叠层：保证文字可读，同时保留海报氛围
			ov := gg.NewLinearGradient(0, 0, 0, shareH)
			ov.AddColorStop(0, color.RGBA{8, 10, 20, 205})
			ov.AddColorStop(0.45, color.RGBA{8, 10, 20, 170})
			ov.AddColorStop(1, color.RGBA{5, 7, 14, 238})
			dc.SetFillStyle(ov)
			dc.DrawRectangle(0, 0, shareW, shareH)
			dc.Fill()
			drewPoster = true
		}
	}
	if !drewPoster {
		grad := gg.NewLinearGradient(0, 0, 0, shareH)
		grad.AddColorStop(0, color.RGBA{0x1b, 0x22, 0x3b, 0xff})
		grad.AddColorStop(0.55, color.RGBA{0x12, 0x16, 0x29, 0xff})
		grad.AddColorStop(1, color.RGBA{0x0b, 0x0d, 0x18, 0xff})
		dc.SetFillStyle(grad)
		dc.DrawRectangle(0, 0, shareW, shareH)
		dc.Fill()
	}

	accent := color.RGBA{0x4d, 0x9b, 0xff, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	gray := color.RGBA{0x9a, 0xb0, 0xd6, 0xff}

	setFace := func(f *sfnt.Font, size float64) {
		if face, ferr := cover.NewFace(f, size); ferr == nil {
			dc.SetFontFace(face)
		}
	}
	cx := float64(shareW) / 2

	// ===== 顶部品牌 =====
	setFace(fontCN, 32)
	dc.SetColor(gray)
	dc.DrawStringAnchored("FilmFusion · 年度观看报告", cx, 156, 0.5, 0.5)

	// ===== 年份大字 =====
	setFace(fontEN, 168)
	dc.SetColor(white)
	dc.DrawStringAnchored(fmt.Sprintf("%d", rep.Year), cx, 336, 0.5, 0.5)

	// ===== 用户名 =====
	setFace(fontCN, 44)
	dc.SetColor(accent)
	dc.DrawStringAnchored("@"+userName, cx, 470, 0.5, 0.5)

	// ===== 统计卡片（2 列 x 3 行）=====
	hours := rep.TotalMinutes / 60
	type stat struct {
		value string
		label string
	}
	stats := []stat{
		{fmt.Sprintf("%d", hours), "观看时长 (小时)"},
		{fmt.Sprintf("%d", rep.ActiveDays), "活跃天数 (天)"},
		{fmt.Sprintf("%d", rep.MovieCount), "看过电影 (部)"},
		{fmt.Sprintf("%d", rep.EpisodeCount), "看过剧集 (集)"},
		{fmt.Sprintf("%d", rep.SeriesCount), "涉及剧目 (部)"},
		{fmt.Sprintf("%d", rep.LongestStreak), "最长连续 (天)"},
	}
	const (
		marginX  = 96.0
		gap      = 40.0
		cardH    = 158.0
		rowGap   = 44.0
		cardsTop = 596.0
	)
	cardW := (float64(shareW) - marginX*2 - gap) / 2
	for i, st := range stats {
		col := i % 2
		row := i / 2
		x := marginX + float64(col)*(cardW+gap)
		y := cardsTop + float64(row)*(cardH+rowGap)
		dc.SetRGBA(1, 1, 1, 0.10)
		dc.DrawRoundedRectangle(x, y, cardW, cardH, 26)
		dc.Fill()
		ccx := x + cardW/2
		setFace(fontEN, 76)
		dc.SetColor(white)
		dc.DrawStringAnchored(st.value, ccx, y+68, 0.5, 0.5)
		setFace(fontCN, 28)
		dc.SetColor(gray)
		dc.DrawStringAnchored(st.label, ccx, y+124, 0.5, 0.5)
	}

	// ===== Top 剧集 =====
	topTop := cardsTop + 2*(cardH+rowGap) + cardH + 80
	setFace(fontCN, 36)
	dc.SetColor(white)
	dc.DrawString("年度最爱剧集", marginX, topTop)

	rows := rep.TopSeries
	if len(rows) > 5 {
		rows = rows[:5]
	}
	listTop := topTop + 34
	rowH := 80.0
	panelH := rowH*float64(maxInt(len(rows), 1)) + 24
	dc.SetRGBA(1, 1, 1, 0.08)
	dc.DrawRoundedRectangle(marginX, listTop, float64(shareW)-marginX*2, panelH, 24)
	dc.Fill()
	if len(rows) == 0 {
		setFace(fontCN, 30)
		dc.SetColor(gray)
		dc.DrawStringAnchored("暂无剧集观看记录", cx, listTop+panelH/2, 0.5, 0.5)
	}
	for i, r := range rows {
		y := listTop + 18 + float64(i)*rowH + rowH/2 - 9
		// 排名徽标
		badgeColor := gray
		if i < 3 {
			badgeColor = accent
		}
		dc.SetColor(badgeColor)
		setFace(fontEN, 40)
		dc.DrawStringAnchored(fmt.Sprintf("%d", i+1), marginX+40, y, 0.5, 0.5)
		// 剧名（按可用宽度截断）
		name := firstNonEmpty(r.SeriesName, r.SeriesID)
		setFace(fontCN, 34)
		dc.SetColor(white)
		countStr := fmt.Sprintf("%d 集", r.EpisodeCount)
		setFace(fontCN, 30)
		cw, _ := dc.MeasureString(countStr)
		nameMaxW := float64(shareW) - marginX*2 - 90 - cw - 60
		setFace(fontCN, 34)
		name = truncateToWidth(dc, name, nameMaxW)
		dc.DrawStringAnchored(name, marginX+90, y, 0, 0.5)
		// 集数
		setFace(fontCN, 30)
		dc.SetColor(accent)
		dc.DrawStringAnchored(countStr, float64(shareW)-marginX-20, y, 1, 0.5)
	}

	// ===== 底部信息 =====
	setFace(fontCN, 28)
	dc.SetColor(gray)
	footer := fmt.Sprintf("活跃日均 %d 分钟 · 共 %d 天有观看", rep.AvgMinutesPerDay, rep.ActiveDays)
	dc.DrawStringAnchored(footer, cx, float64(shareH)-130, 0.5, 0.5)
	setFace(fontCN, 24)
	dc.SetRGBA(0.6, 0.66, 0.8, 0.7)
	dc.DrawStringAnchored("生成于 "+time.Now().Format("2006-01-02 15:04"), cx, float64(shareH)-90, 0.5, 0.5)

	buf := &bytes.Buffer{}
	if err := png.Encode(buf, dc.Image()); err != nil {
		return nil, fmt.Errorf("PNG 编码失败: %w", err)
	}
	return buf.Bytes(), nil
}

// truncateToWidth 按当前字体把字符串截断到不超过 maxWidth，超出加省略号。
func truncateToWidth(dc *gg.Context, s string, maxWidth float64) string {
	if maxWidth <= 0 {
		return s
	}
	if w, _ := dc.MeasureString(s); w <= maxWidth {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if w, _ := dc.MeasureString(candidate); w <= maxWidth {
			return candidate
		}
	}
	return string(runes)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
