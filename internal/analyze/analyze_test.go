package analyze

import (
	"context"
	"io"
	"testing"

	"github.com/berkantay/gomorph/internal/detector"
	"github.com/berkantay/gomorph/internal/video"
)

func TestVideoCapsRequestedFPSAtSourceRate(t *testing.T) {
	decoder := &recordingDecoder{metadata: video.Metadata{FPS: 24, Width: 720, Height: 1280}}
	_, err := Video(context.Background(), Options{
		InputPath: "video.mp4", AnalysisFPS: 30, Width: 480,
		Detector: detector.Config{TileSize: 24, MaxShift: 8, LocalRadius: 3},
	}, decoder)
	if err != nil {
		t.Fatal(err)
	}
	if decoder.openedFPS != 24 {
		t.Fatalf("expected source-rate cap at 24 FPS, got %.2f", decoder.openedFPS)
	}
}

func TestCalibrationElevatesOutlierAndKeepsBaselineLow(t *testing.T) {
	timeline := make([]detector.Observation, 20)
	for i := range timeline {
		timeline[i].RawScore = 0.04 + float64(i%3)*0.002
	}
	timeline[10].RawScore = 0.45

	calibrate(timeline, 10)
	if timeline[10].Confidence < 0.8 {
		t.Fatalf("expected high outlier confidence, got %.4f", timeline[10].Confidence)
	}
	if timeline[0].Confidence > 0.2 {
		t.Fatalf("expected low baseline confidence, got %.4f", timeline[0].Confidence)
	}
}

func TestSegmentsMergeShortGaps(t *testing.T) {
	timeline := make([]detector.Observation, 10)
	for i := range timeline {
		timeline[i].Timestamp = float64(i) / 10
	}
	for _, index := range []int{2, 3, 5, 6} {
		timeline[index].Confidence = 0.9
	}

	result := segments(timeline, 0.65, 0.2, 10)
	if len(result) != 1 {
		t.Fatalf("expected one segment, got %+v", result)
	}
	if result[0].StartSeconds != 0 || result[0].EndSeconds != 0.8 {
		t.Fatalf("unexpected segment bounds: %+v", result[0])
	}
}

type recordingDecoder struct {
	metadata  video.Metadata
	openedFPS float64
}

func (d *recordingDecoder) Probe(context.Context, string) (video.Metadata, error) {
	return d.metadata, nil
}

func (d *recordingDecoder) Open(_ context.Context, _ video.Metadata, options video.DecodeOptions) (video.Stream, error) {
	d.openedFPS = options.FPS
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Next() (video.Frame, error) { return video.Frame{}, io.EOF }
func (emptyStream) Close() error               { return nil }
