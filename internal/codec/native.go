//go:build libav && cgo

package codec

/*
#cgo pkg-config: libavformat libavcodec libavutil libswscale
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/error.h>
#include <libavutil/frame.h>
#include <libavutil/motion_vector.h>
#include <libswscale/swscale.h>

typedef struct {
	int source;
	int x;
	int y;
	int width;
	int height;
	double dx;
	double dy;
} md_vector;

typedef struct {
	double timestamp;
	int width;
	int height;
	int keyframe;
	int vector_count;
	const md_vector *vectors;
	int gray_width;
	int gray_height;
	const uint8_t *gray;
} md_frame;

typedef struct {
	AVFormatContext *format;
	AVCodecContext *decoder;
	AVPacket *packet;
	AVFrame *frame;
	int stream_index;
	int draining;
	AVRational time_base;
	md_vector *vectors;
	int vector_capacity;
	struct SwsContext *scaler;
	uint8_t *gray;
	int gray_width;
	int gray_height;
} md_context;

static void md_error(char *buffer, int size, const char *operation, int code) {
	char detail[AV_ERROR_MAX_STRING_SIZE] = {0};
	av_strerror(code, detail, sizeof(detail));
	snprintf(buffer, size, "%s: %s", operation, detail);
}

static void md_close(md_context *context) {
	if (!context) return;
	av_frame_free(&context->frame);
	av_packet_free(&context->packet);
	avcodec_free_context(&context->decoder);
	avformat_close_input(&context->format);
	sws_freeContext(context->scaler);
	free(context->vectors);
	free(context->gray);
	free(context);
}

static md_context *md_open(const char *path, int gray_width, char *error_buffer, int error_size) {
	md_context *context = calloc(1, sizeof(md_context));
	if (!context) {
		snprintf(error_buffer, error_size, "allocate codec context");
		return NULL;
	}

	int result = avformat_open_input(&context->format, path, NULL, NULL);
	if (result < 0) {
		md_error(error_buffer, error_size, "open video", result);
		md_close(context);
		return NULL;
	}
	result = avformat_find_stream_info(context->format, NULL);
	if (result < 0) {
		md_error(error_buffer, error_size, "read stream info", result);
		md_close(context);
		return NULL;
	}

	const AVCodec *codec = NULL;
	context->stream_index = av_find_best_stream(context->format, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0);
	if (context->stream_index < 0) {
		md_error(error_buffer, error_size, "find video stream", context->stream_index);
		md_close(context);
		return NULL;
	}
	context->time_base = context->format->streams[context->stream_index]->time_base;
	context->decoder = avcodec_alloc_context3(codec);
	if (!context->decoder) {
		snprintf(error_buffer, error_size, "allocate decoder");
		md_close(context);
		return NULL;
	}
	result = avcodec_parameters_to_context(context->decoder, context->format->streams[context->stream_index]->codecpar);
	if (result < 0) {
		md_error(error_buffer, error_size, "copy codec parameters", result);
		md_close(context);
		return NULL;
	}
	context->decoder->flags2 |= AV_CODEC_FLAG2_EXPORT_MVS;
	context->decoder->thread_count = 0;
	result = avcodec_open2(context->decoder, codec, NULL);
	if (result < 0) {
		md_error(error_buffer, error_size, "open decoder", result);
		md_close(context);
		return NULL;
	}

	context->packet = av_packet_alloc();
	context->frame = av_frame_alloc();
	if (!context->packet || !context->frame) {
		snprintf(error_buffer, error_size, "allocate decode buffers");
		md_close(context);
		return NULL;
	}
	if (gray_width > 0) {
		context->gray_width = gray_width;
		context->gray_height = (int)((int64_t)context->decoder->height * gray_width / context->decoder->width);
		if (context->gray_height < 2) context->gray_height = 2;
		if (context->gray_height & 1) context->gray_height++;
		context->gray = malloc((size_t)context->gray_width * context->gray_height);
		if (!context->gray) {
			snprintf(error_buffer, error_size, "allocate grayscale frame");
			md_close(context);
			return NULL;
		}
	}
	return context;
}

static int md_export_frame(md_context *context, md_frame *output, char *error_buffer, int error_size) {
	AVFrameSideData *side_data = av_frame_get_side_data(context->frame, AV_FRAME_DATA_MOTION_VECTORS);
	int count = side_data ? (int)(side_data->size / sizeof(AVMotionVector)) : 0;
	if (count > context->vector_capacity) {
		md_vector *resized = realloc(context->vectors, sizeof(md_vector) * count);
		if (!resized) {
			snprintf(error_buffer, error_size, "allocate motion vectors");
			return -1;
		}
		context->vectors = resized;
		context->vector_capacity = count;
	}
	if (count > 0) {
		const AVMotionVector *vectors = (const AVMotionVector *)side_data->data;
		for (int i = 0; i < count; i++) {
			const AVMotionVector *source = &vectors[i];
			md_vector *target = &context->vectors[i];
			target->source = source->source;
			target->x = source->dst_x;
			target->y = source->dst_y;
			target->width = source->w;
			target->height = source->h;
			target->dx = source->motion_scale ? (double)source->motion_x / source->motion_scale : 0.0;
			target->dy = source->motion_scale ? (double)source->motion_y / source->motion_scale : 0.0;
		}
	}

	int64_t timestamp = context->frame->best_effort_timestamp;
	output->timestamp = timestamp == AV_NOPTS_VALUE ? 0.0 : timestamp * av_q2d(context->time_base);
	output->width = context->frame->width;
	output->height = context->frame->height;
	output->keyframe = !!(context->frame->flags & AV_FRAME_FLAG_KEY);
	output->vector_count = count;
	output->vectors = context->vectors;
	output->gray_width = 0;
	output->gray_height = 0;
	output->gray = NULL;
	if (context->gray_width > 0) {
		context->scaler = sws_getCachedContext(
			context->scaler,
			context->frame->width, context->frame->height, context->frame->format,
			context->gray_width, context->gray_height, AV_PIX_FMT_GRAY8,
			SWS_FAST_BILINEAR, NULL, NULL, NULL
		);
		if (!context->scaler) {
			snprintf(error_buffer, error_size, "initialize grayscale scaler");
			return -1;
		}
		uint8_t *destination[4] = {context->gray, NULL, NULL, NULL};
		int destination_stride[4] = {context->gray_width, 0, 0, 0};
		sws_scale(
			context->scaler,
			(const uint8_t *const *)context->frame->data, context->frame->linesize,
			0, context->frame->height,
			destination, destination_stride
		);
		output->gray_width = context->gray_width;
		output->gray_height = context->gray_height;
		output->gray = context->gray;
	}
	return 1;
}

static int md_next(md_context *context, md_frame *output, char *error_buffer, int error_size) {
	for (;;) {
		int result = avcodec_receive_frame(context->decoder, context->frame);
		if (result == 0) {
			return md_export_frame(context, output, error_buffer, error_size);
		}
		if (result == AVERROR_EOF) return 0;
		if (result != AVERROR(EAGAIN)) {
			md_error(error_buffer, error_size, "decode frame", result);
			return -1;
		}
		if (context->draining) return 0;

		for (;;) {
			result = av_read_frame(context->format, context->packet);
			if (result < 0) {
				result = avcodec_send_packet(context->decoder, NULL);
				context->draining = 1;
				if (result < 0 && result != AVERROR_EOF) {
					md_error(error_buffer, error_size, "flush decoder", result);
					return -1;
				}
				break;
			}
			if (context->packet->stream_index != context->stream_index) {
				av_packet_unref(context->packet);
				continue;
			}
			result = avcodec_send_packet(context->decoder, context->packet);
			av_packet_unref(context->packet);
			if (result < 0) {
				md_error(error_buffer, error_size, "send video packet", result);
				return -1;
			}
			break;
		}
	}
}
*/
import "C"

import (
	"fmt"
	"io"
	"runtime"
	"unsafe"
)

type Native struct {
	GrayWidth int
}

type nativeExtractor struct {
	context *C.md_context
	index   int
}

func (n Native) Open(path string) (Extractor, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	errorBuffer := make([]C.char, 512)
	context := C.md_open(cPath, C.int(n.GrayWidth), &errorBuffer[0], C.int(len(errorBuffer)))
	if context == nil {
		return nil, fmt.Errorf("open codec motion stream: %s", C.GoString(&errorBuffer[0]))
	}
	return &nativeExtractor{context: context}, nil
}

func (e *nativeExtractor) Next() (Frame, error) {
	if e.context == nil {
		return Frame{}, io.EOF
	}
	var nativeFrame C.md_frame
	errorBuffer := make([]C.char, 512)
	result := C.md_next(e.context, &nativeFrame, &errorBuffer[0], C.int(len(errorBuffer)))
	runtime.KeepAlive(e)
	if result == 0 {
		return Frame{}, io.EOF
	}
	if result < 0 {
		return Frame{}, fmt.Errorf("read codec motion frame: %s", C.GoString(&errorBuffer[0]))
	}

	count := int(nativeFrame.vector_count)
	vectors := make([]MotionVector, count)
	if count > 0 {
		nativeVectors := unsafe.Slice((*C.md_vector)(unsafe.Pointer(nativeFrame.vectors)), count)
		for i, vector := range nativeVectors {
			vectors[i] = MotionVector{
				Source: int(vector.source), X: int(vector.x), Y: int(vector.y),
				Width: int(vector.width), Height: int(vector.height),
				DX: float64(vector.dx), DY: float64(vector.dy),
			}
		}
	}
	graySize := int(nativeFrame.gray_width * nativeFrame.gray_height)
	var gray []byte
	if graySize > 0 {
		gray = C.GoBytes(unsafe.Pointer(nativeFrame.gray), C.int(graySize))
	}
	frame := Frame{
		Index: e.index, Timestamp: float64(nativeFrame.timestamp),
		Width: int(nativeFrame.width), Height: int(nativeFrame.height),
		Keyframe: nativeFrame.keyframe != 0, Vectors: vectors,
		Gray: gray, GrayWidth: int(nativeFrame.gray_width), GrayHeight: int(nativeFrame.gray_height),
	}
	e.index++
	return frame, nil
}

func (e *nativeExtractor) Close() error {
	if e.context != nil {
		C.md_close(e.context)
		e.context = nil
	}
	return nil
}
