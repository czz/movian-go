package libav

/*
#include <stdint.h>
#include <stdlib.h>

// Forward declarations for C helper functions from spdif_helper.c
void *spdif_muxer_create(int codec_id, int sample_rate);
int spdif_mux_packet(void *fctx, uint8_t *data, int data_size);
int spdif_get_data(void *fctx, uint8_t **out_data);
void spdif_reset_data(void *fctx);
void spdif_muxer_destroy(void *fctx);
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// SPDIFMuxerContext represents FFmpeg SPDIF muxer context
type SPDIFMuxerContext struct {
	cPtr unsafe.Pointer
}

// SPDIFMuxerCreate creates a new SPDIF muxer context
func SPDIFMuxerCreate(codecID, sampleRate int) *SPDIFMuxerContext {
	ctx := C.spdif_muxer_create(C.int(codecID), C.int(sampleRate))
	if ctx == nil {
		return nil
	}
	return &SPDIFMuxerContext{cPtr: unsafe.Pointer(ctx)}
}

// MuxPacket muxes a packet through SPDIF
func (sm *SPDIFMuxerContext) MuxPacket(data []byte) error {
	if sm.cPtr == nil {
		return fmt.Errorf("SPDIF muxer not initialized")
	}
	if len(data) == 0 {
		return nil
	}

	ret := C.spdif_mux_packet(sm.cPtr, (*C.uint8_t)(unsafe.Pointer(&data[0])), C.int(len(data)))
	if ret < 0 {
		return fmt.Errorf("SPDIF mux failed: %d", ret)
	}
	return nil
}

// GetData returns the muxed data
func (sm *SPDIFMuxerContext) GetData() []byte {
	if sm.cPtr == nil {
		return nil
	}

	var cData *C.uint8_t
	size := C.spdif_get_data(sm.cPtr, &cData)
	if size <= 0 || cData == nil {
		return nil
	}

	// Convert C data to Go slice
	data := C.GoBytes(unsafe.Pointer(cData), C.int(size))
	return data
}

// ResetData resets the muxed data buffer
func (sm *SPDIFMuxerContext) ResetData() {
	if sm.cPtr != nil {
		C.spdif_reset_data(sm.cPtr)
	}
}

// Destroy destroys the SPDIF muxer context
func (sm *SPDIFMuxerContext) Destroy() {
	if sm.cPtr != nil {
		C.spdif_muxer_destroy(sm.cPtr)
		sm.cPtr = nil
	}
}
