package codecgate

import (
	"context"
	"errors"
	"io"
	"math"
	"sort"

	"github.com/berkantay/gomorph/internal/codec"
	"github.com/berkantay/gomorph/internal/detector"
	"github.com/berkantay/gomorph/internal/video"
)

type Config struct {
	CellSize        int
	Threshold       float64
	MinDuration     float64
	LumaTileSize    int
	LumaMaxShift    int
	LumaLocalRadius int
}

type Region struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Score  float64 `json:"score"`
}

type Observation struct {
	FrameIndex          int      `json:"frame_index"`
	Timestamp           float64  `json:"timestamp_seconds"`
	VectorCount         int      `json:"vector_count"`
	Keyframe            bool     `json:"keyframe"`
	RawScore            float64  `json:"raw_score"`
	CodecRawScore       float64  `json:"codec_raw_score"`
	LumaRawScore        float64  `json:"luma_raw_score"`
	LumaReady           bool     `json:"luma_ready"`
	Confidence          float64  `json:"confidence"`
	CameraMotionX       float64  `json:"camera_motion_x"`
	CameraMotionY       float64  `json:"camera_motion_y"`
	MotionAcceleration  float64  `json:"motion_acceleration"`
	SpatialDisagreement float64  `json:"spatial_disagreement"`
	Hotspots            []Region `json:"hotspots,omitempty"`
}

type Segment struct {
	StartSeconds   float64 `json:"start_seconds"`
	EndSeconds     float64 `json:"end_seconds"`
	PeakSeconds    float64 `json:"peak_seconds"`
	PeakConfidence float64 `json:"peak_confidence"`
}

type Report struct {
	Algorithm       string        `json:"algorithm"`
	CellSize        int           `json:"cell_size"`
	FramesAnalyzed  int           `json:"frames_analyzed"`
	DurationSeconds float64       `json:"duration_seconds"`
	RuntimeSeconds  float64       `json:"runtime_seconds"`
	PeakConfidence  float64       `json:"peak_confidence"`
	Segments        []Segment     `json:"segments"`
	Timeline        []Observation `json:"timeline"`
}

type vectorCell struct {
	x       float64
	y       float64
	weight  float64
	present bool
}

type field struct {
	width  int
	height int
	cells  []vectorCell
}

func Analyze(ctx context.Context, extractor codec.Extractor, config Config) (Report, error) {
	if config.CellSize < 8 || config.CellSize > 256 {
		return Report{}, errors.New("codec cell size must be between 8 and 256")
	}
	if config.Threshold <= 0 || config.Threshold >= 1 {
		return Report{}, errors.New("codec threshold must be between 0 and 1")
	}

	report := Report{Algorithm: "codec-motion-curvature-v1", CellSize: config.CellSize, Timeline: []Observation{}}
	var previous *field
	var lumaEngine *detector.Engine
	for {
		select {
		case <-ctx.Done():
			return Report{}, ctx.Err()
		default:
		}
		frame, err := extractor.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, err
		}
		current, cameraX, cameraY := buildField(frame, config.CellSize)
		observation := Observation{
			FrameIndex: frame.Index, Timestamp: frame.Timestamp, VectorCount: len(frame.Vectors),
			Keyframe: frame.Keyframe, CameraMotionX: cameraX, CameraMotionY: cameraY,
		}
		if previous != nil && compatible(*previous, current) && !frame.Keyframe {
			observation = compareFields(observation, *previous, current, config.CellSize, frame.Width, frame.Height)
		}
		observation.CodecRawScore = observation.RawScore
		if len(frame.Gray) > 0 {
			observation.RawScore = 0
			if lumaEngine == nil {
				lumaEngine, err = detector.New(detector.Config{
					TileSize: config.LumaTileSize, MaxShift: config.LumaMaxShift, LocalRadius: config.LumaLocalRadius,
				})
				if err != nil {
					return Report{}, err
				}
			}
			lumaObservation, ready, pushErr := lumaEngine.Push(video.Frame{
				Index: frame.Index, Timestamp: frame.Timestamp, Gray: frame.Gray,
				Width: frame.GrayWidth, Height: frame.GrayHeight,
			})
			if pushErr != nil {
				return Report{}, pushErr
			}
			if ready {
				observation.LumaReady = true
				observation.LumaRawScore = lumaObservation.RawScore
				observation.RawScore = lumaObservation.RawScore
				observation.Hotspots = scaleHotspots(lumaObservation.Hotspots, frame.GrayWidth, frame.GrayHeight, frame.Width, frame.Height)
			}
		}
		report.Timeline = append(report.Timeline, observation)
		previous = &current
		if frame.Timestamp > report.DurationSeconds {
			report.DurationSeconds = frame.Timestamp
		}
	}

	fps := estimateFPS(report.Timeline)
	if lumaEngine != nil {
		report.Algorithm = "codec-gated-luma-curvature-v1"
	}
	calibrate(report.Timeline, fps)
	report.Segments = findSegments(report.Timeline, config.Threshold, config.MinDuration, fps)
	report.FramesAnalyzed = len(report.Timeline)
	for _, observation := range report.Timeline {
		report.PeakConfidence = math.Max(report.PeakConfidence, observation.Confidence)
	}
	return report, nil
}

func scaleHotspots(hotspots []detector.Region, sourceWidth, sourceHeight, targetWidth, targetHeight int) []Region {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return nil
	}
	result := make([]Region, len(hotspots))
	for i, hotspot := range hotspots {
		result[i] = Region{
			X:      hotspot.X * targetWidth / sourceWidth,
			Y:      hotspot.Y * targetHeight / sourceHeight,
			Width:  max(1, hotspot.Width*targetWidth/sourceWidth),
			Height: max(1, hotspot.Height*targetHeight/sourceHeight),
			Score:  hotspot.Score,
		}
	}
	return result
}

func buildField(frame codec.Frame, cellSize int) (field, float64, float64) {
	width := (frame.Width + cellSize - 1) / cellSize
	height := (frame.Height + cellSize - 1) / cellSize
	result := field{width: width, height: height, cells: make([]vectorCell, width*height)}
	pastX := make([]float64, 0, len(frame.Vectors))
	pastY := make([]float64, 0, len(frame.Vectors))
	for _, vector := range frame.Vectors {
		if vector.Source > 0 {
			continue
		}
		pastX = append(pastX, vector.DX)
		pastY = append(pastY, vector.DY)
	}
	cameraX, cameraY := median(pastX), median(pastY)
	for _, vector := range frame.Vectors {
		if vector.Source > 0 || vector.X < 0 || vector.Y < 0 || vector.X >= frame.Width || vector.Y >= frame.Height {
			continue
		}
		index := min(vector.Y/cellSize, height-1)*width + min(vector.X/cellSize, width-1)
		weight := float64(max(1, vector.Width*vector.Height))
		result.cells[index].x += (vector.DX - cameraX) * weight
		result.cells[index].y += (vector.DY - cameraY) * weight
		result.cells[index].weight += weight
		result.cells[index].present = true
	}
	for i := range result.cells {
		if result.cells[i].weight > 0 {
			result.cells[i].x /= result.cells[i].weight
			result.cells[i].y /= result.cells[i].weight
		}
	}
	return result, cameraX, cameraY
}

func compareFields(observation Observation, previous, current field, cellSize, frameWidth, frameHeight int) Observation {
	cellAcceleration := make([]float64, len(current.cells))
	cellSpatial := make([]float64, len(current.cells))
	accelerationValues := make([]float64, 0, len(current.cells))
	spatialValues := make([]float64, 0, len(current.cells))
	for index, cell := range current.cells {
		if cell.present && previous.cells[index].present {
			prior := previous.cells[index]
			value := math.Hypot(cell.x-prior.x, cell.y-prior.y)
			cellAcceleration[index] = value
			accelerationValues = append(accelerationValues, value)
		}
		if !cell.present {
			continue
		}
		x, y := index%current.width, index/current.width
		for _, neighbor := range []int{index - 1, index + 1, index - current.width, index + current.width} {
			if neighbor < 0 || neighbor >= len(current.cells) {
				continue
			}
			nx, ny := neighbor%current.width, neighbor/current.width
			if absInt(nx-x)+absInt(ny-y) != 1 || !current.cells[neighbor].present {
				continue
			}
			other := current.cells[neighbor]
			cellSpatial[index] += math.Hypot(cell.x-other.x, cell.y-other.y)
		}
		cellSpatial[index] /= 4
		spatialValues = append(spatialValues, cellSpatial[index])
	}

	acceleration := upperMean(accelerationValues, 0.2)
	spatial := upperMean(spatialValues, 0.2)
	observation.MotionAcceleration = acceleration
	observation.SpatialDisagreement = spatial
	observation.RawScore = 0.75*acceleration + 0.25*spatial

	regions := make([]Region, 0, len(current.cells))
	for index, cell := range current.cells {
		if !cell.present {
			continue
		}
		x, y := (index%current.width)*cellSize, (index/current.width)*cellSize
		regions = append(regions, Region{
			X: x, Y: y, Width: min(cellSize, frameWidth-x), Height: min(cellSize, frameHeight-y),
			Score: 0.75*cellAcceleration[index] + 0.25*cellSpatial[index],
		})
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].Score > regions[j].Score })
	if len(regions) > 5 {
		regions = regions[:5]
	}
	observation.Hotspots = regions
	return observation
}

func compatible(previous, current field) bool {
	return previous.width == current.width && previous.height == current.height && len(previous.cells) == len(current.cells)
}

func calibrate(timeline []Observation, fps float64) {
	usesLuma := false
	for _, observation := range timeline {
		usesLuma = usesLuma || observation.LumaReady
	}
	isValid := func(observation Observation) bool {
		if usesLuma {
			return observation.LumaReady
		}
		return !observation.Keyframe && observation.VectorCount > 0
	}
	radius := max(1, int(math.Round(fps*0.2)))
	smoothed := make([]float64, len(timeline))
	for i := range timeline {
		start, end := max(0, i-radius), min(len(timeline)-1, i+radius)
		values := make([]float64, 0, end-start+1)
		for j := start; j <= end; j++ {
			if isValid(timeline[j]) {
				values = append(values, timeline[j].RawScore)
			}
		}
		smoothed[i] = mean(values)
	}
	valid := make([]float64, 0, len(timeline))
	for i, observation := range timeline {
		if isValid(observation) {
			valid = append(valid, smoothed[i])
		}
	}
	baseline := median(valid)
	deviations := make([]float64, len(valid))
	for i, value := range valid {
		deviations[i] = math.Abs(value - baseline)
	}
	scale := math.Max(0.05, 1.4826*median(deviations))
	logisticBaseline := 1 / (1 + math.Exp(0.5))
	for i := range timeline {
		if !isValid(timeline[i]) {
			continue
		}
		z := (smoothed[i] - baseline) / scale
		adaptive := 1 / (1 + math.Exp(-(z - 0.5)))
		timeline[i].Confidence = clamp01((adaptive - logisticBaseline) / (1 - logisticBaseline))
	}
}

func findSegments(timeline []Observation, threshold, minimumDuration, fps float64) []Segment {
	contextSeconds := 0.2
	maxGap := math.Max(2/math.Max(fps, 1), contextSeconds)
	segments := make([]Segment, 0)
	var active *Segment
	lastAbove := 0.0
	for _, item := range timeline {
		if item.Confidence < threshold || item.Keyframe {
			continue
		}
		if active == nil || item.Timestamp-lastAbove > maxGap {
			if active != nil && active.EndSeconds-active.StartSeconds >= minimumDuration {
				segments = append(segments, *active)
			}
			active = &Segment{StartSeconds: math.Max(0, item.Timestamp-contextSeconds), EndSeconds: item.Timestamp + contextSeconds, PeakSeconds: item.Timestamp, PeakConfidence: item.Confidence}
		} else {
			active.EndSeconds = item.Timestamp + contextSeconds
			if item.Confidence > active.PeakConfidence {
				active.PeakConfidence = item.Confidence
				active.PeakSeconds = item.Timestamp
			}
		}
		lastAbove = item.Timestamp
	}
	if active != nil && active.EndSeconds-active.StartSeconds >= minimumDuration {
		segments = append(segments, *active)
	}
	for i := range segments {
		peakRaw := -1.0
		for _, item := range timeline {
			if item.Timestamp >= segments[i].StartSeconds && item.Timestamp <= segments[i].EndSeconds && item.RawScore > peakRaw {
				peakRaw = item.RawScore
				segments[i].PeakSeconds = item.Timestamp
			}
		}
	}
	return segments
}

func estimateFPS(timeline []Observation) float64 {
	deltas := make([]float64, 0, len(timeline)-1)
	for i := 1; i < len(timeline); i++ {
		delta := timeline[i].Timestamp - timeline[i-1].Timestamp
		if delta > 0 {
			deltas = append(deltas, delta)
		}
	}
	delta := median(deltas)
	if delta <= 0 {
		return 30
	}
	return 1 / delta
}

func upperMean(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOfValues := append([]float64(nil), values...)
	sort.Float64s(copyOfValues)
	count := max(1, int(math.Ceil(float64(len(copyOfValues))*fraction)))
	return mean(copyOfValues[len(copyOfValues)-count:])
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOfValues := append([]float64(nil), values...)
	sort.Float64s(copyOfValues)
	middle := len(copyOfValues) / 2
	if len(copyOfValues)%2 == 0 {
		return (copyOfValues[middle-1] + copyOfValues[middle]) / 2
	}
	return copyOfValues[middle]
}

func clamp01(value float64) float64 { return math.Max(0, math.Min(1, value)) }

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
