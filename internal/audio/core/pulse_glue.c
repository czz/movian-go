//go:build linux && pulse

// Canonical pulseaudio.c globals + callbacks that need external
// linkage (cgo preambles are duplicated across generated TUs, so
// non-static definitions live here).
#include <pulse/pulseaudio.h>
#include <stdlib.h>

pa_threaded_mainloop *movian_pa_mainloop;
pa_mainloop_api *movian_pa_api;
pa_context *movian_pa_ctx;

extern void paContextErrorCB(char *msg);

// C: context_state_callback (pulseaudio.c:547-578). The FAILED arm's
// pulseaudio_error() (notify) is delegated to Go.
void movian_context_state_callback(pa_context *c, void *userdata) {
	pa_operation *o;
	switch(pa_context_get_state(c)) {
	case PA_CONTEXT_CONNECTING:
	case PA_CONTEXT_UNCONNECTED:
	case PA_CONTEXT_AUTHORIZING:
	case PA_CONTEXT_SETTING_NAME:
		break;
	case PA_CONTEXT_READY:
		o = pa_context_subscribe(c, PA_SUBSCRIPTION_MASK_SINK_INPUT, NULL, NULL);
		if(o != NULL) pa_operation_unref(o);
		pa_threaded_mainloop_signal(movian_pa_mainloop, 0);
		break;
	case PA_CONTEXT_TERMINATED:
		pa_threaded_mainloop_signal(movian_pa_mainloop, 0);
		break;
	case PA_CONTEXT_FAILED:
		paContextErrorCB((char*)pa_strerror(pa_context_errno(c)));
		pa_threaded_mainloop_signal(movian_pa_mainloop, 0);
		break;
	}
}
