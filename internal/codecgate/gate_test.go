package codecgate

import (
	"context"
	"io"
	"testing"

	"github.com/berkantay/gomorph/internal/codec"
)

func TestLumaGateFindsLocalizedNonLinearChange(t *testing.T) {
	const width, height, fps = 96, 64, 24
	frames := make([]codec.Frame, 36)
	for index := range frames {
		pixels := make([]byte, width*height)
		for i := range pixels {
			pixels[i] = 40
		}
		if index >= 12 && index <= 20 {
			step := index - 11
			value := byte(min(240, 40+step*step*3))
			paint(pixels, width, 32, 16, 24, 24, value)
		}
		frames[index] = codec.Frame{
			Index: index, Timestamp: float64(index) / fps,
			Width: width, Height: height, Keyframe: index == 0,
			Gray: pixels, GrayWidth: width, GrayHeight: height,
		}
	}

	report, err := Analyze(context.Background(), &sliceExtractor{frames: frames}, Config{
		CellSize: 16, Threshold: 0.5, MinDuration: 0.2,
		LumaTileSize: 16, LumaMaxShift: 4, LumaLocalRadius: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Segments) == 0 {
		t.Fatalf("expected a candidate segment, peak confidence %.3f", report.PeakConfidence)
	}
	peak := report.Segments[0].PeakSeconds
	if peak < 0.5 || peak > 0.9 {
		t.Fatalf("expected peak during the synthetic change, got %.3f", peak)
	}
}

type sliceExtractor struct {
	frames []codec.Frame
	index  int
}

func (e *sliceExtractor) Next() (codec.Frame, error) {
	if e.index >= len(e.frames) {
		return codec.Frame{}, io.EOF
	}
	frame := e.frames[e.index]
	e.index++
	return frame, nil
}

func (*sliceExtractor) Close() error { return nil }

func paint(pixels []byte, stride, x, y, width, height int, value byte) {
	for py := y; py < y+height; py++ {
		for px := x; px < x+width; px++ {
			pixels[py*stride+px] = value
		}
	}
}
