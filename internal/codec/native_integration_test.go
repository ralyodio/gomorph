//go:build libav && cgo

package codec

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeExtractsMotionAndLuma(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is not installed")
	}
	path := filepath.Join(t.TempDir(), "motion.mp4")
	command := exec.Command(ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "testsrc2=s=96x64:r=12:d=1",
		"-c:v", "mpeg4", "-q:v", "4", "-y", path,
	)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("generate fixture: %v: %s", runErr, output)
	}

	extractor, err := (Native{GrayWidth: 48}).Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer extractor.Close()
	frames := 0
	framesWithMotion := 0
	for {
		frame, nextErr := extractor.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		frames++
		if len(frame.Vectors) > 0 {
			framesWithMotion++
		}
		if frame.GrayWidth != 48 || frame.GrayHeight != 32 || len(frame.Gray) != 48*32 {
			t.Fatalf("unexpected grayscale frame: %dx%d, %d bytes", frame.GrayWidth, frame.GrayHeight, len(frame.Gray))
		}
	}
	if frames != 12 {
		t.Fatalf("expected 12 decoded frames, got %d", frames)
	}
	if framesWithMotion == 0 {
		t.Fatal("expected exported codec motion vectors")
	}
}
