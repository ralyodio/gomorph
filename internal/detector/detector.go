package detector

import (
	"errors"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/berkantay/gomorph/internal/video"
)

type Config struct {
	TileSize    int
	MaxShift    int
	LocalRadius int
}

type Motion struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Region struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Score  float64 `json:"score"`
}

type Observation struct {
	FrameIndex         int      `json:"frame_index"`
	Timestamp          float64  `json:"timestamp_seconds"`
	RawScore           float64  `json:"raw_score"`
	Confidence         float64  `json:"confidence"`
	SceneCut           bool     `json:"scene_cut"`
	CameraMotion       Motion   `json:"camera_motion"`
	PhotometricChange  float64  `json:"photometric_change"`
	MotionAcceleration float64  `json:"motion_acceleration"`
	Hotspots           []Region `json:"hotspots,omitempty"`
}

type Engine struct {
	config       Config
	older        *video.Frame
	previous     *video.Frame
	olderMean    float64
	previousMean float64
	priorGlobal  Motion
	priorRegions []Motion
}

type regionMetric struct {
	motion             Motion
	region             Region
	photo              float64
	motionAcceleration float64
}

func New(config Config) (*Engine, error) {
	if config.TileSize < 8 {
		return nil, errors.New("tile size must be at least 8 pixels")
	}
	if config.MaxShift < 0 || config.MaxShift > 64 {
		return nil, errors.New("max shift must be between 0 and 64 pixels")
	}
	if config.LocalRadius < 0 || config.LocalRadius > 12 {
		return nil, errors.New("local radius must be between 0 and 12 pixels")
	}
	return &Engine{config: config}, nil
}

// Push consumes one grayscale frame. Two prior frames are required to measure
// second-order change, so ready is false for the first two calls.
func (e *Engine) Push(frame video.Frame) (observation Observation, ready bool, err error) {
	if frame.Width <= 0 || frame.Height <= 0 || len(frame.Gray) != frame.Width*frame.Height {
		return Observation{}, false, errors.New("invalid grayscale frame")
	}
	if e.previous != nil && (frame.Width != e.previous.Width || frame.Height != e.previous.Height) {
		return Observation{}, false, errors.New("frame dimensions changed")
	}
	currentMean := mean(frame.Gray)
	if e.older == nil || e.previous == nil {
		if e.previous != nil {
			e.initializeMotion(*e.previous, frame)
		}
		e.advance(frame, currentMean)
		return Observation{}, false, nil
	}

	global := estimateShift(e.previous.Gray, frame.Gray, frame.Width, frame.Height, e.config.MaxShift, 6)
	cutChange := alignedDifference(e.previous.Gray, frame.Gray, frame.Width, frame.Height, global, 4)
	isCut := cutChange > 0.24
	if isCut {
		observation = Observation{
			FrameIndex: frame.Index, Timestamp: frame.Timestamp, SceneCut: true,
			CameraMotion: global, PhotometricChange: cutChange,
		}
		e.priorRegions = nil
		e.priorGlobal = Motion{}
		e.advance(frame, currentMean)
		return observation, true, nil
	}

	tilesX := (frame.Width + e.config.TileSize - 1) / e.config.TileSize
	tilesY := (frame.Height + e.config.TileSize - 1) / e.config.TileSize
	regionCount := tilesX * tilesY
	metrics := make([]regionMetric, regionCount)
	photoTotal := 0.0
	motionTotal := 0.0
	illuminationVelocity := currentMean - e.previousMean
	illuminationAcceleration := currentMean - 2*e.previousMean + e.olderMean
	olderShift := Motion{X: global.X + e.priorGlobal.X, Y: global.Y + e.priorGlobal.Y}

	processRegions(regionCount, func(start, end int) {
		for regionIndex := start; regionIndex < end; regionIndex++ {
			x := (regionIndex % tilesX) * e.config.TileSize
			y := (regionIndex / tilesX) * e.config.TileSize
			width := min(e.config.TileSize, frame.Width-x)
			height := min(e.config.TileSize, frame.Height-y)
			motion := estimateRegionShift(e.previous.Gray, frame.Gray, frame.Width, frame.Height,
				x, y, width, height, global, e.config.LocalRadius)

			prior := e.priorGlobal
			if regionIndex < len(e.priorRegions) {
				prior = e.priorRegions[regionIndex]
			}
			motionAcceleration := math.Hypot(float64(motion.X-prior.X), float64(motion.Y-prior.Y))
			photo, changed, reconstruction := regionChanges(
				e.older.Gray, e.previous.Gray, frame.Gray, frame.Width, frame.Height,
				x, y, width, height, global, olderShift, motion,
				illuminationVelocity, illuminationAcceleration,
			)
			motionScore := clamp01(motionAcceleration / float64(max(2, e.config.LocalRadius*2+2)))
			score := 0.45*clamp01(photo/48) + 0.20*changed + 0.20*motionScore + 0.15*clamp01(reconstruction/48)
			metrics[regionIndex] = regionMetric{
				motion: motion, region: Region{X: x, Y: y, Width: width, Height: height, Score: score},
				photo: photo, motionAcceleration: motionAcceleration,
			}
		}
	})

	regionMotions := make([]Motion, regionCount)
	regions := make([]Region, regionCount)
	for i, metric := range metrics {
		regionMotions[i] = metric.motion
		regions[i] = metric.region
		photoTotal += metric.photo
		motionTotal += metric.motionAcceleration
	}

	topCount := max(1, len(regions)/5)
	selectTopRegions(regions, topCount)
	rawScore := 0.0
	for i := 0; i < topCount; i++ {
		rawScore += regions[i].Score
	}
	rawScore /= float64(topCount)
	if len(regions) > 5 {
		regions = append([]Region(nil), regions[:5]...)
	}

	observation = Observation{
		FrameIndex: frame.Index, Timestamp: frame.Timestamp, RawScore: clamp01(rawScore),
		CameraMotion: global, PhotometricChange: photoTotal / float64(regionCount*255),
		MotionAcceleration: motionTotal / float64(regionCount), Hotspots: regions,
	}
	e.priorRegions = regionMotions
	e.priorGlobal = global
	e.advance(frame, currentMean)
	return observation, true, nil
}

func selectTopRegions(regions []Region, count int) {
	if count <= 0 || len(regions) < 2 {
		return
	}
	count = min(count, len(regions))
	left, right := 0, len(regions)-1
	target := count - 1
	for left < right {
		pivot := regions[(left+right)/2].Score
		i, j := left, right
		for i <= j {
			for regions[i].Score > pivot {
				i++
			}
			for regions[j].Score < pivot {
				j--
			}
			if i <= j {
				regions[i], regions[j] = regions[j], regions[i]
				i++
				j--
			}
		}
		if target <= j {
			right = j
		} else if target >= i {
			left = i
		} else {
			break
		}
	}
	sort.Slice(regions[:count], func(i, j int) bool { return regions[i].Score > regions[j].Score })
}

func processRegions(count int, process func(start, end int)) {
	const minimumPerWorker = 64
	workers := min(runtime.GOMAXPROCS(0), (count+minimumPerWorker-1)/minimumPerWorker)
	if workers <= 1 {
		process(0, count)
		return
	}

	chunkSize := (count + workers - 1) / workers
	var wait sync.WaitGroup
	for start := 0; start < count; start += chunkSize {
		end := min(count, start+chunkSize)
		wait.Add(1)
		go func() {
			defer wait.Done()
			process(start, end)
		}()
	}
	wait.Wait()
}

func (e *Engine) initializeMotion(previous, current video.Frame) {
	e.priorGlobal = estimateShift(previous.Gray, current.Gray, current.Width, current.Height, e.config.MaxShift, 6)
	tilesX := (current.Width + e.config.TileSize - 1) / e.config.TileSize
	tilesY := (current.Height + e.config.TileSize - 1) / e.config.TileSize
	e.priorRegions = make([]Motion, 0, tilesX*tilesY)
	for y := 0; y < current.Height; y += e.config.TileSize {
		for x := 0; x < current.Width; x += e.config.TileSize {
			e.priorRegions = append(e.priorRegions, estimateRegionShift(
				previous.Gray, current.Gray, current.Width, current.Height,
				x, y, min(e.config.TileSize, current.Width-x), min(e.config.TileSize, current.Height-y),
				e.priorGlobal, e.config.LocalRadius,
			))
		}
	}
}

func (e *Engine) advance(frame video.Frame, frameMean float64) {
	if e.previous != nil {
		copyOfPrevious := *e.previous
		e.older = &copyOfPrevious
		e.olderMean = e.previousMean
	}
	copyOfFrame := frame
	e.previous = &copyOfFrame
	e.previousMean = frameMean
}

func estimateShift(reference, current []byte, width, height, radius, stride int) Motion {
	if radius == 0 {
		return Motion{}
	}
	if radius < 8 {
		best := searchMotion(reference, current, width, height, 0, 0, width, height, Motion{}, radius, 2, stride)
		refined := searchMotion(reference, current, width, height, 0, 0, width, height, best, 1, 1, max(3, stride-2))
		return Motion{X: clampInt(refined.X, -radius, radius), Y: clampInt(refined.Y, -radius, radius)}
	}
	coarse := searchMotion(reference, current, width, height, 0, 0, width, height, Motion{}, radius, 4, max(8, stride))
	medium := searchMotion(reference, current, width, height, 0, 0, width, height, coarse, 2, 2, max(6, stride))
	refined := searchMotion(reference, current, width, height, 0, 0, width, height, medium, 1, 1, max(6, stride))
	return Motion{X: clampInt(refined.X, -radius, radius), Y: clampInt(refined.Y, -radius, radius)}
}

func estimateRegionShift(reference, current []byte, width, height, x, y, regionWidth, regionHeight int, center Motion, radius int) Motion {
	if radius == 0 {
		return center
	}
	best := searchMotion(reference, current, width, height, x, y, regionWidth, regionHeight, center, radius, 2, 6)
	refined := searchMotion(reference, current, width, height, x, y, regionWidth, regionHeight, best, 1, 1, 3)
	return Motion{
		X: clampInt(refined.X, center.X-radius, center.X+radius),
		Y: clampInt(refined.Y, center.Y-radius, center.Y+radius),
	}
}

func searchMotion(reference, current []byte, width, height, x, y, regionWidth, regionHeight int, center Motion, radius, step, stride int) Motion {
	best := center
	bestCost := math.MaxFloat64
	for dy := center.Y - radius; dy <= center.Y+radius; dy += step {
		for dx := center.X - radius; dx <= center.X+radius; dx += step {
			candidate := Motion{X: dx, Y: dy}
			cost, samples := shiftCost(reference, current, width, height, x, y, regionWidth, regionHeight, candidate, stride)
			if samples > 0 && betterMotion(cost, candidate, bestCost, best, center) {
				bestCost = cost
				best = candidate
			}
		}
	}
	return best
}

func betterMotion(cost float64, candidate Motion, bestCost float64, best, center Motion) bool {
	const tolerance = 1e-9
	if cost < bestCost-tolerance {
		return true
	}
	if math.Abs(cost-bestCost) > tolerance {
		return false
	}
	candidateDistance := absInt(candidate.X-center.X) + absInt(candidate.Y-center.Y)
	bestDistance := absInt(best.X-center.X) + absInt(best.Y-center.Y)
	return candidateDistance < bestDistance
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func clampInt(value, minimum, maximum int) int {
	return max(minimum, min(maximum, value))
}

func shiftCost(reference, current []byte, width, height, x, y, regionWidth, regionHeight int, shift Motion, stride int) (float64, int) {
	total := 0.0
	samples := 0
	for cy := y; cy < y+regionHeight && cy < height; cy += stride {
		ry := cy + shift.Y
		if ry < 0 || ry >= height {
			continue
		}
		for cx := x; cx < x+regionWidth && cx < width; cx += stride {
			rx := cx + shift.X
			if rx < 0 || rx >= width {
				continue
			}
			total += math.Abs(float64(current[cy*width+cx]) - float64(reference[ry*width+rx]))
			samples++
		}
	}
	if samples == 0 {
		return math.MaxFloat64, 0
	}
	return total / float64(samples), samples
}

func regionChanges(older, previous, current []byte, width, height, x, y, regionWidth, regionHeight int, previousShift, olderShift, motion Motion, illuminationVelocity, illuminationAcceleration float64) (float64, float64, float64) {
	photoTotal := 0.0
	reconstructionTotal := 0.0
	changed := 0
	photoSamples := 0
	reconstructionSamples := 0
	for cy := y; cy < y+regionHeight && cy < height; cy += 2 {
		py, oy := cy+previousShift.Y, cy+olderShift.Y
		ry := cy + motion.Y
		for cx := x; cx < x+regionWidth && cx < width; cx += 2 {
			px, ox := cx+previousShift.X, cx+olderShift.X
			currentValue := float64(current[cy*width+cx])
			if py >= 0 && py < height && oy >= 0 && oy < height && px >= 0 && px < width && ox >= 0 && ox < width {
				acceleration := currentValue - 2*float64(previous[py*width+px]) + float64(older[oy*width+ox])
				value := math.Abs(acceleration - illuminationAcceleration)
				photoTotal += value
				if value > 20 {
					changed++
				}
				photoSamples++
			}

			rx := cx + motion.X
			if ry >= 0 && ry < height && rx >= 0 && rx < width {
				difference := currentValue - float64(previous[ry*width+rx]) - illuminationVelocity
				reconstructionTotal += math.Abs(difference)
				reconstructionSamples++
			}
		}
	}
	if photoSamples == 0 || reconstructionSamples == 0 {
		return 0, 0, 0
	}
	return photoTotal / float64(photoSamples), float64(changed) / float64(photoSamples), reconstructionTotal / float64(reconstructionSamples)
}

func alignedDifference(reference, current []byte, width, height int, shift Motion, stride int) float64 {
	cost, samples := shiftCost(reference, current, width, height, 0, 0, width, height, shift, stride)
	if samples == 0 {
		return 0
	}
	return cost / 255
}

func mean(values []byte) float64 {
	if len(values) == 0 {
		return 0
	}
	total := uint64(0)
	for _, value := range values {
		total += uint64(value)
	}
	return float64(total) / float64(len(values))
}

func clamp01(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
