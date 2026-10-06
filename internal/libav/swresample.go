package libav

/*
#include <stdint.h>
#include <stdlib.h>

// Forward declarations for C helper functions from swresample_helper.c
void *swr_alloc_helper(void);
void *swr_alloc_set_opts_helper(int64_t out_ch_layout, int out_sample_fmt, int out_sample_rate,
                                int64_t in_ch_layout, int in_sample_fmt, int in_sample_rate);
int swr_init_helper(void *s);
void swr_free_helper(void **s);
int swr_convert_helper(void *s, uint8_t **out, int out_count, uint8_t **in, int in_count);
int64_t swr_get_delay_helper(void *s, int64_t base);
int swr_set_compensation_helper(void *s, int sample_delta, int compensation_distance);
*/
import "C"
import (
	"fmt"
	"runtime"
	"unsafe"
)

// SwrContext represents FFmpeg swresample context
type SwrContext struct {
	cPtr unsafe.Pointer
}

// SwrAlloc allocates a new SwrContext
func SwrAlloc() *SwrContext {
	return &SwrContext{
		cPtr: unsafe.Pointer(C.swr_alloc_helper()),
	}
}

// SwrAllocSetOpts allocates and configures SwrContext with options
func SwrAllocSetOpts(outChLayout int64, outSampleFmt int, outSampleRate int, inChLayout int64, inSampleFmt int, inSampleRate int) *SwrContext {
	ctx := C.swr_alloc_set_opts_helper(
		C.int64_t(outChLayout),
		C.int(outSampleFmt),
		C.int(outSampleRate),
		C.int64_t(inChLayout),
		C.int(inSampleFmt),
		C.int(inSampleRate),
	)
	return &SwrContext{cPtr: unsafe.Pointer(ctx)}
}

// SwrInit initializes the SwrContext
func (s *SwrContext) Start() error {
	if s.cPtr == nil {
		return fmt.Errorf("SwrContext is nil")
	}
	if ret := C.swr_init_helper(s.cPtr); ret < 0 {
		return fmt.Errorf("swr_init failed: %d", ret)
	}
	return nil
}

// SwrFree frees the SwrContext
func (s *SwrContext) Free() {
	if s.cPtr != nil {
		cp := s.cPtr
		C.swr_free_helper(&cp)
		s.cPtr = cp
		// s.cPtr already zeroed by helper
	}
}

// SwrConvert converts audio samples
func (s *SwrContext) Convert(out [][]byte, outCount int, in [][]byte, inCount int) (int, error) {
	if s.cPtr == nil {
		return 0, fmt.Errorf("SwrContext not initialized")
	}

	// cgo: the pointer arrays and the data buffers are Go memory — pin
	// them for the duration of the synchronous swr_convert call.
	// (Pinner.Pin is transitive: pinning the array pins each &buf[0].)
	var pinner runtime.Pinner
	defer pinner.Unpin()

	outArr := new([16]*C.uint8_t)
	inArr := new([16]*C.uint8_t)
	var outPtrs, inPtrs **C.uint8_t
	if len(out) > 0 {
		if len(out) > len(outArr) {
			return 0, fmt.Errorf("swr_convert: too many out planes %d", len(out))
		}
		for i := range out {
			if len(out[i]) > 0 {
				pinner.Pin(&out[i][0])
				outArr[i] = (*C.uint8_t)(unsafe.Pointer(&out[i][0]))
			}
		}
		pinner.Pin(outArr)
		outPtrs = &outArr[0]
	}
	if len(in) > 0 {
		if len(in) > len(inArr) {
			return 0, fmt.Errorf("swr_convert: too many in planes %d", len(in))
		}
		for i := range in {
			if len(in[i]) > 0 {
				pinner.Pin(&in[i][0])
				inArr[i] = (*C.uint8_t)(unsafe.Pointer(&in[i][0]))
			}
		}
		pinner.Pin(inArr)
		inPtrs = &inArr[0]
	}

	ret := C.swr_convert_helper(
		s.cPtr,
		outPtrs,
		C.int(outCount),
		inPtrs,
		C.int(inCount),
	)

	if ret < 0 {
		return 0, fmt.Errorf("swr_convert failed: %d", ret)
	}
	return int(ret), nil
}

// SwrGetDelay gets the resampling delay
func (s *SwrContext) GetDelay(base int64) (int64, error) {
	if s.cPtr == nil {
		return 0, fmt.Errorf("SwrContext not initialized")
	}
	delay := C.swr_get_delay_helper(s.cPtr, C.int64_t(base))
	return int64(delay), nil
}

// SwrSetCompensation sets sample rate compensation
func (s *SwrContext) SetCompensation(sampleDelta, compensationDistance int) error {
	if s.cPtr == nil {
		return fmt.Errorf("SwrContext not initialized")
	}
	if ret := C.swr_set_compensation_helper(s.cPtr, C.int(sampleDelta), C.int(compensationDistance)); ret < 0 {
		return fmt.Errorf("swr_set_compensation failed: %d", ret)
	}
	return nil
}
