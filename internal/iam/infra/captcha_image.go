package infra

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"math/rand"
)

const (
	captchaImageWidth  = 160
	captchaImageHeight = 60
)

// GenerateCaptchaImage creates a PNG data URI for a simple arithmetic captcha.
// The rendering keeps the expression readable for humans while adding enough
// noise, character jitter, and ripple distortion to defeat simple OCR templates.
func GenerateCaptchaImage(text string) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, captchaImageWidth, captchaImageHeight))
	drawGradientBackground(img)
	drawBackgroundNoise(img)
	drawCurves(img, 8, 45, 135)
	drawExpression(img, []rune(text))
	drawCurves(img, 4, 20, 95)
	drawForegroundNoise(img)

	var buf bytes.Buffer
	if err := png.Encode(&buf, ripple(img)); err != nil {
		return "", fmt.Errorf("encode captcha png: %w", err)
	}

	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func drawGradientBackground(img *image.RGBA) {
	for y := 0; y < captchaImageHeight; y++ {
		for x := 0; x < captchaImageWidth; x++ {
			ratio := float64(x+y) / float64(captchaImageWidth+captchaImageHeight)
			img.Set(x, y, color.RGBA{
				R: uint8(235 - ratio*28),
				G: uint8(244 - ratio*34),
				B: uint8(250 - ratio*18),
				A: 255,
			})
		}
	}
}

func drawBackgroundNoise(img *image.RGBA) {
	for i := 0; i < 360; i++ {
		x := rand.Intn(captchaImageWidth)
		y := rand.Intn(captchaImageHeight)
		size := 1 + rand.Intn(3)
		col := randomRGBA(120, 230, uint8(28+rand.Intn(48)))
		fillRect(img, x, y, size, size, col)
	}

	for i := 0; i < 10; i++ {
		x := rand.Intn(captchaImageWidth)
		y := rand.Intn(captchaImageHeight)
		w := 18 + rand.Intn(58)
		h := 8 + rand.Intn(32)
		drawEllipse(img, x, y, w, h, randomRGBA(120, 210, uint8(38+rand.Intn(50))))
	}
}

func drawExpression(img *image.RGBA, chars []rune) {
	if len(chars) == 0 {
		return
	}

	step := (captchaImageWidth - 24) / len(chars)
	baseline := 17 + rand.Intn(8)
	for i, ch := range chars {
		glyph, ok := captchaGlyphs[ch]
		if !ok {
			continue
		}

		scale := 3 + rand.Intn(2)
		x := 9 + i*step + rand.Intn(5)
		y := baseline + rand.Intn(11) - 5
		if ch == '=' || ch == '?' {
			y += 2
		}
		col := randomRGBA(12, 88, 238)
		shadow := randomRGBA(90, 150, 85)
		drawGlyph(img, glyph, x+1, y+1, scale, shadow, true)
		drawGlyph(img, glyph, x, y, scale, col, rand.Intn(2) == 0)
	}
}

func drawGlyph(img *image.RGBA, glyph [][]int, x, y, scale int, col color.RGBA, thicken bool) {
	for row := range glyph {
		for column := range glyph[row] {
			if glyph[row][column] == 0 {
				continue
			}
			px := x + column*scale + rand.Intn(2)
			py := y + row*scale + rand.Intn(2)
			fillRect(img, px, py, scale, scale, col)
			if thicken {
				fillRect(img, px+1, py, scale, scale, col)
			}
		}
	}
}

func drawCurves(img *image.RGBA, count, low, high int) {
	for i := 0; i < count; i++ {
		col := randomRGBA(low, high, uint8(60+rand.Intn(70)))
		p0 := image.Point{X: -8, Y: rand.Intn(captchaImageHeight)}
		p1 := image.Point{X: rand.Intn(captchaImageWidth / 2), Y: rand.Intn(captchaImageHeight)}
		p2 := image.Point{X: captchaImageWidth/2 + rand.Intn(captchaImageWidth/2), Y: rand.Intn(captchaImageHeight)}
		p3 := image.Point{X: captchaImageWidth + 8, Y: rand.Intn(captchaImageHeight)}
		previous := p0
		for step := 1; step <= 80; step++ {
			t := float64(step) / 80
			next := cubicPoint(p0, p1, p2, p3, t)
			drawLine(img, previous.X, previous.Y, next.X, next.Y, col)
			if rand.Intn(3) == 0 {
				drawLine(img, previous.X, previous.Y+1, next.X, next.Y+1, col)
			}
			previous = next
		}
	}
}

func drawForegroundNoise(img *image.RGBA) {
	for i := 0; i < 52; i++ {
		x := rand.Intn(captchaImageWidth)
		y := rand.Intn(captchaImageHeight)
		drawLine(
			img,
			x,
			y,
			clamp(x+rand.Intn(15)-7, 0, captchaImageWidth-1),
			clamp(y+rand.Intn(11)-5, 0, captchaImageHeight-1),
			randomRGBA(20, 140, uint8(45+rand.Intn(70))),
		)
	}
}

func ripple(source *image.RGBA) *image.RGBA {
	target := image.NewRGBA(source.Bounds())
	phaseX := rand.Float64() * math.Pi * 2
	phaseY := rand.Float64() * math.Pi * 2
	for y := 0; y < captchaImageHeight; y++ {
		for x := 0; x < captchaImageWidth; x++ {
			sourceX := clamp(x+int(math.Round(math.Sin(float64(y)/7.0+phaseX)*2.2)), 0, captchaImageWidth-1)
			sourceY := clamp(y+int(math.Round(math.Sin(float64(x)/14.0+phaseY)*1.4)), 0, captchaImageHeight-1)
			target.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	return target
}

func cubicPoint(p0, p1, p2, p3 image.Point, t float64) image.Point {
	mt := 1 - t
	x := mt*mt*mt*float64(p0.X) + 3*mt*mt*t*float64(p1.X) + 3*mt*t*t*float64(p2.X) + t*t*t*float64(p3.X)
	y := mt*mt*mt*float64(p0.Y) + 3*mt*mt*t*float64(p1.Y) + 3*mt*t*t*float64(p2.Y) + t*t*t*float64(p3.Y)
	return image.Point{X: int(math.Round(x)), Y: int(math.Round(y))}
}

func drawLine(img *image.RGBA, x1, y1, x2, y2 int, col color.Color) {
	dx := abs(x2 - x1)
	dy := -abs(y2 - y1)
	sx := 1
	if x1 > x2 {
		sx = -1
	}
	sy := 1
	if y1 > y2 {
		sy = -1
	}
	err := dx + dy

	for {
		if inBounds(x1, y1) {
			img.Set(x1, y1, col)
		}
		if x1 == x2 && y1 == y2 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x1 += sx
		}
		if e2 <= dx {
			err += dx
			y1 += sy
		}
	}
}

func drawEllipse(img *image.RGBA, x, y, width, height int, col color.Color) {
	if width <= 0 || height <= 0 {
		return
	}
	rx := float64(width) / 2
	ry := float64(height) / 2
	cx := float64(x) + rx
	cy := float64(y) + ry
	for degree := 0; degree < 360; degree += 3 {
		radians := float64(degree) * math.Pi / 180
		px := int(math.Round(cx + math.Cos(radians)*rx))
		py := int(math.Round(cy + math.Sin(radians)*ry))
		if inBounds(px, py) {
			img.Set(px, py, col)
		}
	}
}

func fillRect(img *image.RGBA, x, y, width, height int, col color.Color) {
	draw.Draw(img, image.Rect(x, y, x+width, y+height), &image.Uniform{col}, image.Point{}, draw.Src)
}

func randomRGBA(low, high int, alpha uint8) color.RGBA {
	if high <= low {
		high = low + 1
	}
	return color.RGBA{
		R: uint8(low + rand.Intn(high-low)),
		G: uint8(low + rand.Intn(high-low)),
		B: uint8(low + rand.Intn(high-low)),
		A: alpha,
	}
}

func inBounds(x, y int) bool {
	return x >= 0 && x < captchaImageWidth && y >= 0 && y < captchaImageHeight
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var captchaGlyphs = map[rune][][]int{
	'0': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 1, 1},
		{1, 0, 1, 0, 1},
		{1, 1, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	'1': {
		{0, 0, 1, 0, 0},
		{0, 1, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 1, 1, 0},
	},
	'2': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 0, 0, 0},
		{1, 1, 1, 1, 1},
	},
	'3': {
		{1, 1, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{1, 1, 1, 1, 0},
	},
	'4': {
		{0, 0, 0, 1, 0},
		{0, 0, 1, 1, 0},
		{0, 1, 0, 1, 0},
		{1, 0, 0, 1, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 0, 1, 0},
	},
	'5': {
		{1, 1, 1, 1, 1},
		{1, 0, 0, 0, 0},
		{1, 1, 1, 1, 0},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{1, 1, 1, 1, 0},
	},
	'6': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 0},
		{1, 0, 0, 0, 0},
		{1, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	'7': {
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 1, 0, 0, 0},
	},
	'8': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	'9': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{1, 0, 0, 0, 1},
		{0, 1, 1, 1, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 1, 1, 1, 0},
	},
	'+': {
		{0, 0, 0, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 0, 0, 0},
	},
	'−': {
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
	},
	'×': {
		{1, 0, 0, 0, 1},
		{0, 1, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 1, 0, 1, 0},
		{1, 0, 0, 0, 1},
	},
	'÷': {
		{0, 0, 1, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 1, 0, 0},
	},
	'=': {
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 0},
		{1, 1, 1, 1, 1},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0},
	},
	'?': {
		{0, 1, 1, 1, 0},
		{1, 0, 0, 0, 1},
		{0, 0, 0, 0, 1},
		{0, 0, 0, 1, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 1, 0, 0},
	},
}
