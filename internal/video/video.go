package video

import "context"

type Metadata struct {
	Path            string  `json:"path"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	FPS             float64 `json:"fps"`
	DurationSeconds float64 `json:"duration_seconds"`
	Codec           string  `json:"codec"`
}

type Frame struct {
	Index     int
	Timestamp float64
	Gray      []byte
	Width     int
	Height    int
}

type Stream interface {
	Next() (Frame, error)
	Close() error
}

type Decoder interface {
	Probe(ctx context.Context, path string) (Metadata, error)
	Open(ctx context.Context, source Metadata, options DecodeOptions) (Stream, error)
}

type DecodeOptions struct {
	Width           int
	FPS             float64
	StartSeconds    float64
	DurationSeconds float64
}
