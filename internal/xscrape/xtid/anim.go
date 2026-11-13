package xtid

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// calcAnimationKey 复刻 twscrape 的 load_keys + cacl_anim_key。
func calcAnimationKey(animationPaths []string, indices []int, verification []byte) (string, error) {
	if len(indices) == 0 {
		return "", fmt.Errorf("key byte 索引为空")
	}
	if len(verification) < 6 {
		return "", fmt.Errorf("verification key 只有 %d 字节，无法选择动画", len(verification))
	}
	for _, idx := range indices {
		if idx < 0 || idx >= len(verification) {
			return "", fmt.Errorf("key byte 索引 %d 超出 verification key 长度 %d", idx, len(verification))
		}
	}

	// 参考实现先按 key_bytes[5] 选 SVG，再按 indices[0] 选帧。
	selected := int(verification[5]) % len(animationPaths)
	rows, err := parseAnimationRows(animationPaths[selected])
	if err != nil {
		return "", err
	}
	rowIndex := int(verification[indices[0]]) % 16
	if rowIndex >= len(rows) {
		return "", fmt.Errorf("动画只有 %d 帧，索引 %d 越界", len(rows), rowIndex)
	}

	frameTime := 1
	for _, idx := range indices[1:] {
		frameTime *= int(verification[idx]) % 16
	}
	// 与 JS Math.round(frame_time / 10) * 10 一致（frame_time 非负）。
	frameTime = int(math.Floor(float64(frameTime)/10+0.5)) * 10

	return animationKeyFromRow(rows[rowIndex], float64(frameTime)/4096)
}

// animationKeyFromRow 对应参考实现的 cacl_anim_key(frames, target_time)。
func animationKeyFromRow(frames []float64, targetTime float64) (string, error) {
	if len(frames) < 7 {
		return "", fmt.Errorf("动画帧只有 %d 个数值，至少需要 7 个", len(frames))
	}
	fromColor := []float64{frames[0], frames[1], frames[2], 1}
	toColor := []float64{frames[3], frames[4], frames[5], 1}
	fromRotation := []float64{0}
	toRotation := []float64{solve(frames[6], 60, 360, true)}

	easingFrames := frames[7:]
	curves := make([]float64, len(easingFrames))
	for i, value := range easingFrames {
		minValue := 0.0
		if i%2 == 1 {
			minValue = -1.0
		}
		curves[i] = solve(value, minValue, 1, false)
	}
	if len(curves) != 4 {
		return "", fmt.Errorf("cubic-bezier 需要 4 个控制点，实际 %d 个", len(curves))
	}
	value := cubicCurve{curves: curves}.value(targetTime)

	color := interpolate(fromColor, toColor, value)
	for i := range color {
		color[i] = math.Max(0, math.Min(255, color[i]))
	}
	rotation := interpolate(fromRotation, toRotation, value)
	matrix := rotationMatrix(rotation[0])

	var b strings.Builder
	for _, v := range color[:len(color)-1] {
		b.WriteString(strconv.FormatInt(int64(math.RoundToEven(v)), 16))
	}
	for _, v := range matrix {
		rounded := roundTo2(v)
		if rounded < 0 {
			rounded = -rounded
		}
		hexValue := floatToHex(rounded)
		switch {
		case strings.HasPrefix(hexValue, "."):
			b.WriteString(strings.ToLower("0" + hexValue))
		case hexValue == "":
			b.WriteString("0")
		default:
			b.WriteString(hexValue)
		}
	}
	b.WriteString("00")

	out := strings.NewReplacer(".", "", "-", "").Replace(b.String())
	return out, nil
}

type cubicCurve struct {
	curves []float64
}

func (c cubicCurve) value(t float64) float64 {
	startGradient := 0.0
	endGradient := 0.0
	start := 0.0
	mid := 0.0
	end := 1.0

	if t <= 0 {
		if c.curves[0] > 0 {
			startGradient = c.curves[1] / c.curves[0]
		} else if c.curves[1] == 0 && c.curves[2] > 0 {
			startGradient = c.curves[3] / c.curves[2]
		}
		return startGradient * t
	}
	if t >= 1 {
		if c.curves[2] < 1 {
			endGradient = (c.curves[3] - 1) / (c.curves[2] - 1)
		} else if c.curves[2] == 1 && c.curves[0] < 1 {
			endGradient = (c.curves[1] - 1) / (c.curves[0] - 1)
		}
		return 1 + endGradient*(t-1)
	}
	for start < end {
		mid = (start + end) / 2
		xEst := cubicCalculate(c.curves[0], c.curves[2], mid)
		if math.Abs(t-xEst) < 0.00001 {
			return cubicCalculate(c.curves[1], c.curves[3], mid)
		}
		if xEst < t {
			start = mid
		} else {
			end = mid
		}
	}
	return cubicCalculate(c.curves[1], c.curves[3], mid)
}

func cubicCalculate(a, b, m float64) float64 {
	return 3.0*a*(1-m)*(1-m)*m + 3.0*b*(1-m)*m*m + m*m*m
}

func interpolate(from, to []float64, f float64) []float64 {
	out := make([]float64, len(from))
	for i := range from {
		out[i] = from[i]*(1-f) + to[i]*f
	}
	return out
}

func rotationMatrix(rotation float64) [4]float64 {
	degToRad := float64(math.Pi) / 180.0
	rad := rotation * degToRad
	return [4]float64{math.Cos(rad), -math.Sin(rad), math.Sin(rad), math.Cos(rad)}
}

// solve 对应参考实现的 solve(value, min_val, max_val, rounding)。
func solve(value, minValue, maxValue float64, rounding bool) float64 {
	result := value*(maxValue-minValue)/255 + minValue
	if rounding {
		return math.Floor(result)
	}
	return roundTo2(result)
}

// roundTo2 模拟 Python 的 round(x, 2)：对二进制浮点数的精确值做十进制
// 四舍六入五取偶。Go 的 math.Round 是五入远离零，直接使用会与 golden 不一致。
func roundTo2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	r := new(big.Rat).SetFloat64(v)
	if r == nil {
		return v
	}
	r.Mul(r, big.NewRat(100, 1))

	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))

	twiceRem := new(big.Int).Abs(rem)
	twiceRem.Lsh(twiceRem, 1)
	switch cmp := twiceRem.Cmp(den); {
	case cmp > 0:
		if r.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	case cmp == 0:
		// 恰好一半：向偶数舍入。
		if new(big.Int).Abs(q).Bit(0) == 1 {
			if r.Sign() >= 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}

	out, _ := new(big.Rat).SetFrac(q, big.NewInt(100)).Float64()
	return out
}

// floatToHex 逐行复刻参考实现的 float_to_hex（包括大写 A-F 的行为）。
func floatToHex(x float64) string {
	var result []byte
	quotient := int64(x)
	fraction := x - float64(quotient)

	for quotient > 0 {
		quotient = int64(x / 16)
		remainder := int64(x - float64(quotient)*16)
		if remainder > 9 {
			result = append([]byte{byte(remainder + 55)}, result...)
		} else {
			result = append([]byte{byte('0' + remainder)}, result...)
		}
		x = float64(quotient)
	}

	if fraction == 0 {
		return string(result)
	}

	result = append(result, '.')
	for fraction > 0 {
		fraction *= 16
		integer := int64(fraction)
		fraction -= float64(integer)
		if integer > 9 {
			result = append(result, byte(integer+55))
		} else {
			result = append(result, byte('0'+integer))
		}
	}
	return string(result)
}
