package infra

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/rand"
)

// GenerateCaptchaImage creates a 120×40 PNG image with the given digits
// and returns it as a base64-encoded data URI.
// The image includes random lines for noise to prevent automated reading.
func GenerateCaptchaImage(digits string) (string, error) {
	width := 120
	height := 40

	// Create a white background
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Draw random noise lines
	for i := 0; i < 6; i++ {
		x1 := rand.Intn(width)
		y1 := rand.Intn(height)
		x2 := rand.Intn(width)
		y2 := rand.Intn(height)
		noiseColor := color.RGBA{
			R: uint8(rand.Intn(120)),
			G: uint8(rand.Intn(120)),
			B: uint8(rand.Intn(120)),
			A: 255,
		}
		drawLine(img, x1, y1, x2, y2, noiseColor)
	}

	// Draw each digit with varying positions and colors
	digitColors := []color.RGBA{
		{R: 20, G: 40, B: 180, A: 255},
		{R: 180, G: 30, B: 30, A: 255},
		{R: 20, G: 140, B: 50, A: 255},
		{R: 160, G: 80, B: 20, A: 255},
	}

	x := 10
	for i, ch := range digits {
		col := digitColors[i%len(digitColors)]
		yOffset := rand.Intn(8)
		drawDigit(img, ch, x, 8+yOffset, col)
		x += 25 + rand.Intn(5)
	}

	// Add some random dots
	for i := 0; i < 30; i++ {
		dotX := rand.Intn(width)
		dotY := rand.Intn(height)
		dotColor := color.RGBA{
			R: uint8(rand.Intn(100)),
			G: uint8(rand.Intn(100)),
			B: uint8(rand.Intn(100)),
			A: 255,
		}
		img.Set(dotX, dotY, dotColor)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("encode captcha png: %w", err)
	}

	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return dataURI, nil
}

// drawLine draws a line on the image using Bresenham's algorithm.
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
		if x1 >= 0 && x1 < img.Bounds().Max.X && y1 >= 0 && y1 < img.Bounds().Max.Y {
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

// drawDigit draws a simple bitmap representation of a digit.
// This is a basic approach that draws a 5×7 pixel grid for each digit.
func drawDigit(img *image.RGBA, digit rune, x, y int, col color.Color) {
	// 5×7 bitmap font for digits 0-9
	font := map[rune][][]int{
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
	}

	bitmap, ok := font[digit]
	if !ok {
		return
	}

	pixelSize := 4 // Each "pixel" in the bitmap is 4×4 real pixels
	for row := 0; row < 7; row++ {
		for colIdx := 0; colIdx < 5; colIdx++ {
			if bitmap[row][colIdx] == 1 {
				px := x + colIdx*pixelSize
				py := y + row*pixelSize
				// Draw a 4×4 block
				for dx := 0; dx < pixelSize; dx++ {
					for dy := 0; dy < pixelSize; dy++ {
						img.Set(px+dx, py+dy, col)
					}
				}
			}
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
