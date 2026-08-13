package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/berkantay/gomorph/internal/analyze"
	"github.com/berkantay/gomorph/internal/cascade"
	"github.com/berkantay/gomorph/internal/codec"
	"github.com/berkantay/gomorph/internal/codecgate"
	"github.com/berkantay/gomorph/internal/detector"
	jsonreport "github.com/berkantay/gomorph/internal/report"
	"github.com/berkantay/gomorph/internal/video"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return errors.New("missing command")
	}

	switch os.Args[1] {
	case "analyze":
		return runAnalyze(os.Args[2:])
	case "codec-analyze":
		return runCodecAnalyze(os.Args[2:])
	case "cascade-analyze":
		return runCascadeAnalyze(os.Args[2:])
	case "version":
		fmt.Printf("gomorph %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func runCascadeAnalyze(args []string) error {
	flags := flag.NewFlagSet("cascade-analyze", flag.ContinueOnError)
	input := flags.String("input", "", "input video path")
	output := flags.String("output", "morph-cascade-report.json", "JSON report path, or - for stdout")
	gateWidth := flags.Int("gate-width", 160, "native luma gate width")
	gateThreshold := flags.Float64("gate-threshold", 0.50, "high-recall gate threshold")
	detailWidth := flags.Int("detail-width", 480, "candidate refinement width")
	detailFPS := flags.Float64("detail-fps", 30, "candidate refinement frames per second")
	detailTileSize := flags.Int("detail-tile-size", 24, "candidate refinement tile size")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("--input is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	extractor, err := (codec.Native{GrayWidth: *gateWidth}).Open(*input)
	if err != nil {
		return err
	}
	defer extractor.Close()

	result, err := cascade.Analyze(ctx, extractor, video.NewFFmpeg(), cascade.Options{
		InputPath:    *input,
		GateCellSize: 32, GateThreshold: *gateThreshold, GateMinDuration: 0.2,
		GateTileSize: 16, GateMaxShift: 8, GateLocalRadius: 3,
		DetailWidth: *detailWidth, DetailFPS: *detailFPS, DetailTileSize: *detailTileSize,
		DetailMaxShift: 8, DetailLocalRadius: 3,
	})
	if err != nil {
		return err
	}
	if err := jsonreport.WriteJSON(*output, result); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "cascade-analyzed %.2fs in %.3fs: %d candidate refinement(s)\n",
		result.Video.DurationSeconds, result.RuntimeSeconds, len(result.Refinements))
	return nil
}

func runCodecAnalyze(args []string) error {
	flags := flag.NewFlagSet("codec-analyze", flag.ContinueOnError)
	input := flags.String("input", "", "input video path")
	output := flags.String("output", "codec-motion-report.json", "JSON report path, or - for stdout")
	cellSize := flags.Int("cell-size", 32, "motion field cell size in source pixels")
	gateWidth := flags.Int("gate-width", 160, "luma curvature gate width")
	threshold := flags.Float64("threshold", 0.50, "candidate segment confidence threshold")
	minDuration := flags.Float64("min-duration", 0.20, "minimum candidate duration in seconds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("--input is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	extractor, err := (codec.Native{GrayWidth: *gateWidth}).Open(*input)
	if err != nil {
		return err
	}
	defer extractor.Close()

	started := time.Now()
	result, err := codecgate.Analyze(ctx, extractor, codecgate.Config{
		CellSize: *cellSize, Threshold: *threshold, MinDuration: *minDuration,
		LumaTileSize: 16, LumaMaxShift: 8, LumaLocalRadius: 3,
	})
	if err != nil {
		return err
	}
	result.RuntimeSeconds = time.Since(started).Seconds()
	if err := jsonreport.WriteJSON(*output, result); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codec-analyzed %.2fs in %.3fs: %d candidate segment(s), peak %.3f\n",
		result.DurationSeconds, result.RuntimeSeconds, len(result.Segments), result.PeakConfidence)
	return nil
}

func runAnalyze(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	input := flags.String("input", "", "input video path")
	output := flags.String("output", "morph-report.json", "JSON report path, or - for stdout")
	width := flags.Int("width", 320, "analysis width in pixels")
	fps := flags.Float64("fps", 15, "analysis frames per second; capped at source rate; 0 keeps source rate")
	tileSize := flags.Int("tile-size", 32, "regional analysis tile size")
	maxShift := flags.Int("max-shift", 8, "maximum global camera translation per analyzed frame")
	localRadius := flags.Int("local-radius", 3, "local motion search radius around camera motion")
	threshold := flags.Float64("threshold", 0.60, "candidate segment confidence threshold")
	minDuration := flags.Float64("min-duration", 0.20, "minimum candidate duration in seconds")
	quiet := flags.Bool("quiet", false, "disable progress output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("--input is required")
	}
	if *width < 64 || *width > 1920 {
		return errors.New("--width must be between 64 and 1920")
	}
	if *fps < 0 || *fps > 240 {
		return errors.New("--fps must be between 0 and 240")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	result, err := analyze.Video(ctx, analyze.Options{
		InputPath:   *input,
		AnalysisFPS: *fps,
		Width:       *width,
		Threshold:   *threshold,
		MinDuration: *minDuration,
		Detector: detector.Config{
			TileSize:    *tileSize,
			MaxShift:    *maxShift,
			LocalRadius: *localRadius,
		},
		OnProgress: func(frames int, timestamp float64) {
			if !*quiet && frames%30 == 0 {
				fmt.Fprintf(os.Stderr, "\rprocessed %d frames (%.1fs)", frames, timestamp)
			}
		},
	}, video.NewFFmpeg())
	if err != nil {
		return err
	}
	result.RuntimeSeconds = time.Since(started).Seconds()
	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}
	if err := analyze.WriteReport(*output, result); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "analyzed %.2fs in %.2fs: %d candidate segment(s), peak %.3f\n",
		result.Video.DurationSeconds, result.RuntimeSeconds, len(result.Segments), result.Summary.PeakConfidence)
	return nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage:
  gomorph analyze --input VIDEO [options]
  gomorph codec-analyze --input VIDEO [options]
  gomorph cascade-analyze --input VIDEO [options]
  gomorph version

Fast, full-frame video morph candidate detection.`)
}
