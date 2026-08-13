# GoMorph

![GoMorph detecting a product and background deformation](assets/gomorph.png)

**Catch the frame where reality gets weird.**

GoMorph is a fast, training-free experiment for locating morph-like deformation in videos. It does not assume that the manipulated subject is a face: hotspots can occur on products, objects, text, or the background.

It ships two execution paths:

- a portable Go/FFmpeg detector with no model, GPU, Python, OpenCV, or CGo requirement;
- an optional native cascade that reads codec motion vectors and downscaled luma in-process through libavcodec, gates the complete video at low resolution, and performs detailed analysis only inside suspicious windows.

The native cascade is the recommended path for long videos and streaming workloads. The portable detector remains the simplest build and fallback.

## What it measures

The precise detector:

1. estimates and removes global camera translation;
2. estimates motion independently in small image regions;
3. measures changes in regional motion velocity;
4. measures camera- and lighting-compensated second-order pixel change;
5. combines motion acceleration, warp error, and photometric acceleration;
6. suppresses hard scene cuts;
7. aggregates evidence in a 0.4-second temporal window;
8. calibrates the score against the video's own baseline.

The native cascade adds:

1. in-process H.264/HEVC/AV1 decoding through libavcodec;
2. codec motion-vector extraction for motion-complexity diagnostics;
3. a 160-pixel luma-curvature gate over the complete video;
4. sparse 480-pixel refinement around candidate intervals.

Codec motion vectors are not treated as a verdict. They depend on GOP structure, reference frames, codec, and encoder settings. The luma gate protects recall, while codec vectors remain an inexpensive complementary signal.

This is an efficient classical detector, not a claim of universal AI-video detection or a calibrated authenticity classifier. Confidence is relative to the analyzed video.

## Requirements

- Go 1.25 or newer
- `ffmpeg` and `ffprobe` available in `PATH`

The native build additionally requires libavformat, libavcodec, libavutil, libswscale, CGo, and `pkg-config`.

macOS:

```bash
brew install ffmpeg
```

## Build and run

```bash
make build

./bin/gomorph analyze \
  --input /absolute/path/to/video.mp4 \
  --output reports/video.json
```

The defaults analyze at 320 pixels wide and 15 FPS. This is the recommended fast first pass. For subtle or very short morphs:

```bash
./bin/gomorph analyze \
  --input /absolute/path/to/video.mp4 \
  --output reports/video-high-sensitivity.json \
  --width 480 \
  --fps 30 \
  --tile-size 24
```

Set `--fps 0` to preserve the video's source frame rate. Values above the source frame rate are automatically capped to avoid analyzing duplicated frames. Write JSON to stdout with `--output -`.

## Native codec cascade

```bash
make build-native

./bin/gomorph-native cascade-analyze \
  --input /absolute/path/to/video.mp4 \
  --output reports/video-cascade.json
```

The cascade report contains the low-resolution gate timeline, codec motion diagnostics, candidate intervals, and detailed hotspots. To inspect the gate without refinement:

```bash
./bin/gomorph-native codec-analyze \
  --input /absolute/path/to/video.mp4 \
  --output reports/video-codec.json
```

For a short clip, native gate plus refinement can be slower than the already-fast portable pass because refinement starts an FFmpeg process. The cascade becomes advantageous when videos are long and suspicious intervals are sparse. A future zero-copy refinement backend can remove that remaining process boundary.

## Report

The JSON report includes:

- candidate time ranges and their peak confidence;
- a per-frame timeline;
- the strongest rectangular hotspots per analyzed frame;
- camera motion, photometric change, and motion acceleration diagnostics;
- runtime and source-video metadata.

Coordinates refer to the downscaled analysis dimensions in the report. Confidence is calibrated within one video and is useful for ranking suspicious moments. It is not yet a calibrated probability across unrelated videos.

## Performance controls

| Option | Default | Effect |
|---|---:|---|
| `--width` | 320 | Lower is faster; higher catches smaller deformations. |
| `--fps` | 15 | Lower is faster; higher catches shorter events. |
| `--tile-size` | 32 | Larger is faster; smaller localizes more precisely. |
| `--max-shift` | 8 | Camera translation search radius. |
| `--local-radius` | 3 | Regional motion search around camera motion. |
| `--threshold` | 0.60 | Candidate segment confidence threshold. |

Runtime grows approximately with pixels, FPS, and the square of the motion search radii.

## Reference measurement

One verified 4.01-second, 720×1280, 24 FPS H.264 clip on an Apple M4 Pro produced:

| Path | Observed runtime | Result |
|---|---:|---|
| Native gate | ~0.060 s | One candidate interval |
| Native cascade | ~0.146 s | Peak at 2.1667 s with a localized hotspot |
| Portable detailed pass | ~0.14–0.18 s | Peak at 2.1667 s |

These are development measurements, not universal performance claims. Public benchmarks must report cold and warm runs, codec, dimensions, duration, CPU, memory, and detection accuracy together.

A 60-second scalability fixture was created by padding the verified clip with frozen frames while preserving the morph at 30.1667 seconds. On the same machine:

| Path | Observed runtime | Throughput | Result |
|---|---:|---:|---|
| Native cascade | ~0.60 s | ~100× real-time | Peak at 30.1667 s |
| Portable detailed pass | ~1.19 s | ~50× real-time | Peak at 30.1667 s |

The padded fixture measures sparse-cascade scaling, not detection accuracy or generalization.

## Validation path

The reference clip's known morph and strongest localized deformation both occur at 2.1667 seconds. The next step is a broader timestamp- and region-annotated benchmark covering hard negatives: camera motion, zoom, focus changes, lighting flicker, reflections, water, smoke, compression, and ordinary object motion.

Planned accuracy extensions preserve the CLI/report contract:

- dense optical-flow residuals;
- semantic features from a frozen video/image encoder;
- segmentation and object tracking;
- a small temporal classifier trained on real and synthetic morph transitions.

The next benchmark milestone is a timestamp- and region-annotated dataset with generator holdouts and difficult natural negatives.

## Research context

- [D3](https://openaccess.thecvf.com/content/ICCV2025/html/Zheng_D3_Training-Free_AI-Generated_Video_Detection_Using_Second-Order_Features_ICCV_2025_paper.html) studies training-free second-order temporal features for video-level synthetic detection.
- [CoViAR](https://openaccess.thecvf.com/content_cvpr_2018/html/Wu_Compressed_Video_Action_CVPR_2018_paper.html) demonstrates the efficiency of compressed-domain video representations.
- [UNITE](https://openaccess.thecvf.com/content/CVPR2025/html/Kundu_Towards_a_Universal_Synthetic_Video_Detector_From_Face_or_Background_CVPR_2025_paper.html) addresses full-frame synthetic video rather than only faces.

This project focuses on a different operational target: fast temporal and spatial localization of morph-like events.

## Development

```bash
make check
make check-native
go test -bench=. ./internal/detector
```
