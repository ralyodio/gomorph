package video

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

type FFmpeg struct{}

func NewFFmpeg() FFmpeg { return FFmpeg{} }

func (FFmpeg) Probe(ctx context.Context, path string) (Metadata, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return Metadata{}, errors.New("ffprobe is required but was not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,codec_name:format=duration", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return Metadata{}, fmt.Errorf("probe video: %w", err)
	}
	var payload struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			FrameRate string `json:"r_frame_rate"`
			Codec     string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Metadata{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	if len(payload.Streams) == 0 {
		return Metadata{}, errors.New("input contains no video stream")
	}
	stream := payload.Streams[0]
	duration, _ := strconv.ParseFloat(payload.Format.Duration, 64)
	return Metadata{
		Path: path, Width: stream.Width, Height: stream.Height,
		FPS: parseRate(stream.FrameRate), DurationSeconds: duration, Codec: stream.Codec,
	}, nil
}

func (FFmpeg) Open(ctx context.Context, source Metadata, options DecodeOptions) (Stream, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("ffmpeg is required but was not found in PATH")
	}
	if source.Path == "" || source.Width <= 0 || source.Height <= 0 {
		return nil, errors.New("valid probed video metadata is required")
	}
	width := options.Width
	fps := options.FPS
	height := scaledHeight(source.Width, source.Height, width)
	filters := make([]string, 0, 2)
	if fps > 0 {
		filters = append(filters, fmt.Sprintf("fps=%.6f", fps))
	} else {
		fps = source.FPS
	}
	filters = append(filters, fmt.Sprintf("scale=%d:%d:flags=fast_bilinear", width, height))
	arguments := []string{"-v", "error"}
	if options.StartSeconds > 0 {
		arguments = append(arguments, "-ss", strconv.FormatFloat(options.StartSeconds, 'f', 6, 64))
	}
	arguments = append(arguments, "-i", source.Path)
	if options.DurationSeconds > 0 {
		arguments = append(arguments, "-t", strconv.FormatFloat(options.DurationSeconds, 'f', 6, 64))
	}
	arguments = append(arguments, "-an", "-sn", "-dn", "-vf", strings.Join(filters, ","), "-pix_fmt", "gray", "-f", "rawvideo", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", arguments...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open ffmpeg output: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	timestampOffset := options.StartSeconds
	if timestampOffset > 0 && fps > 0 {
		timestampOffset = math.Ceil(timestampOffset*fps-1e-9) / fps
	}
	return &ffmpegStream{
		cmd: cmd, reader: bufio.NewReaderSize(stdout, width*height*2), stderr: &stderr,
		width: width, height: height, fps: fps, frameSize: width * height, timestampOffset: timestampOffset,
	}, nil
}

type ffmpegStream struct {
	cmd             *exec.Cmd
	reader          *bufio.Reader
	stderr          *strings.Builder
	width           int
	height          int
	fps             float64
	frameSize       int
	index           int
	done            bool
	timestampOffset float64
}

func (s *ffmpegStream) Next() (Frame, error) {
	if s.done {
		return Frame{}, io.EOF
	}
	pixels := make([]byte, s.frameSize)
	_, err := io.ReadFull(s.reader, pixels)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		s.done = true
		if waitErr := s.cmd.Wait(); waitErr != nil {
			return Frame{}, fmt.Errorf("ffmpeg decode: %w: %s", waitErr, strings.TrimSpace(s.stderr.String()))
		}
		return Frame{}, io.EOF
	}
	if err != nil {
		return Frame{}, fmt.Errorf("read decoded frame: %w", err)
	}
	frame := Frame{Index: s.index, Timestamp: s.timestampOffset + float64(s.index)/s.fps, Gray: pixels, Width: s.width, Height: s.height}
	s.index++
	return frame, nil
}

func (s *ffmpegStream) Close() error {
	if s.done {
		return nil
	}
	s.done = true
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.cmd.Wait()
}

func scaledHeight(sourceWidth, sourceHeight, targetWidth int) int {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return targetWidth
	}
	height := int(float64(sourceHeight)*float64(targetWidth)/float64(sourceWidth) + 0.5)
	if height%2 != 0 {
		height++
	}
	if height < 2 {
		return 2
	}
	return height
}

func parseRate(value string) float64 {
	parts := strings.Split(value, "/")
	if len(parts) == 2 {
		numerator, errN := strconv.ParseFloat(parts[0], 64)
		denominator, errD := strconv.ParseFloat(parts[1], 64)
		if errN == nil && errD == nil && denominator != 0 {
			return numerator / denominator
		}
	}
	rate, _ := strconv.ParseFloat(value, 64)
	return rate
}
