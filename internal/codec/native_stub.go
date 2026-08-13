//go:build !libav || !cgo

package codec

type Native struct {
	GrayWidth int
}

func (Native) Open(string) (Extractor, error) { return nil, ErrUnavailable }
