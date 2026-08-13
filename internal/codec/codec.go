package codec

import (
	"errors"
)

var ErrUnavailable = errors.New("codec motion vectors unavailable: rebuild with CGO_ENABLED=1 and -tags libav")

type MotionVector struct {
	Source int
	X      int
	Y      int
	Width  int
	Height int
	DX     float64
	DY     float64
}

type Frame struct {
	Index      int
	Timestamp  float64
	Width      int
	Height     int
	Keyframe   bool
	Vectors    []MotionVector
	Gray       []byte
	GrayWidth  int
	GrayHeight int
}

type Extractor interface {
	Next() (Frame, error)
	Close() error
}
