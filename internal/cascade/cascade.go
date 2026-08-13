package cascade

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/berkantay/gomorph/internal/analyze"
	"github.com/berkantay/gomorph/internal/codec"
	"github.com/berkantay/gomorph/internal/codecgate"
	"github.com/berkantay/gomorph/internal/detector"
	"github.com/berkantay/gomorph/internal/video"
)

type Options struct {
	InputPath         string
	GateCellSize      int
	GateThreshold     float64
	GateMinDuration   float64
	GateTileSize      int
	GateMaxShift      int
	GateLocalRadius   int
	DetailWidth       int
	DetailFPS         float64
	DetailTileSize    int
	DetailMaxShift    int
	DetailLocalRadius int
}

type Refinement struct {
	StartSeconds   float64           `json:"start_seconds"`
	EndSeconds     float64           `json:"end_seconds"`
	PeakSeconds    float64           `json:"peak_seconds"`
	PeakConfidence float64           `json:"peak_confidence"`
	RawScore       float64           `json:"raw_score"`
	AnalysisWidth  int               `json:"analysis_width"`
	AnalysisHeight int               `json:"analysis_height"`
	Hotspots       []detector.Region `json:"hotspots"`
}

type Report struct {
	SchemaVersion  int              `json:"schema_version"`
	Algorithm      string           `json:"algorithm"`
	RuntimeSeconds float64          `json:"runtime_seconds"`
	Video          video.Metadata   `json:"video"`
	Gate           codecgate.Report `json:"gate"`
	Refinements    []Refinement     `json:"refinements"`
}

func Analyze(ctx context.Context, extractor codec.Extractor, decoder video.Decoder, options Options) (Report, error) {
	started := time.Now()
	metadata, err := decoder.Probe(ctx, options.InputPath)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: 1, Algorithm: "codec-gated-luma-curvature-cascade-v1",
		Video: metadata, Refinements: []Refinement{},
	}

	gateStarted := time.Now()
	report.Gate, err = codecgate.Analyze(ctx, extractor, codecgate.Config{
		CellSize: options.GateCellSize, Threshold: options.GateThreshold, MinDuration: options.GateMinDuration,
		LumaTileSize: options.GateTileSize, LumaMaxShift: options.GateMaxShift, LumaLocalRadius: options.GateLocalRadius,
	})
	if err != nil {
		return Report{}, err
	}
	report.Gate.RuntimeSeconds = time.Since(gateStarted).Seconds()

	cached := cachedDecoder{Decoder: decoder, metadata: metadata}
	for _, segment := range report.Gate.Segments {
		start := math.Max(0, segment.StartSeconds-0.1)
		end := math.Min(metadata.DurationSeconds, segment.EndSeconds+0.1)
		detail, detailErr := analyze.Video(ctx, analyze.Options{
			InputPath: options.InputPath, AnalysisFPS: options.DetailFPS, Width: options.DetailWidth,
			Threshold: 0.6, MinDuration: options.GateMinDuration,
			StartSeconds: start, EndSeconds: end,
			Detector: detector.Config{
				TileSize: options.DetailTileSize, MaxShift: options.DetailMaxShift, LocalRadius: options.DetailLocalRadius,
			},
		}, cached)
		if detailErr != nil {
			return Report{}, detailErr
		}
		peak, peakErr := strongest(detail.Timeline, segment.StartSeconds, segment.EndSeconds)
		if peakErr != nil {
			continue
		}
		report.Refinements = append(report.Refinements, Refinement{
			StartSeconds: segment.StartSeconds, EndSeconds: segment.EndSeconds,
			PeakSeconds: peak.Timestamp, PeakConfidence: segment.PeakConfidence, RawScore: peak.RawScore,
			AnalysisWidth: detail.AnalysisWidth, AnalysisHeight: detail.AnalysisHeight,
			Hotspots: peak.Hotspots,
		})
	}
	report.RuntimeSeconds = time.Since(started).Seconds()
	return report, nil
}

func strongest(timeline []detector.Observation, start, end float64) (detector.Observation, error) {
	var peak detector.Observation
	found := false
	for _, observation := range timeline {
		if observation.Timestamp < start || observation.Timestamp > end || observation.SceneCut {
			continue
		}
		if !found || observation.RawScore > peak.RawScore {
			peak = observation
			found = true
		}
	}
	if !found {
		return detector.Observation{}, errors.New("candidate window contained no analyzable frames")
	}
	return peak, nil
}

type cachedDecoder struct {
	video.Decoder
	metadata video.Metadata
}

func (d cachedDecoder) Probe(context.Context, string) (video.Metadata, error) {
	return d.metadata, nil
}
