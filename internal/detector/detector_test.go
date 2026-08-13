package detector

import (
	"math/rand"
	"testing"

	"github.com/berkantay/gomorph/internal/video"
)

func TestStaticFramesHaveNoMorphSignal(t *testing.T) {
	engine := newTestEngine(t)
	frames := []video.Frame{
		testFrame(0, solidPixels(96, 64, 80)),
		testFrame(1, solidPixels(96, 64, 80)),
		testFrame(2, solidPixels(96, 64, 80)),
	}

	observation := pushAll(t, engine, frames)
	if observation.RawScore != 0 {
		t.Fatalf("expected no signal, got %.4f", observation.RawScore)
	}
}

func TestLinearLightingChangeIsSuppressed(t *testing.T) {
	engine := newTestEngine(t)
	frames := []video.Frame{
		testFrame(0, solidPixels(96, 64, 50)),
		testFrame(1, solidPixels(96, 64, 70)),
		testFrame(2, solidPixels(96, 64, 90)),
	}

	observation := pushAll(t, engine, frames)
	if observation.RawScore > 0.02 {
		t.Fatalf("expected linear lighting suppression, got %.4f", observation.RawScore)
	}
}

func TestConstantCameraVelocityIsStabilized(t *testing.T) {
	engine := newTestEngine(t)
	base := texturedPixels(96, 64, 42)
	frames := []video.Frame{
		testFrame(0, shifted(base, 96, 64, 0, 0)),
		testFrame(1, shifted(base, 96, 64, 2, 0)),
		testFrame(2, shifted(base, 96, 64, 4, 0)),
	}

	observation := pushAll(t, engine, frames)
	if observation.SceneCut {
		t.Fatal("constant camera movement was classified as a cut")
	}
	if observation.RawScore > 0.12 {
		t.Fatalf("expected stabilized camera movement, got %.4f", observation.RawScore)
	}
}

func TestLocalNonLinearChangeProducesSignalAndHotspot(t *testing.T) {
	engine := newTestEngine(t)
	first := solidPixels(96, 64, 40)
	second := append([]byte(nil), first...)
	third := append([]byte(nil), first...)
	paint(second, 96, 32, 16, 24, 24, 90)
	paint(third, 96, 32, 16, 24, 24, 190)

	observation := pushAll(t, engine, []video.Frame{
		testFrame(0, first), testFrame(1, second), testFrame(2, third),
	})
	if observation.SceneCut {
		t.Fatal("local change was classified as a scene cut")
	}
	if observation.RawScore < 0.20 {
		t.Fatalf("expected a strong local signal, got %.4f", observation.RawScore)
	}
	if len(observation.Hotspots) == 0 || observation.Hotspots[0].X != 32 || observation.Hotspots[0].Y != 16 {
		t.Fatalf("unexpected primary hotspot: %+v", observation.Hotspots)
	}
}

func TestSelectTopRegions(t *testing.T) {
	regions := []Region{{Score: 2}, {Score: 9}, {Score: 1}, {Score: 7}, {Score: 4}, {Score: 8}}
	selectTopRegions(regions, 3)
	want := []float64{9, 8, 7}
	for i, score := range want {
		if regions[i].Score != score {
			t.Fatalf("top region %d: expected %.0f, got %.0f", i, score, regions[i].Score)
		}
	}
}

func BenchmarkPush320x180(b *testing.B) {
	benchmarkPush(b, 320, 180, 32)
}

func BenchmarkPush480x854(b *testing.B) {
	benchmarkPush(b, 480, 854, 24)
}

func benchmarkPush(b *testing.B, width, height, tileSize int) {
	b.Helper()
	engine, err := New(Config{TileSize: tileSize, MaxShift: 8, LocalRadius: 3})
	if err != nil {
		b.Fatal(err)
	}
	frames := make([]video.Frame, 3)
	for i := range frames {
		frames[i] = video.Frame{Index: i, Gray: texturedPixels(width, height, int64(i+1)), Width: width, Height: height}
	}
	_, _, _ = engine.Push(frames[0])
	_, _, _ = engine.Push(frames[1])
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frames[2].Index = i + 2
		if _, _, err := engine.Push(frames[2]); err != nil {
			b.Fatal(err)
		}
	}
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := New(Config{TileSize: 16, MaxShift: 6, LocalRadius: 2})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func pushAll(t *testing.T, engine *Engine, frames []video.Frame) Observation {
	t.Helper()
	var observation Observation
	for _, frame := range frames {
		item, ready, err := engine.Push(frame)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			observation = item
		}
	}
	return observation
}

func testFrame(index int, pixels []byte) video.Frame {
	return video.Frame{Index: index, Timestamp: float64(index) / 15, Gray: pixels, Width: 96, Height: 64}
}

func solidPixels(width, height int, value byte) []byte {
	pixels := make([]byte, width*height)
	for i := range pixels {
		pixels[i] = value
	}
	return pixels
}

func texturedPixels(width, height int, seed int64) []byte {
	random := rand.New(rand.NewSource(seed))
	pixels := make([]byte, width*height)
	for i := range pixels {
		pixels[i] = byte(random.Intn(256))
	}
	return pixels
}

func shifted(source []byte, width, height, dx, dy int) []byte {
	result := make([]byte, len(source))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx, sy := x+dx, y+dy
			if sx >= 0 && sx < width && sy >= 0 && sy < height {
				result[y*width+x] = source[sy*width+sx]
			}
		}
	}
	return result
}

func paint(pixels []byte, stride, x, y, width, height int, value byte) {
	for py := y; py < y+height; py++ {
		for px := x; px < x+width; px++ {
			pixels[py*stride+px] = value
		}
	}
}
