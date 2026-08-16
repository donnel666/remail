package kitesim

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"image"
	// Register PNG decoding for image.Decode.
	_ "image/png"
	"math"
	"strings"
)

const captchaAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var captchaCenters = [...]int{20, 43, 66, 89}

//go:embed captcha_model.bin
var captchaModelBytes []byte

var embeddedCaptchaModel = loadCaptchaModel(captchaModelBytes)

type captchaModel struct {
	conv1Weight []float32
	conv1Bias   []float32
	conv2Weight []float32
	conv2Bias   []float32
	fcWeight    []float32
	fcBias      []float32
}

func solveCaptcha(png []byte) (string, error) {
	imageValue, _, err := image.Decode(strings.NewReader(string(png)))
	if err != nil {
		return "", errors.New("kitesim: invalid captcha image")
	}
	bounds := imageValue.Bounds()
	if bounds.Dx() < 105 || bounds.Dy() < 40 {
		return "", errors.New("kitesim: unsupported captcha size")
	}
	answer := make([]byte, len(captchaCenters))
	for index, center := range captchaCenters {
		input := make([]float32, 40*32)
		for y := range 40 {
			for x := range 32 {
				r, g, b, _ := imageValue.At(bounds.Min.X+center-16+x, bounds.Min.Y+y).RGBA()
				gray := float32((299*r + 587*g + 114*b) / 1000)
				input[y*32+x] = 1 - gray/65535
			}
		}
		answer[index] = captchaAlphabet[embeddedCaptchaModel.predict(input)]
	}
	return string(answer), nil
}

func loadCaptchaModel(raw []byte) *captchaModel {
	const floatCount = 8*5*5 + 8 + 16*8*3*3 + 16 + 36*16*10*8 + 36
	if len(raw) != floatCount*4 {
		panic("kitesim: invalid embedded captcha model")
	}
	values := make([]float32, floatCount)
	for i := range values {
		values[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	take := func(size int) []float32 {
		part := values[:size:size]
		values = values[size:]
		return part
	}
	return &captchaModel{
		conv1Weight: take(8 * 5 * 5),
		conv1Bias:   take(8),
		conv2Weight: take(16 * 8 * 3 * 3),
		conv2Bias:   take(16),
		fcWeight:    take(36 * 16 * 10 * 8),
		fcBias:      take(36),
	}
}

func (m *captchaModel) predict(input []float32) int {
	conv1 := convolution(input, 1, 40, 32, m.conv1Weight, m.conv1Bias, 8, 5, 2)
	pool1 := maxPool2(conv1, 8, 40, 32)
	conv2 := convolution(pool1, 8, 20, 16, m.conv2Weight, m.conv2Bias, 16, 3, 1)
	features := maxPool2(conv2, 16, 20, 16)
	bestIndex := 0
	bestScore := float32(-math.MaxFloat32)
	for output := range len(captchaAlphabet) {
		score := m.fcBias[output]
		weights := m.fcWeight[output*len(features) : (output+1)*len(features)]
		for i, value := range features {
			score += value * weights[i]
		}
		if score > bestScore {
			bestIndex, bestScore = output, score
		}
	}
	return bestIndex
}

func convolution(input []float32, inputChannels, height, width int, weights, bias []float32, outputChannels, kernel, padding int) []float32 {
	output := make([]float32, outputChannels*height*width)
	for outputChannel := range outputChannels {
		for y := range height {
			for x := range width {
				sum := bias[outputChannel]
				for inputChannel := range inputChannels {
					for kernelY := range kernel {
						inputY := y + kernelY - padding
						if inputY < 0 || inputY >= height {
							continue
						}
						for kernelX := range kernel {
							inputX := x + kernelX - padding
							if inputX < 0 || inputX >= width {
								continue
							}
							weightIndex := (((outputChannel*inputChannels+inputChannel)*kernel+kernelY)*kernel + kernelX)
							inputIndex := (inputChannel*height+inputY)*width + inputX
							sum += input[inputIndex] * weights[weightIndex]
						}
					}
				}
				if sum > 0 {
					output[(outputChannel*height+y)*width+x] = sum
				}
			}
		}
	}
	return output
}

func maxPool2(input []float32, channels, height, width int) []float32 {
	outputHeight, outputWidth := height/2, width/2
	output := make([]float32, channels*outputHeight*outputWidth)
	for channel := range channels {
		for y := range outputHeight {
			for x := range outputWidth {
				best := float32(0)
				for dy := range 2 {
					for dx := range 2 {
						value := input[(channel*height+y*2+dy)*width+x*2+dx]
						if value > best {
							best = value
						}
					}
				}
				output[(channel*outputHeight+y)*outputWidth+x] = best
			}
		}
	}
	return output
}
