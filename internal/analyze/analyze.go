package analyze

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/berkantay/gomorph/internal/detector"
	jsonreport "github.com/berkantay/gomorph/internal/report"
	"github.com/berkantay/gomorph/internal/video"
)

type Options struct {
	InputPath    string
	AnalysisFPS  float64
	Width        int
	Threshold    float64
	MinDuration  float64
	StartSeconds float64
	EndSeconds   float64
	Detector     detector.Config
	OnProgress   func(frames int, timestamp float64)
}

type Segment struct {
	StartSeconds   float64 `json:"start_seconds"`
	EndSeconds     float64 `json:"end_seconds"`
	PeakSeconds    float64 `json:"peak_seconds"`
	PeakConfidence float64 `json:"peak_confidence"`
}

type Summary struct {
	FramesAnalyzed int     `json:"frames_analyzed"`
	PeakConfidence float64 `json:"peak_confidence"`
	PeakTimestamp  float64 `json:"peak_timestamp_seconds"`
	BaselineMedian float64 `json:"baseline_raw_median"`
	BaselineMAD    float64 `json:"baseline_raw_mad"`
}

type Report struct {
	SchemaVersion  int                    `json:"schema_version"`
	Algorithm      string                 `json:"algorithm"`
	CreatedAt      time.Time              `json:"created_at"`
	Video          video.Metadata         `json:"video"`
	AnalysisWidth  int                    `json:"analysis_width"`
	AnalysisHeight int                    `json:"analysis_height"`
	AnalysisFPS    float64                `json:"analysis_fps"`
	RuntimeSeconds float64                `json:"runtime_seconds"`
	Summary        Summary                `json:"summary"`
	Segments       []Segment              `json:"segments"`
	Timeline       []detector.Observation `json:"timeline"`
}

func Video(ctx context.Context, options Options, decoder video.Decoder) (Report, error) {
	metadata, err := decoder.Probe(ctx, options.InputPath)
	if err != nil {
		return Report{}, err
	}
	decodeFPS := options.AnalysisFPS
	if decodeFPS > 0 && metadata.FPS > 0 && decodeFPS > metadata.FPS {
		decodeFPS = metadata.FPS
	}
	fps := decodeFPS
	if fps == 0 {
		fps = metadata.FPS
	}
	duration := 0.0
	if options.EndSeconds > options.StartSeconds {
		duration = options.EndSeconds - options.StartSeconds
	}
	stream, err := decoder.Open(ctx, metadata, video.DecodeOptions{
		Width: options.Width, FPS: decodeFPS, StartSeconds: options.StartSeconds, DurationSeconds: duration,
	})
	if err != nil {
		return Report{}, err
	}
	defer stream.Close()

	engine, err := detector.New(options.Detector)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: 1, Algorithm: "stabilized-second-order-regional-motion-v1",
		CreatedAt: time.Now().UTC(), Video: metadata, AnalysisWidth: options.Width, AnalysisFPS: fps,
		Timeline: make([]detector.Observation, 0, int(metadata.DurationSeconds*fps)),
	}
	frames := 0
	for {
		frame, readErr := stream.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Report{}, readErr
		}
		if report.AnalysisHeight == 0 {
			report.AnalysisHeight = frame.Height
		}
		observation, ready, pushErr := engine.Push(frame)
		if pushErr != nil {
			return Report{}, fmt.Errorf("analyze frame %d: %w", frame.Index, pushErr)
		}
		if ready {
			report.Timeline = append(report.Timeline, observation)
		}
		frames++
		if options.OnProgress != nil {
			options.OnProgress(frames, frame.Timestamp)
		}
	}

	calibrate(report.Timeline, fps)
	report.Segments = segments(report.Timeline, options.Threshold, options.MinDuration, fps)
	report.Summary = summarize(report.Timeline)
	return report, nil
}

func WriteReport(path string, report Report) error {
	return jsonreport.WriteJSON(path, report)
}

func calibrate(timeline []detector.Observation, fps float64) {
	radius := max(1, int(math.Round(fps*0.2)))
	smoothed := rollingScores(timeline, radius)
	values := make([]float64, 0, len(smoothed))
	for i, value := range smoothed {
		if !timeline[i].SceneCut {
			values = append(values, value)
		}
	}
	median := percentile(values, 0.5)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - median)
	}
	mad := percentile(deviations, 0.5)
	scale := math.Max(0.02, 1.4826*mad)
	baseline := 1 / (1 + math.Exp(0.5))
	for i := range timeline {
		if timeline[i].SceneCut {
			timeline[i].Confidence = 0
			continue
		}
		z := (smoothed[i] - median) / scale
		adaptive := 1 / (1 + math.Exp(-(z - 0.5)))
		timeline[i].Confidence = clamp01((adaptive - baseline) / (1 - baseline))
	}
}

func rollingScores(timeline []detector.Observation, radius int) []float64 {
	result := make([]float64, len(timeline))
	for i := range timeline {
		start := max(0, i-radius)
		end := min(len(timeline)-1, i+radius)
		total := 0.0
		count := 0
		for j := start; j <= end; j++ {
			if !timeline[j].SceneCut {
				total += timeline[j].RawScore
				count++
			}
		}
		if count > 0 {
			result[i] = total / float64(count)
		}
	}
	return result
}

func segments(timeline []detector.Observation, threshold, minimumDuration, fps float64) []Segment {
	if len(timeline) == 0 {
		return []Segment{}
	}
	temporalContext := 0.2
	maximumGap := math.Max(2.0/math.Max(fps, 1), temporalContext)
	result := make([]Segment, 0)
	var active *Segment
	lastAbove := 0.0
	for _, item := range timeline {
		if item.Confidence >= threshold && !item.SceneCut {
			if active == nil || item.Timestamp-lastAbove > maximumGap {
				if active != nil {
					result = appendIfLongEnough(result, *active, minimumDuration)
				}
				active = &Segment{
					StartSeconds: math.Max(0, item.Timestamp-temporalContext),
					EndSeconds:   item.Timestamp + temporalContext,
					PeakSeconds:  item.Timestamp, PeakConfidence: item.Confidence,
				}
			} else {
				active.EndSeconds = item.Timestamp + temporalContext
				if item.Confidence > active.PeakConfidence {
					active.PeakConfidence = item.Confidence
				}
			}
			lastAbove = item.Timestamp
		}
	}
	if active != nil {
		result = appendIfLongEnough(result, *active, minimumDuration)
	}
	refineSegmentPeaks(result, timeline)
	return result
}

func refineSegmentPeaks(segments []Segment, timeline []detector.Observation) {
	for i := range segments {
		peakRawScore := -1.0
		for _, item := range timeline {
			if item.Timestamp < segments[i].StartSeconds || item.Timestamp > segments[i].EndSeconds || item.SceneCut {
				continue
			}
			if item.RawScore > peakRawScore {
				peakRawScore = item.RawScore
				segments[i].PeakSeconds = item.Timestamp
			}
		}
	}
}

func appendIfLongEnough(segments []Segment, segment Segment, minimumDuration float64) []Segment {
	if segment.EndSeconds-segment.StartSeconds >= minimumDuration {
		return append(segments, segment)
	}
	return segments
}

func summarize(timeline []detector.Observation) Summary {
	summary := Summary{FramesAnalyzed: len(timeline)}
	values := make([]float64, 0, len(timeline))
	for _, item := range timeline {
		if !item.SceneCut {
			values = append(values, item.RawScore)
		}
		if item.Confidence > summary.PeakConfidence {
			summary.PeakConfidence = item.Confidence
			summary.PeakTimestamp = item.Timestamp
		}
	}
	summary.BaselineMedian = percentile(values, 0.5)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - summary.BaselineMedian)
	}
	summary.BaselineMAD = percentile(deviations, 0.5)
	return summary
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyOfValues := append([]float64(nil), values...)
	sort.Float64s(copyOfValues)
	index := int(math.Round(quantile * float64(len(copyOfValues)-1)))
	return copyOfValues[index]
}

func clamp01(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
