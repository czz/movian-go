//go:build webpopup && cef

// webpopup_cef.go — THE webpopup backend: CEF (Chromium Embedded
// Framework) in windowless (off-screen) mode, on every desktop arch.
// CefRenderHandler::OnPaint delivers BGRA frames that the
// `websurface` GLW widget uploads as a texture — Movian's own
// window, focus and input system drive the page. Same seam the WPE
// backend used; the WPE/GTK/WebView2/WKWebView backends are gone.
//
// Threading: every CEF call runs on the process main thread (the
// CEF browser UI thread) inside WpMainCheck, which also pumps CEF's
// message loop via cef_do_message_loop_work
// (multi_threaded_message_loop is unsupported on Linux). The GLW
// side crosses through wpPlatformDispatch and the websurface hooks.
//
// Subprocesses: CEF re-execs this binary with --type=...; a C
// constructor (cef_ctor) intercepts that before the Go runtime
// starts, so Chromium's zygote sees a plain C process. The first
// CEF call must be cef_api_hash() (API versioning, CEF 133+);
// CefExecuteProcess in cmd/movian-go main() is the belt-and-suspenders
// backstop.
package ui

/*
#cgo pkg-config: cef
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdatomic.h>
#if defined(_WIN32)
#include <windows.h>
#include <process.h>
#else
#include <fcntl.h>
#include <unistd.h>
#endif
#if defined(__APPLE__)
#include <crt_externs.h>
#endif
#include <include/capi/cef_app_capi.h>
#include <include/capi/cef_client_capi.h>
#include <include/capi/cef_browser_capi.h>
#include <include/capi/cef_render_handler_capi.h>
#include <include/capi/cef_life_span_handler_capi.h>
#include <include/capi/cef_load_handler_capi.h>
#include <include/capi/cef_display_handler_capi.h>
#include <include/capi/cef_browser_process_handler_capi.h>
#include <include/capi/cef_cookie_capi.h>
#include <include/capi/cef_command_line_capi.h>
#include <include/cef_api_hash.h>

extern void cefOnContextInit(void);
extern void cefOnBrowserCreated(void *browser, void *data);
extern void cefOnPaint(void *browser, void *data, void *buf,
    int w, int h);
extern void cefOnLoadStart(void *data, char *url);
extern void cefOnLoadEnd(void *data);
extern void cefOnAddressChange(void *data, char *url);
extern void cefOnField(void *data, char *msg);
extern void cefOnConsole(void *data, char *msg);
extern void cefCookieVisit(char *name, char *value);
extern void cefCookiesDone(void *data);

// ------------------------------------------------------------------
// refcounted handler plumbing (C API objects must implement
// cef_base_ref_counted_t; base.size = sizeof(api struct), checked at
// wrap time by libcef).
// ------------------------------------------------------------------
// CEF_CALLBACK (= __stdcall on 32-bit Windows, empty elsewhere) is
// required on every function assigned into a cef_*_t vtable — without
// it mingw-386 rejects the assignment as an incompatible pointer type.
#define REFCOUNT_IMPL(prefix, type)                                        \
	typedef struct { type h; atomic_int rc; void *ud; } prefix##_t;    \
	static void CEF_CALLBACK prefix##_add_ref(                         \
	    cef_base_ref_counted_t *s) {                                   \
		atomic_fetch_add(&((prefix##_t *)s)->rc, 1);               \
	}                                                                \
	static int CEF_CALLBACK prefix##_release(                          \
	    cef_base_ref_counted_t *s) {                                   \
		return atomic_fetch_sub(&((prefix##_t *)s)->rc, 1) == 1;     \
	}                                                                \
	static int CEF_CALLBACK prefix##_has_one(                          \
	    cef_base_ref_counted_t *s) {                                   \
		return atomic_load(&((prefix##_t *)s)->rc) == 1;             \
	}                                                                \
	static int CEF_CALLBACK prefix##_has_atleast(                      \
	    cef_base_ref_counted_t *s) {                                   \
		return atomic_load(&((prefix##_t *)s)->rc) >= 1;             \
	}                                                                \
	static void prefix##_init(prefix##_t *o) {                       \
		memset(&o->h, 0, sizeof(o->h));                              \
		o->h.base.size = sizeof(type);                               \
		o->h.base.add_ref = prefix##_add_ref;                        \
		o->h.base.release = prefix##_release;                        \
		o->h.base.has_one_ref = prefix##_has_one;                    \
		o->h.base.has_at_least_one_ref = prefix##_has_atleast;       \
		atomic_init(&o->rc, 1);                                      \
	}

// ---- helpers ----
static void cef_go_str(char *dst, size_t cap, const cef_string_t *s) {
	cef_string_utf8_t u8 = {0};
	cef_string_to_utf8(s->str, s->length, &u8);
	if (u8.str) {
		strncpy(dst, u8.str, cap - 1);
		dst[cap - 1] = 0;
	} else {
		dst[0] = 0;
	}
	cef_string_utf8_clear(&u8);
}

static void cef_set_str(cef_string_t *dst, const char *s) {
	cef_string_from_utf8(s, strlen(s), (cef_string_utf16_t *)dst);
}

// ---- render handler: OnPaint → BGRA frame to Go ----
REFCOUNT_IMPL(rh, cef_render_handler_t)

static int g_viewsz[2] = {1280, 720};

static void CEF_CALLBACK rh_get_view_rect(cef_render_handler_t *self,
    cef_browser_t *b, cef_rect_t *rect) {
	rect->x = 0; rect->y = 0;
	rect->width = g_viewsz[0]; rect->height = g_viewsz[1];
}

static void CEF_CALLBACK rh_on_paint(cef_render_handler_t *self,
    cef_browser_t *b, cef_paint_element_type_t type, size_t n,
    const cef_rect_t *dr, const void *buf, int w, int h) {
	if (type != PET_VIEW)
		return;
	cefOnPaint(b, ((rh_t *)self)->ud, (void *)buf, w, h);
}

// ---- load handler: load-start URL → trap detection ----
REFCOUNT_IMPL(lh, cef_load_handler_t)

static void CEF_CALLBACK lh_on_load_start(cef_load_handler_t *self,
    cef_browser_t *b, cef_frame_t *f, cef_transition_type_t t) {
	if (!f->is_main(f))
		return;
	char url[2048];
	cef_go_str(url, sizeof url, f->get_url(f));
	cefOnLoadStart(((lh_t *)self)->ud, url);
}

// load-end → fresh DOM: (re)inject the focus tracker + spatial nav.
// CEF has no document-start user-script API in the browser process
// (that needs render-process plumbing), so hooks attach on load-end —
// fine for keydown/focusin which fire later anyway.
static void CEF_CALLBACK lh_on_load_end(cef_load_handler_t *self,
    cef_browser_t *b, cef_frame_t *f, int httpStatusCode) {
	if (!f->is_main(f))
		return;
	cefOnLoadEnd(((lh_t *)self)->ud);
}

// ---- display handler: address change + console bridge ----
// The injected focus tracker can't use script-message handlers like
// WPE's (CEF has no equivalent in the browser process without
// render-process plumbing) — it posts "MOVIANOSK:<m>" through
// console.log instead; on_console_message delivers it here.
REFCOUNT_IMPL(dh, cef_display_handler_t)

static void CEF_CALLBACK dh_on_address_change(
    cef_display_handler_t *self, cef_browser_t *b, cef_frame_t *f,
    const cef_string_t *url) {
	char u[2048];
	cef_go_str(u, sizeof u, url);
	cefOnAddressChange(((dh_t *)self)->ud, u);
}

static int CEF_CALLBACK dh_on_console_message(
    cef_display_handler_t *self, cef_browser_t *b,
    cef_log_severity_t level, const cef_string_t *message,
    const cef_string_t *source, int line) {
	char m[4096];
	cef_go_str(m, sizeof m, message);
	if (strncmp(m, "MOVIANOSK:", 10) == 0) {
		cefOnField(((dh_t *)self)->ud, m + 10);
		return 1;
	}
	cefOnConsole(((dh_t *)self)->ud, m);
	return 0;
}

// ---- life span: browser created → register session ----
REFCOUNT_IMPL(lsh, cef_life_span_handler_t)

static void CEF_CALLBACK lsh_on_after_created(
    cef_life_span_handler_t *self, cef_browser_t *b) {
	b->base.add_ref(&b->base);
	cefOnBrowserCreated(b, ((lsh_t *)self)->ud);
}

// ---- client ----
REFCOUNT_IMPL(cl, cef_client_t)

static cl_t g_client;

static cef_render_handler_t *CEF_CALLBACK cl_get_rh(cef_client_t *s) {
	static rh_t rh; static int done;
	if (!done) {
		rh_init(&rh);
		rh.h.get_view_rect = rh_get_view_rect;
		rh.h.on_paint = rh_on_paint;
		done = 1;
	}
	return &rh.h;
}
static cef_load_handler_t *CEF_CALLBACK cl_get_lh(cef_client_t *s) {
	static lh_t lh; static int done;
	if (!done) {
		lh_init(&lh);
		lh.h.on_load_start = lh_on_load_start;
		lh.h.on_load_end = lh_on_load_end;
		done = 1;
	}
	return &lh.h;
}
static cef_display_handler_t *CEF_CALLBACK cl_get_dh(cef_client_t *s) {
	static dh_t dh; static int done;
	if (!done) {
		dh_init(&dh);
		dh.h.on_address_change = dh_on_address_change;
		dh.h.on_console_message = dh_on_console_message;
		done = 1;
	}
	return &dh.h;
}
static cef_life_span_handler_t *CEF_CALLBACK cl_get_lsh(
    cef_client_t *s) {
	static lsh_t l; static int done;
	if (!done) {
		lsh_init(&l);
		l.h.on_after_created = lsh_on_after_created;
		done = 1;
	}
	return &l.h;
}

// ---- browser process handler + app ----
REFCOUNT_IMPL(bph, cef_browser_process_handler_t)
REFCOUNT_IMPL(app, cef_app_t)

static void CEF_CALLBACK bph_on_context_initialized(
    cef_browser_process_handler_t *self) {
	cefOnContextInit();
}

// cl_init_once — the client singleton must exist before
// cef_initialize (browsers may be created as soon as the context is
// up, before on_context_initialized is even observed).
static void cl_init_once(void) {
	static int done;
	if (done)
		return;
	done = 1;
	cl_init(&g_client);
	g_client.h.get_render_handler = cl_get_rh;
	g_client.h.get_load_handler = cl_get_lh;
	g_client.h.get_display_handler = cl_get_dh;
	g_client.h.get_life_span_handler = cl_get_lsh;
}

static cef_browser_process_handler_t *CEF_CALLBACK app_get_bph(
    cef_app_t *self) {
	static bph_t b; static int done;
	if (!done) {
		bph_init(&b);
		b.h.on_context_initialized = bph_on_context_initialized;
		done = 1;
	}
	return &b.h;
}

// Chromium first-run (EULA dialog RunLoop — hangs headless) and the
// GPU child need real command-line switches. On Linux they ride in
// argv (see cefArgv) because subprocesses launch before this hook
// runs; on Windows/macOS cef_main_args_t carries no argv, so the
// hook is the only channel — append there too (dupes are harmless).
static void CEF_CALLBACK app_before_cmdline(cef_app_t *self,
    const cef_string_t *ptype, cef_command_line_t *cmd) {
#if !defined(__linux__)
	static const char *sw[] = {
		"--no-first-run", "--no-default-browser-check",
		"--disable-gpu", "--disable-gpu-compositing",
		"--disable-component-update",
		"--disable-background-networking",
	};
	for (size_t i = 0; i < sizeof(sw) / sizeof(sw[0]); i++) {
		cef_string_t s = {0};
		cef_set_str(&s, sw[i]);
		cmd->append_switch(cmd, &s);
		cef_string_utf16_clear(&s);
	}
#endif
}

static app_t g_app;

// ------------------------------------------------------------------
// entry points
// ------------------------------------------------------------------
// cef_ctor — CEF subprocess interception BEFORE the Go runtime
// starts: C constructors run before _rt0 (ELF .init_array, mach-o
// mod_init_funcs, PE TLS/CRT init all precede Go startup) — meaning
// zero Go threads, signal handlers or stack guards exist yet, which
// is exactly what the Chromium zygote CHECKs for. If --type=... is
// present this process is a CEF worker and never returns.
__attribute__((constructor)) static void cef_ctor(void) {
#if defined(_WIN32)
	const char *cl = GetCommandLineA();
	if (cl == NULL || strstr(cl, "--type=") == NULL)
		return;
	cef_api_hash(CEF_API_VERSION, 0);
	cef_main_args_t args;
	args.instance = GetModuleHandle(NULL);
	int code = cef_execute_process(&args, NULL, NULL);
	_exit(code >= 0 ? code : 0);
#elif defined(__APPLE__)
	int argc = *_NSGetArgc();
	char **argv = *_NSGetArgv();
	int is_sub = 0;
	for (int i = 0; i < argc; i++)
		if (strncmp(argv[i], "--type=", 7) == 0)
			is_sub = 1;
	if (!is_sub)
		return;
	cef_api_hash(CEF_API_VERSION, 0);
	cef_main_args_t args = {argc, argv};
	int code = cef_execute_process(&args, NULL, NULL);
	_exit(code >= 0 ? code : 0);
#else
	int fd = open("/proc/self/cmdline", O_RDONLY);
	if (fd < 0)
		return;
	char buf[8192];
	ssize_t n = read(fd, buf, sizeof buf - 1);
	close(fd);
	if (n <= 0)
		return;
	buf[n] = 0;
	int is_sub = 0;
	for (ssize_t i = 0; i < n;) {
		char *a = buf + i;
		if (strncmp(a, "--type=", 7) == 0)
			is_sub = 1;
		i += strlen(a) + 1;
	}
	if (!is_sub)
		return;
	int argc = 0;
	for (ssize_t i = 0; i < n; i++)
		if (buf[i] == 0)
			argc++;
	char **argv = (char **)malloc(sizeof(char *) * argc);
	int k = 0;
	for (ssize_t i = 0; i < n;) {
		argv[k++] = buf + i;
		i += strlen(argv[k - 1]) + 1;
	}
	cef_api_hash(CEF_API_VERSION, 0);
	cef_main_args_t args = {argc, argv};
	int code = cef_execute_process(&args, NULL, NULL);
	_exit(code >= 0 ? code : 0);
#endif
}

static cef_main_args_t g_args;
static int g_have_args;

// cef_store_args — copy argv into C heap: g_args outlives the Go
// call frame (cef_initialize runs much later on the browser UI
// thread) and a Go-held argv would be garbage by then.
static void cef_store_args(int argc, char **argv) {
	if (g_have_args)
		return;
#if defined(_WIN32)
	// cef_main_args_t on Windows is { HINSTANCE } — no argv copy.
	g_args.instance = GetModuleHandle(NULL);
#else
	char **v = (char **)malloc(sizeof(char *) * argc);
	for (int i = 0; i < argc; i++)
		v[i] = strdup(argv[i]);
	g_args.argc = argc;
	g_args.argv = v;
#endif
	g_have_args = 1;
}

// cef_early — CefExecuteProcess: hash + execute_process. Returns the
// CEF exit code for subprocesses (caller exits), -1 otherwise.
static int cef_early(int argc, char **argv) {
	cef_api_hash(CEF_API_VERSION, 0);
	cef_store_args(argc, argv);
	cl_init_once();
	app_init(&g_app);
	g_app.h.get_browser_process_handler = app_get_bph;
	g_app.h.on_before_command_line_processing = app_before_cmdline;
	return cef_execute_process(&g_args, &g_app.h, NULL);
}

static int cef_init(const char *subproc, const char *resdir,
    const char *locdir, const char *cachedir) {
	cef_api_hash(CEF_API_VERSION, 0);
	cl_init_once();
	app_init(&g_app);
	g_app.h.get_browser_process_handler = app_get_bph;
	g_app.h.on_before_command_line_processing = app_before_cmdline;

	cef_settings_t s = {0};
	s.size = sizeof(s);
	s.no_sandbox = 1;
	s.windowless_rendering_enabled = 1;
	s.multi_threaded_message_loop = 0;
	s.log_severity = LOGSEVERITY_WARNING;
	if (subproc && *subproc)
		cef_set_str(&s.browser_subprocess_path, subproc);
	if (resdir && *resdir)
		cef_set_str(&s.resources_dir_path, resdir);
	if (locdir && *locdir)
		cef_set_str(&s.locales_dir_path, locdir);
	if (cachedir && *cachedir)
		cef_set_str(&s.root_cache_path, cachedir);
	return cef_initialize(&g_args, &s, &g_app.h, NULL);
}

static void cef_pump(void) { cef_do_message_loop_work(); }

// ------------------------------------------------------------------
// browser control (all on the CEF UI thread = our main thread)
// ------------------------------------------------------------------
static void *cef_browser_create(void *data, int w, int h,
    const char *uri) {
	cef_window_info_t wi = {0};
	wi.size = sizeof(wi);
	wi.windowless_rendering_enabled = 1;

	cef_browser_settings_t bs = {0};
	bs.size = sizeof(bs);
	bs.windowless_frame_rate = 60;
	bs.background_color = 0xffffffff;

	cef_string_t url = {0};
	cef_set_str(&url, uri);
	int ok = cef_browser_host_create_browser(&wi, &g_client.h, &url,
	    &bs, NULL, NULL);
	cef_string_utf16_clear(&url);
	return ok ? (void *)1 : NULL;
}

// session data → handlers (the client singleton carries it in ud;
// one popup at a time, same as the WPE exportable's single data ptr)
static void cef_set_ud(void *data) {
	((rh_t *)cl_get_rh(&g_client.h))->ud = data;
	((lh_t *)cl_get_lh(&g_client.h))->ud = data;
	((dh_t *)cl_get_dh(&g_client.h))->ud = data;
	((lsh_t *)cl_get_lsh(&g_client.h))->ud = data;
}

// Deferred close: the trap fires mid-navigation (provisional load
// IPC still in flight) — a synchronous close_browser there destroys
// the WebContents under the load's own dispatch → ~WebContentsImpl
// CHECK → SIGTRAP. Post the close to the UI task queue instead; it
// runs after the current navigation dispatch completes. Same fix as
// WPE's deferred view unref.
REFCOUNT_IMPL(ctask, cef_task_t)

static void CEF_CALLBACK ctask_exec(cef_task_t *s) {
	ctask_t *t = (ctask_t *)s;
	cef_browser_t *b = (cef_browser_t *)t->ud;
	cef_browser_host_t *h = b->get_host(b);
	h->close_browser(h, 1);
	b->base.release(&b->base);
}

static void cef_close(void *browser) {
	ctask_t *t = (ctask_t *)malloc(sizeof(ctask_t));
	ctask_init(t);
	t->h.execute = ctask_exec;
	t->ud = browser;
	cef_post_task(TID_UI, &t->h);
}

static char *cef_get_url(void *browser) {
	cef_browser_t *b = browser;
	cef_frame_t *f = b->get_main_frame(b);
	if (!f)
		return NULL;
	cef_string_userfree_t u = f->get_url(f);
	char *out = NULL;
	if (u) {
		cef_string_utf8_t u8 = {0};
		cef_string_to_utf8(u->str, u->length, &u8);
		if (u8.str)
			out = strdup(u8.str);
		cef_string_utf8_clear(&u8);
		cef_string_userfree_free(u);
	}
	return out;
}

static void cef_set_size(void *browser, int w, int h) {
	cef_browser_t *b = browser;
	g_viewsz[0] = w; g_viewsz[1] = h;
	b->get_host(b)->was_resized(b->get_host(b));
}

static void cef_focus(void *browser) {
	cef_browser_t *b = browser;
	b->get_host(b)->set_focus(b->get_host(b), 1);
}

static void cef_js(void *browser, const char *js) {
	cef_browser_t *b = browser;
	cef_frame_t *f = b->get_main_frame(b);
	if (!f)
		return;
	cef_string_t code = {0}, src = {0};
	cef_set_str(&code, js);
	f->execute_java_script(f, &code, &src, 0);
	cef_string_utf16_clear(&code);
}

// XKB keysym (the websurface's transport key space, shared with the
// WPE backend) → Chromium key event.
static int cef_vk(unsigned int ks) {
	switch (ks) {
	case 0xff08: return 8;   // BackSpace
	case 0xff09: return 9;   // Tab
	case 0xff0d: return 13;  // Return
	case 0xff1b: return 27;  // Escape
	case 0xffff: return 46;  // Delete
	case 0xff50: return 36;  // Home
	case 0xff51: return 37;  // Left
	case 0xff52: return 38;  // Up
	case 0xff53: return 39;  // Right
	case 0xff54: return 40;  // Down
	case 0xff55: return 33;  // PageUp
	case 0xff56: return 34;  // PageDown
	case 0xff57: return 35;  // End
	}
	return 0;
}

static void cef_key(void *browser, unsigned int ks) {
	int vk = cef_vk(ks);
	if (!vk)
		return;
	cef_browser_t *b = browser;
	cef_browser_host_t *h = b->get_host(b);
	cef_key_event_t ev = {0};
	ev.size = sizeof(ev);
	ev.windows_key_code = vk;
	ev.native_key_code = vk;
	ev.type = KEYEVENT_RAWKEYDOWN;
	h->send_key_event(h, &ev);
	if (vk == 13 || vk == 9) {
		// Chromium runs default actions (button activation, form
		// submit, tab-focus) on the keypress — i.e. the CHAR event —
		// not on rawkeydown. Return/Tab carry control chars.
		ev.type = KEYEVENT_CHAR;
		ev.character = vk;
		ev.unmodified_character = vk;
		h->send_key_event(h, &ev);
	}
	ev.type = KEYEVENT_KEYUP;
	h->send_key_event(h, &ev);
}

static void cef_text(void *browser, unsigned int cp) {
	cef_browser_t *b = browser;
	cef_browser_host_t *h = b->get_host(b);
	cef_key_event_t ev = {0};
	ev.size = sizeof(ev);
	ev.type = KEYEVENT_CHAR;
	ev.character = cp;
	ev.unmodified_character = cp;
	ev.windows_key_code = (int)cp;
	h->send_key_event(h, &ev);
}

static void cef_pointer(void *browser, int type, int x, int y,
    unsigned int button, unsigned int state, unsigned int mods) {
	cef_browser_t *b = browser;
	cef_browser_host_t *h = b->get_host(b);
	cef_mouse_event_t ev = {0};
	ev.x = x; ev.y = y;
	if (type == 1) { // motion
		h->send_mouse_move_event(h, &ev, 0);
	} else { // button
		cef_mouse_button_type_t bt =
		    button == 3 ? MBT_RIGHT : MBT_LEFT;
		h->send_mouse_click_event(h, &ev, bt, !state, 1);
	}
}

// injected user script — same focus tracker + spatial nav as the WPE
// backend, with console.log('MOVIANOSK:...') as the bridge instead
// of webkit.messageHandlers.
static void cef_inject_scripts(void *browser) {
	cef_browser_t *b = browser;
	cef_frame_t *f = b->get_main_frame(b);
	if (!f)
		return;
	static const char *js =
	 "window.addEventListener('focusin',function(e){"
	 "var t=e.target;if(!t||!t.tagName)return;"
	 "if(t.tagName=='TEXTAREA'||t.isContentEditable||"
	 "(t.tagName=='INPUT'&&!/^(checkbox|radio|submit|button|"
	 "image|reset|hidden|file|range|color)$/.test(t.type)))"
	 "console.log('MOVIANOSK:F'+"
	 "((t.type=='password')?'P':'')+(t.value||''));},true);"
	 "window.addEventListener('focusout',function(){"
	 "console.log('MOVIANOSK:B');},true);"
	 "window.addEventListener('keydown',function(e){"
	 "var ae=document.activeElement;"
	 "if(e.keyCode==13&&ae&&ae.tagName=='INPUT'&&"
	 "/^(checkbox|radio)$/.test(ae.type)){e.preventDefault();"
	 "ae.click();return;}"
	 "var d=({37:-1,38:-1,39:1,40:1})[e.keyCode];"
	 "var h=(e.keyCode==37||e.keyCode==39);"
	 "if(d===undefined)return;"
	 "var tf=ae&&(ae.tagName=='INPUT'&&"
	 "!/^(checkbox|radio|submit|button|image|reset|hidden|file|"
	 "range|color)$/.test(ae.type)||ae.tagName=='TEXTAREA'||"
	 "ae.isContentEditable);"
	 "if(tf&&h)return;"
	 "if(ae&&ae.tagName=='SELECT')return;"
	 "var r0=ae&&ae.getBoundingClientRect?"
	 "ae.getBoundingClientRect():null;"
	 "if(!r0||(!r0.width&&!r0.height))r0={left:innerWidth/2,"
	 "top:innerHeight/2,right:innerWidth/2,bottom:innerHeight/2,"
	 "width:0,height:0};"
	 "var best=null,bs=1e15;"
	 "var all=document.querySelectorAll('a[href],input,button,"
	 "select,textarea,[tabindex]');"
	 "for(var i=0;i<all.length;i++){var el=all[i];"
	 "if(el===ae||el.disabled||el.tabIndex<0)continue;"
	 "var r=el.getBoundingClientRect();"
	 "if(!r.width||!r.height)continue;"
	 "var pr,pe;"
	 "if(h){pr=d>0?r.left-r0.right:r0.left-r.right;"
	 "pe=Math.max(r0.top-r.bottom,r.top-r0.bottom,0);}"
	 "else{pr=d>0?r.top-r0.bottom:r0.top-r.bottom;"
	 "pe=Math.max(r0.left-r.right,r.left-r0.right,0);}"
	 "if(pr<0)continue;"
	 "var sc=pe*10+pr;"
	 "if(sc<bs){bs=sc;best=el;}}"
	 "if(best){e.preventDefault();best.focus();"
	 "if(best.scrollIntoView)"
	 "best.scrollIntoView({block:'nearest'});}"
	 "},true);";
	cef_string_t code = {0}, src = {0};
	cef_set_str(&code, js);
	f->execute_java_script(f, &code, &src, 0);
	cef_string_utf16_clear(&code);
}

// ---- cookies: global manager → visit_url_cookies ----
REFCOUNT_IMPL(cvis, cef_cookie_visitor_t)

static int CEF_CALLBACK cvis_visit(cef_cookie_visitor_t *self,
    const cef_cookie_t *c, int count, int total, int *del) {
	cvis_t *v = (cvis_t *)self;
	char name[256], value[1024];
	cef_go_str(name, sizeof name, &c->name);
	cef_go_str(value, sizeof value, &c->value);
	cefCookieVisit(name, value);
	if (count + 1 >= total) {
		cefCookiesDone(v->ud);
		return 0;
	}
	return 1;
}

static cvis_t g_cvis;

// cef_get_cookies — get_global_manager is synchronous (returns the
// manager, storage init is lazy); visit_url_cookies runs the visitor
// on the UI thread. An empty jar never calls visit → report done
// when visit_url_cookies returns with no visitor callback: the Go
// side treats "done without any visit" as an empty map.
static void cef_get_cookies(const char *uri, void *data) {
	cvis_init(&g_cvis);
	g_cvis.h.visit = cvis_visit;
	g_cvis.ud = data;
	cef_cookie_manager_t *mgr =
	    cef_cookie_manager_get_global_manager(NULL);
	if (!mgr) {
		cefCookiesDone(data);
		return;
	}
	cef_string_t u = {0};
	cef_set_str(&u, uri);
	mgr->visit_url_cookies(mgr, &u, 1, &g_cvis.h);
	cef_string_utf16_clear(&u);
}
*/
import "C"

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/trace"
)

var (
	cefMu       sync.Mutex
	cefJobs     []func()
	cefInit     = -1 // -1 unchecked, 0 failed, 1 ok
	cefCtxUp    bool
	cefActiveN  atomic.Int32
	cefConsoleN int

	cefCacheDir string

	// wpSurfaceGlueCef — the websurface glue seam (cef builds).
	wpSurfaceGlueCef struct {
		show func(h any) bool
		hide func()
	}
)

// SetWebsurfaceGlue — wired by cmd/movian-go (cef builds only).
func SetWebsurfaceGlue(show func(h any) bool, hide func()) {
	wpSurfaceGlueCef.show = show
	wpSurfaceGlueCef.hide = hide
}

// CefExecuteProcess — must run at the very top of main() before any
// other CEF call (and before Movian starts): if this process is a
// CEF subprocess (--type=...) it enters the CEF subprocess loop and
// returns its exit code (caller exits); returns -1 in the browser
// process. Wired by cmd/movian-go under the cef tag.
// cefArgv — os.Args plus the switches CEF needs. They ride inside
// the real argv (not on_before_command_line_processing) because the
// GPU-process launcher only honors actual command-line switches:
// appended ones still spawned a GPU process that FATALs headless.
func cefArgv() (argc C.int, argv **C.char) {
	args := append(append([]string{}, os.Args...),
		"--no-first-run", "--no-default-browser-check",
		"--disable-gpu", "--disable-gpu-compositing",
		"--disable-component-update",
		"--disable-background-networking")
	if runtime.GOOS == "linux" {
		// Windowless browser — CEF needs no X/wayland connection;
		// headless ozone also skips the GPU process entirely (an
		// X11 ozone attempt FATALs when the GPU child dies).
		args = append(args, "--ozone-platform=headless")
	}
	v := make([]*C.char, len(args))
	for i, a := range args {
		v[i] = C.CString(a)
	}
	return C.int(len(args)), &v[0]
}

func CefExecuteProcess() int {
	argc, argv := cefArgv()
	return int(C.cef_early(argc, argv))
}

// cefSession — one embedded CEF browser. The CEF side lives on the
// main thread (CEF UI thread); the frame slot is read by the UI
// thread's websurface widget. Implements uiglw.WebsurfaceHooks.
type cefSession struct {
	browser  unsafe.Pointer // cef_browser_t* (add_ref'd), main thread
	key      unsafe.Pointer // cefSessions map key (callback user data)
	trap     string
	wr       *WebpopupResult
	done     chan<- *WebpopupResult
	finished bool

	frameMu     sync.Mutex
	frame       []byte
	fw, fh      int
	frameStride int
	frameSeq    uint32

	oskMu       sync.Mutex
	oskField    bool
	oskPassword bool
	oskValue    string
}

var cefSessions sync.Map // unsafe.Pointer -> *cefSession

func cefSessionOf(data unsafe.Pointer) *cefSession {
	v, _ := cefSessions.Load(data)
	s, _ := v.(*cefSession)
	return s
}

// ------------------------------------------------------------------
// cgo callbacks (all on the CEF UI thread = process main thread)
// ------------------------------------------------------------------

//export cefOnContextInit
func cefOnContextInit() {
	cefCtxUp = true
}

//export cefOnBrowserCreated
func cefOnBrowserCreated(browser, data unsafe.Pointer) {
	s := cefSessionOf(data)
	if s == nil {
		return
	}
	s.browser = browser
	C.cef_focus(browser)
	webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
		"browser created id=%v", browser)
}

//export cefOnPaint
func cefOnPaint(browser, data, buf unsafe.Pointer, w, h C.int) {
	s := cefSessionOf(data)
	if s == nil || buf == nil {
		return
	}
	stride := int(w) * 4
	n := int(h) * stride
	s.frameMu.Lock()
	if cap(s.frame) < n {
		s.frame = make([]byte, n)
	} else {
		s.frame = s.frame[:n]
	}
	C.memcpy(unsafe.Pointer(&s.frame[0]), buf, C.size_t(n))
	if s.frameSeq == 0 {
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"first paint %dx%d", int(w), int(h))
	}
	s.fw, s.fh = int(w), int(h)
	s.frameStride = stride
	s.frameSeq++
	s.frameMu.Unlock()
}

//export cefOnLoadStart
func cefOnLoadStart(data unsafe.Pointer, url *C.char) {
	s := cefSessionOf(data)
	if s == nil {
		return
	}
	s.oskMu.Lock()
	s.oskField = false
	s.oskValue = ""
	s.oskMu.Unlock()
	s.checkTrapURI(C.GoString(url))
}

//export cefOnLoadEnd
func cefOnLoadEnd(data unsafe.Pointer) {
	s := cefSessionOf(data)
	if s != nil && s.browser != nil {
		// Fresh DOM — (re)attach the focus tracker + spatial nav.
		C.cef_inject_scripts(s.browser)
	}
}

//export cefOnAddressChange
func cefOnAddressChange(data unsafe.Pointer, url *C.char) {
	if s := cefSessionOf(data); s != nil {
		s.checkTrapURI(C.GoString(url))
	}
}

//export cefOnField
func cefOnField(data unsafe.Pointer, msg *C.char) {
	s := cefSessionOf(data)
	if s == nil {
		return
	}
	m := C.GoString(msg)
	s.oskMu.Lock()
	defer s.oskMu.Unlock()
	if m == "B" {
		s.oskField = false
		s.oskValue = ""
	} else if strings.HasPrefix(m, "F") {
		s.oskField = true
		s.oskPassword = len(m) > 1 && m[1] == 'P'
		s.oskValue = strings.TrimPrefix(m[1:], "P")
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"text field focused pw=%v", s.oskPassword)
	}
}

//export cefCookieVisit
func cefCookieVisit(name, value *C.char) {
	cefCookieResult[C.GoString(name)] = C.GoString(value)
}

var cefCookiesCh chan map[string]string
var cefCookieResult map[string]string

//export cefOnConsole
func cefOnConsole(data unsafe.Pointer, msg *C.char) {
	if cefConsoleN < 200 {
		cefConsoleN++
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"console: %s", C.GoString(msg))
	}
}

//export cefCookiesDone
func cefCookiesDone(data unsafe.Pointer) {
	cefActiveN.Add(-1)
	if cefCookiesCh != nil {
		cefCookiesCh <- cefCookieResult
	}
}

// ------------------------------------------------------------------
// session lifecycle (main thread)
// ------------------------------------------------------------------

func (s *cefSession) checkTrapURI(uri string) {
	if uri != "" && s.trap != "" && strings.HasPrefix(uri, s.trap) &&
		s.wr.Trapped.URL == "" {
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"Opening %s -- Final URI reached", uri)
		s.wr.Trapped.URL = uri
		s.finish(WebpopupTrappedURL)
	}
}

func (s *cefSession) finish(code int) {
	if s.finished {
		return
	}
	s.finished = true
	s.wr.ResultCode = code
	cefActiveN.Add(-1)
	if wpSurfaceGlueCef.hide != nil {
		wpSurfaceGlueCef.hide()
	}
	if s.browser != nil {
		C.cef_close(s.browser)
		s.browser = nil
	}
	if s.key != nil {
		cefSessions.Delete(s.key)
		s.key = nil
	}
	s.done <- s.wr
}

// ------------------------------------------------------------------
// WebsurfaceHooks — called by the GLW widget on the UI thread.
// ------------------------------------------------------------------

func (s *cefSession) Frame() (data []byte, w, h, stride int, seq uint32) {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	return s.frame, s.fw, s.fh, s.frameStride, s.frameSeq
}

func (s *cefSession) Text(r rune) {
	wpPlatformDispatch(func() {
		if s.browser != nil {
			C.cef_text(s.browser, C.uint(r))
		}
	})
}

// Key — XKB keysym (same transport space as the WPE backend).
func (s *cefSession) Key(keyCode uint32) {
	wpPlatformDispatch(func() {
		if s.browser != nil {
			C.cef_key(s.browser, C.uint(keyCode))
		}
	})
}

func (s *cefSession) Pointer(typ uint8, x, y int32, button, state,
	mods uint32) {
	wpPlatformDispatch(func() {
		if s.browser != nil {
			C.cef_pointer(s.browser, C.int(typ), C.int(x), C.int(y),
				C.uint(button), C.uint(state), C.uint(mods))
		}
	})
}

func (s *cefSession) SetSize(w, h int) {
	wpPlatformDispatch(func() {
		if s.browser != nil {
			C.cef_set_size(s.browser, C.int(w), C.int(h))
		}
	})
}

func (s *cefSession) OskField() (value string, password, ok bool) {
	s.oskMu.Lock()
	defer s.oskMu.Unlock()
	return s.oskValue, s.oskPassword, s.oskField
}

func (s *cefSession) SetText(str string) {
	js := "(()=>{var e=document.activeElement;" +
		"while(e&&e.tagName=='IFRAME'&&e.contentDocument)" +
		"{e=e.contentDocument.activeElement;}" +
		"if(!e||!('value' in e))return;" +
		"e.value=atob('" +
		base64.StdEncoding.EncodeToString([]byte(str)) + "');" +
		"e.dispatchEvent(new Event('input',{bubbles:true}));" +
		"e.dispatchEvent(new Event('change',{bubbles:true}));})()"
	wpPlatformDispatch(func() {
		if s.browser != nil {
			cjs := C.CString(js)
			defer C.free(unsafe.Pointer(cjs))
			C.cef_js(s.browser, cjs)
		}
	})
}

func (s *cefSession) CloseRequest() {
	wpPlatformDispatch(func() { s.finish(WebpopupClosedByUser) })
}

func (s *cefSession) Detached() {
	if !s.finished {
		wpPlatformDispatch(func() { s.finish(WebpopupClosedByUser) })
	}
}

// ------------------------------------------------------------------
// platform contract (same wp* names as the other backends —
// mutually exclusive via the cef build tag)
// ------------------------------------------------------------------

func cefEnsureInit() int {
	if cefInit == -1 {
		// CEF workers re-exec the same binary — the cgo C
		// constructor (cef_ctor) intercepts --type=... before the
		// Go runtime even starts, so the subprocess never touches
		// Go. MOVIANGO_CEF_SUBPROC overrides for exotic layouts.
		sub := os.Getenv("MOVIANGO_CEF_SUBPROC")
		if sub == "" {
			if exe, err := os.Executable(); err == nil {
				sub = exe
			}
			// else: leave empty — CEF defaults to the current
			// executable/module as the subprocess path.
		}
		cex := C.CString(sub)
		defer C.free(unsafe.Pointer(cex))
		cefdir := os.Getenv("CEF_DIR")
		var resdir, locdir string
		if cefdir != "" {
			if runtime.GOOS == "darwin" {
				// macOS dist: everything under the framework's
				// Resources dir.
				resdir = cefdir + "/Release/" +
					"Chromium Embedded Framework.framework/Resources"
				locdir = resdir + "/locales"
			} else {
				resdir = cefdir + "/Release"
				locdir = resdir + "/locales"
			}
		}
		if cefCacheDir == "" {
			// wpPlatformStorage arrives via dispatch after init —
			// root_cache_path must be decided here; fall back to
			// the user cache dir so cookies/storage still persist.
			if d, err := os.UserCacheDir(); err == nil {
				cefCacheDir = d + "/movian/cef"
				os.MkdirAll(cefCacheDir, 0755)
			}
		}
		cres := C.CString(resdir)
		defer C.free(unsafe.Pointer(cres))
		cloc := C.CString(locdir)
		defer C.free(unsafe.Pointer(cloc))
		ccache := C.CString(cefCacheDir)
		defer C.free(unsafe.Pointer(ccache))
		if C.cef_init(cex, cres, cloc, ccache) != 0 {
			cefInit = 1
		} else {
			cefInit = 0
		}
	}
	return cefInit
}

func wpPlatformSetup() {}

// wpPlatformOK reports availability without initializing: cef_init
// must stay lazy — it bakes root_cache_path, which wpPlatformStorage
// supplies via the dispatched job queue after this probe runs.
// A real failure still surfaces as WebpopupLoadError at first use.
func wpPlatformOK() bool { return cefInit != 0 }

// wpPlatformDispatch queues fn for the main thread and wakes the
// blocked mainloop courier.
func wpPlatformDispatch(fn func()) {
	cefMu.Lock()
	cefJobs = append(cefJobs, fn)
	cefMu.Unlock()
	if webpopupDeps.mainloopWake != nil {
		webpopupDeps.mainloopWake()
	}
}

// WpMainCheck — runs on the process main thread: job drain, CEF
// message-loop work. No eager init here — the dispatched jobs
// (storage dir first, then sessions) trigger cef_init lazily on
// this same thread, keeping initialize/pump thread-consistent.
func WpMainCheck() {
	for {
		cefMu.Lock()
		if len(cefJobs) == 0 {
			cefMu.Unlock()
			break
		}
		jobs := cefJobs
		cefJobs = nil
		cefMu.Unlock()
		for _, f := range jobs {
			f()
		}
	}
	if cefInit == 1 {
		C.cef_pump()
	}
}

// WpActive — bounded mainloop wait while browser work is alive.
func WpActive() bool {
	if cefActiveN.Load() > 0 {
		return true
	}
	cefMu.Lock()
	defer cefMu.Unlock()
	return len(cefJobs) > 0
}

// cefUAPlatform — UA token per host OS (wpLastUA reports what the
// webview pages ran with).
func cefUAPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows NT 10.0; Win64; x64"
	case "darwin":
		return "Macintosh; Intel Mac OS X 10_15_7"
	default:
		return "X11; Linux x86_64"
	}
}

// wpNewSession creates the windowless browser on the main thread and
// shows the websurface.
func wpNewSession(uri, trap string, wr *WebpopupResult,
	done chan<- *WebpopupResult) *cefSession {
	s := &cefSession{trap: trap, wr: wr, done: done}
	s.key = unsafe.Pointer(new(C.int))
	cefSessions.Store(s.key, s)
	C.cef_set_ud(s.key)
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	if C.cef_browser_create(s.key, 1280, 720, curi) == nil {
		s.finish(WebpopupLoadError)
		return s
	}
	cefActiveN.Add(1)

	wpLastUA = "Mozilla/5.0 (" + cefUAPlatform() + ") " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/cef Safari/537.36"

	if wpSurfaceGlueCef.show == nil || !wpSurfaceGlueCef.show(s) {
		s.finish(WebpopupLoadError)
		return s
	}
	return s
}

// wpPlatformRunPopup — runs on the main thread; completion arrives
// via trap or user close on this same context.
func wpPlatformRunPopup(uri, title, trap string, wr *WebpopupResult,
	done chan<- *WebpopupResult) {
	if cefEnsureInit() != 1 {
		wr.ResultCode = WebpopupLoadError
		done <- wr
		return
	}
	wpNewSession(uri, trap, wr, done)
}

// wpPlatformBrowser — detached browsing shares the embedded surface.
func wpPlatformBrowser(uri, title string) {
	if cefEnsureInit() != 1 {
		return
	}
	wr := &WebpopupResult{}
	done := make(chan *WebpopupResult, 1)
	wpNewSession(uri, "", wr, done)
	go func() { <-done }()
}

// wpPlatformStorage — persistent profile dir; becomes CEF's
// root_cache_path at init (cookies/storage persist automatically).
func wpPlatformStorage(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		webpopupDeps.ts.Trace(trace.TRACE_ERROR, "cef",
			"cookie storage dir %s: %v", dir, err)
	}
	cefCacheDir = dir
}

// wpPlatformCookies — cookie query via the global cookie manager.
func wpPlatformCookies(uri string, done chan map[string]string) {
	if cefEnsureInit() != 1 {
		done <- nil
		return
	}
	cefCookieResult = map[string]string{}
	cefCookiesCh = done
	cefActiveN.Add(1)
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	C.cef_get_cookies(curi, nil)
}
