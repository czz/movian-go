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
#include <stdint.h>
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
#include <include/capi/cef_devtools_message_observer_capi.h>
#include <include/capi/cef_registration_capi.h>
#include <include/capi/cef_parser_capi.h>
#include <include/capi/cef_command_line_capi.h>
#include <include/cef_api_hash.h>
#include <include/cef_version_info.h>

extern void cefOnContextInit(void);
extern void cefOnBrowserCreated(void *browser, void *data);
extern void cefOnPaint(void *browser, void *data, void *buf,
    int w, int h);
extern void cefOnLoadStart(void *data, char *url);
extern void cefOnLoadEnd(void *data);
extern void cefOnAddressChange(void *data, char *url, int is_main);
extern void cefOnField(void *data, char *msg);
extern void cefOnConsole(void *data, char *msg);
extern int cefCookieVisit(void *data, char *name, char *value,
    char *domain, char *path, int secure, int httponly, int has_expires,
    int64_t expires);
extern void cefCookiesDone(void *data);
extern void cefOnDevToolsResult(void *data, int id, int success,
    void *result, size_t n);
extern int cefCookieDelMatch(char *dom, char *path, char *name);
extern void cefCookiesDeleted(void *data, int n);

// ------------------------------------------------------------------
// refcounted handler plumbing (C API objects must implement
// cef_base_ref_counted_t; base.size = sizeof(api struct), checked at
// wrap time by libcef).
// ------------------------------------------------------------------
// CEF_CALLBACK (= __stdcall on 32-bit Windows, empty elsewhere) is
// required on every function assigned into a cef_*_t vtable — without
// it mingw-386 rejects the assignment as an incompatible pointer type.
#define REFCOUNT_IMPL_D(prefix, type, extra, destroy)                      \
	typedef struct { type h; atomic_int rc; extra } prefix##_t;        \
	static void CEF_CALLBACK prefix##_add_ref(                         \
	    cef_base_ref_counted_t *s) {                                   \
		atomic_fetch_add(&((prefix##_t *)s)->rc, 1);               \
	}                                                                \
	static int CEF_CALLBACK prefix##_release(                          \
	    cef_base_ref_counted_t *s) {                                   \
		if (atomic_fetch_sub(&((prefix##_t *)s)->rc, 1) == 1) {      \
			destroy(s);                                          \
			return 1;                                            \
		}                                                            \
		return 0;                                                    \
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

// Static objects (never freed): the release that reaches zero does nothing.
#define REFCOUNT_IMPL(prefix, type)                                        \
	static void prefix##_keep(void *o) { (void)o; }                    \
	REFCOUNT_IMPL_D(prefix, type, void *ud;, prefix##_keep)

// ---- per-browser session token ----
// The user data of every callback of one browser: C memory, immutable,
// reference counted. The Go side holds one reference (dropped when the
// session finishes) and each per-browser handler holds one, so the token
// is freed exactly once, after the last object that can deliver a callback
// carrying it has been released. No Go pointer is ever stored in C.
typedef struct { atomic_int rc; } stok_t;
static atomic_long g_stok_made, g_stok_freed;

static void *stok_new(void) {
	stok_t *t = (stok_t *)malloc(sizeof(stok_t));
	atomic_init(&t->rc, 1);
	atomic_fetch_add(&g_stok_made, 1);
	return t;
}
static void stok_retain(void *p) {
	atomic_fetch_add(&((stok_t *)p)->rc, 1);
}
static void stok_release(void *p) {
	if (atomic_fetch_sub(&((stok_t *)p)->rc, 1) == 1) {
		atomic_fetch_add(&g_stok_freed, 1);
		free(p);
	}
}
static long stok_made(void) { return atomic_load(&g_stok_made); }
static long stok_freed(void) { return atomic_load(&g_stok_freed); }

// Per-browser handler: holds a token reference, freed at its last release.
#define HANDLER_IMPL(prefix, type)                                         \
	static void prefix##_destroy(void *o);                             \
	REFCOUNT_IMPL_D(prefix, type, void *ud;, prefix##_destroy)         \
	static void prefix##_destroy(void *o) {                            \
		stok_release(((prefix##_t *)o)->ud);                       \
		free(o);                                                   \
	}                                                                \
	static prefix##_t *prefix##_new(void *tok) {                       \
		prefix##_t *o = (prefix##_t *)malloc(sizeof(prefix##_t));  \
		prefix##_init(o);                                          \
		o->ud = tok;                                               \
		stok_retain(tok);                                          \
		return o;                                                  \
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
HANDLER_IMPL(rh, cef_render_handler_t)

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
HANDLER_IMPL(lh, cef_load_handler_t)

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
HANDLER_IMPL(dh, cef_display_handler_t)

static void CEF_CALLBACK dh_on_address_change(
    cef_display_handler_t *self, cef_browser_t *b, cef_frame_t *f,
    const cef_string_t *url) {
	char u[2048];
	cef_go_str(u, sizeof u, url);
	cefOnAddressChange(((dh_t *)self)->ud, u, f ? f->is_main(f) : 0);
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
HANDLER_IMPL(lsh, cef_life_span_handler_t)

static void CEF_CALLBACK lsh_on_after_created(
    cef_life_span_handler_t *self, cef_browser_t *b) {
	b->base.add_ref(&b->base);
	cefOnBrowserCreated(b, ((lsh_t *)self)->ud);
}

// ---- client ----
// One client per browser, owning one reference to each of its handlers.
// Its getters hand out an extra reference (the CEF contract: the caller
// releases what a getter returns), so a handler can outlive the client.
static void cl_destroy(void *o);
REFCOUNT_IMPL_D(cl, cef_client_t,
    rh_t *rh; lh_t *lh; dh_t *dh; lsh_t *lsh;, cl_destroy)

static void cl_destroy(void *o) {
	cl_t *c = (cl_t *)o;
	c->rh->h.base.release(&c->rh->h.base);
	c->lh->h.base.release(&c->lh->h.base);
	c->dh->h.base.release(&c->dh->h.base);
	c->lsh->h.base.release(&c->lsh->h.base);
	free(c);
}

static cef_render_handler_t *CEF_CALLBACK cl_get_rh(cef_client_t *s) {
	rh_t *h = ((cl_t *)s)->rh;
	h->h.base.add_ref(&h->h.base);
	return &h->h;
}
static cef_load_handler_t *CEF_CALLBACK cl_get_lh(cef_client_t *s) {
	lh_t *h = ((cl_t *)s)->lh;
	h->h.base.add_ref(&h->h.base);
	return &h->h;
}
static cef_display_handler_t *CEF_CALLBACK cl_get_dh(cef_client_t *s) {
	dh_t *h = ((cl_t *)s)->dh;
	h->h.base.add_ref(&h->h.base);
	return &h->h;
}
static cef_life_span_handler_t *CEF_CALLBACK cl_get_lsh(
    cef_client_t *s) {
	lsh_t *h = ((cl_t *)s)->lsh;
	h->h.base.add_ref(&h->h.base);
	return &h->h;
}

// cl_new — a client with its own handler instances, all carrying tok.
// Returns the single reference, which create_browser consumes.
static cl_t *cl_new(void *tok) {
	cl_t *c = (cl_t *)malloc(sizeof(cl_t));
	cl_init(c);
	c->rh = rh_new(tok);
	c->rh->h.get_view_rect = rh_get_view_rect;
	c->rh->h.on_paint = rh_on_paint;
	c->lh = lh_new(tok);
	c->lh->h.on_load_start = lh_on_load_start;
	c->lh->h.on_load_end = lh_on_load_end;
	c->dh = dh_new(tok);
	c->dh->h.on_address_change = dh_on_address_change;
	c->dh->h.on_console_message = dh_on_console_message;
	c->lsh = lsh_new(tok);
	c->lsh->h.on_after_created = lsh_on_after_created;
	c->h.get_render_handler = cl_get_rh;
	c->h.get_load_handler = cl_get_lh;
	c->h.get_display_handler = cl_get_dh;
	c->h.get_life_span_handler = cl_get_lsh;
	return c;
}

// ---- browser process handler + app ----
REFCOUNT_IMPL(bph, cef_browser_process_handler_t)
REFCOUNT_IMPL(app, cef_app_t)

static void CEF_CALLBACK bph_on_context_initialized(
    cef_browser_process_handler_t *self) {
	cefOnContextInit();
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
	app_init(&g_app);
	g_app.h.get_browser_process_handler = app_get_bph;
	g_app.h.on_before_command_line_processing = app_before_cmdline;
	return cef_execute_process(&g_args, &g_app.h, NULL);
}

static int cef_init(const char *subproc, const char *resdir,
    const char *locdir, const char *cachedir) {
	cef_api_hash(CEF_API_VERSION, 0);
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
	cl_t *c = cl_new(data);
	int ok = cef_browser_host_create_browser(&wi, &c->h, &url,
	    &bs, NULL, NULL);
	// cef_browser_host_create_browser consumes the caller's reference to
	// the client (libcef wraps it without add_ref and releases it when the
	// browser is gone, or at once if creation fails), so it is not
	// released here: the client, its handlers and their token references
	// are freed by CEF's last release.
	cef_string_utf16_clear(&url);
	return ok ? (void *)1 : NULL;
}

// ---- DevTools: Network.getCookies results ----
// The visitor cannot return Partitioned cookies (cef_cookie_t has no
// partition field); DevTools reports their partition key. One observer per
// browser carries the session token like the other handlers; only method
// results are used (the Go side matches them by message id).
HANDLER_IMPL(dto, cef_dev_tools_message_observer_t)

static void CEF_CALLBACK dto_on_result(
    cef_dev_tools_message_observer_t *self, cef_browser_t *b,
    int id, int success, const void *result, size_t n) {
	cefOnDevToolsResult(((dto_t *)self)->ud, id, success,
	    (void *)result, n);
}

// cef_dt_attach registers a new observer for the browser and returns the
// registration (the caller releases it with cef_dt_detach), or NULL.
// Passing the observer to CEF transfers the one reference dto_new created,
// whatever the call returns (the CefCppToCRefCounted::Wrap rule described at
// cef_get_cookies), so it is not released here. Releasing the registration
// makes CEF drop its reference to the observer, which then releases the
// token reference.
static void *cef_dt_attach(void *browser, void *tok) {
	cef_browser_t *b = browser;
	cef_browser_host_t *h = b->get_host(b);
	if (!h)
		return NULL;
	dto_t *o = dto_new(tok);
	o->h.on_dev_tools_method_result = dto_on_result;
	cef_registration_t *r = h->add_dev_tools_message_observer(h, &o->h);
	h->base.release(&h->base);
	return r;
}

static void cef_dt_detach(void *reg) {
	cef_registration_t *r = reg;
	r->base.release(&r->base);
}

// cef_dt_send submits a DevTools message (UI thread only); 1 if accepted.
static int cef_dt_send(void *browser, const char *msg, size_t n) {
	cef_browser_t *b = browser;
	cef_browser_host_t *h = b->get_host(b);
	if (!h)
		return 0;
	int ok = h->send_dev_tools_message(h, msg, n);
	h->base.release(&h->base);
	return ok;
}

// ---- test seam: drive the real client and handlers without libcef ----
// The browser is a stub whose add_ref does nothing; callbacks are delivered
// through the objects cl_new built, exactly as CEF would.
static void CEF_CALLBACK tbr_add_ref(cef_base_ref_counted_t *s) { (void)s; }

// The stub host records DevTools traffic like libcef would: it keeps the
// observer it was given (and the reference that came with it) until the
// registration is released, then drops that reference.
typedef struct { cef_registration_t r; void *host; int released; } treg_t;
typedef struct {
	cef_browser_host_t h;
	cef_dev_tools_message_observer_t *obs;
	treg_t reg;
	char sent[512];
	int nsent, attached;
} thost_t;
typedef struct { cef_browser_t b; thost_t host; } tbrowser_t;

static int CEF_CALLBACK tbr_release_noop(cef_base_ref_counted_t *s) {
	(void)s; return 0;
}
static int CEF_CALLBACK treg_release(cef_base_ref_counted_t *s) {
	treg_t *r = (treg_t *)s;
	thost_t *h = (thost_t *)r->host;
	if (!r->released && h->obs) {
		r->released = 1;
		cef_dev_tools_message_observer_t *o = h->obs;
		h->obs = NULL;
		o->base.release(&o->base);
	}
	return 0;
}
static cef_registration_t *CEF_CALLBACK thost_add_observer(
    cef_browser_host_t *self,
    cef_dev_tools_message_observer_t *obs) {
	thost_t *h = (thost_t *)self;
	h->obs = obs;
	h->attached++;
	h->reg.released = 0;
	return &h->reg.r;
}
static int CEF_CALLBACK thost_send(cef_browser_host_t *self,
    const void *msg, size_t n) {
	thost_t *h = (thost_t *)self;
	if (n >= sizeof h->sent)
		n = sizeof h->sent - 1;
	memcpy(h->sent, msg, n);
	h->sent[n] = 0;
	h->nsent++;
	return 1;
}
static cef_browser_host_t *CEF_CALLBACK tbr_get_host(cef_browser_t *self) {
	return &((tbrowser_t *)self)->host.h;
}
static void *cef_test_browser(void) {
	tbrowser_t *t = (tbrowser_t *)calloc(1, sizeof(tbrowser_t));
	t->b.base.size = sizeof(cef_browser_t);
	t->b.base.add_ref = tbr_add_ref;
	t->b.get_host = tbr_get_host;
	t->host.h.base.size = sizeof(cef_browser_host_t);
	t->host.h.base.add_ref = tbr_add_ref;
	t->host.h.base.release = tbr_release_noop;
	t->host.h.add_dev_tools_message_observer = thost_add_observer;
	t->host.h.send_dev_tools_message = thost_send;
	t->host.reg.r.base.size = sizeof(cef_registration_t);
	t->host.reg.r.base.add_ref = tbr_add_ref;
	t->host.reg.r.base.release = treg_release;
	t->host.reg.host = &t->host;
	return t;
}
static int cef_test_dt_attached(void *browser) {
	return ((tbrowser_t *)browser)->host.attached;
}
static int cef_test_dt_observer_live(void *browser) {
	return ((tbrowser_t *)browser)->host.obs != NULL;
}
static const char *cef_test_dt_sent(void *browser, int *n) {
	thost_t *h = &((tbrowser_t *)browser)->host;
	*n = h->nsent;
	return h->sent;
}
// cef_test_dt_deliver hands a method result to the registered observer, as
// libcef does on the UI thread; 0 if no observer is registered.
static int cef_test_dt_deliver(void *browser, int id, int success,
    const char *json) {
	cef_dev_tools_message_observer_t *o = ((tbrowser_t *)browser)->host.obs;
	if (!o)
		return 0;
	o->on_dev_tools_method_result(o, (cef_browser_t *)browser, id, success,
	    json, strlen(json));
	return 1;
}
static void *cef_test_client(void *tok) { return cl_new(tok); }
static void cef_test_after_created(void *client, void *browser) {
	cef_client_t *c = (cef_client_t *)client;
	cef_life_span_handler_t *l = c->get_life_span_handler(c);
	l->on_after_created(l, (cef_browser_t *)browser);
	l->base.release(&l->base);
}
static void cef_test_paint(void *client, void *browser, void *buf,
    int w, int h) {
	cef_client_t *c = (cef_client_t *)client;
	cef_render_handler_t *r = c->get_render_handler(c);
	r->on_paint(r, (cef_browser_t *)browser, PET_VIEW, 0, NULL, buf, w, h);
	r->base.release(&r->base);
}
static void cef_test_client_release(void *client) {
	cef_client_t *c = (cef_client_t *)client;
	c->base.release(&c->base);
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

// cef_go_dup — a malloc'd UTF-8 copy of s, whatever its length (a fixed
// buffer cut long values silently); free() it. NULL when malloc fails.
static char *cef_go_dup(const cef_string_t *s) {
	cef_string_utf8_t u8 = {0};
	cef_string_to_utf8(s->str, s->length, &u8);
	char *out = malloc(u8.length + 1);
	if (out) {
		if (u8.str)
			memcpy(out, u8.str, u8.length);
		out[u8.length] = 0;
	}
	cef_string_utf8_clear(&u8);
	return out;
}

// cef_canon_url — the scheme, host and path of uri as cef_parse_url
// canonicalizes them (an IPv6 host comes bracketed), each malloc'd: free
// them. Returns 0, with all three NULL, when CEF rejects the URL or a copy
// fails.
static int cef_canon_url(const char *uri, char **scheme, char **host,
    char **path) {
	cef_string_t s = {0};
	cef_urlparts_t p = {0};
	cef_set_str(&s, uri);
	p.size = sizeof(p);
	int ok = cef_parse_url(&s, &p);
	*scheme = *host = *path = NULL;
	if (ok) {
		*scheme = cef_go_dup(&p.scheme);
		*host = cef_go_dup(&p.host);
		*path = cef_go_dup(&p.path);
		if (!*scheme || !*host || !*path) {
			free(*scheme);
			free(*host);
			free(*path);
			*scheme = *host = *path = NULL;
			ok = 0;
		}
	}
	cef_string_utf16_clear(&s);
	cef_string_utf16_clear(&p.spec);
	cef_string_utf16_clear(&p.scheme);
	cef_string_utf16_clear(&p.username);
	cef_string_utf16_clear(&p.password);
	cef_string_utf16_clear(&p.host);
	cef_string_utf16_clear(&p.port);
	cef_string_utf16_clear(&p.origin);
	cef_string_utf16_clear(&p.path);
	cef_string_utf16_clear(&p.query);
	cef_string_utf16_clear(&p.fragment);
	return ok;
}

// ---- cookies: global manager → visit_url_cookies ----
// webcookies() and the popup cookie wait read the URL-scoped visitor ONLY.
// CEF 152's cef_cookie_t carries no partition key, and visit_all_cookies
// would hand over cookies of every CHIPS partition indistinguishably;
// visit_url_cookies leaves partitioned cookies out. Do not switch this to
// visit_all_cookies.
// One visitor per webcookies() call, on the heap: CEF holds it until the
// visit is over, and its last release frees it. ud is the call's key in
// cookieQueries: a C-owned token (malloc'd by Go, never Go memory, since
// the visitor keeps it after the cgo call returned). The visitor owns the
// token from cef_get_cookies on and frees it exactly once, at its final
// release, after the last cefCookiesDone. cefCookiesDone may be reached
// several times per call; Go acts on the first only.
typedef struct {
	cef_cookie_visitor_t h;
	atomic_int rc;
	void *ud;
} cvis_t;

static void CEF_CALLBACK cvis_add_ref(cef_base_ref_counted_t *s) {
	atomic_fetch_add(&((cvis_t *)s)->rc, 1);
}

static int CEF_CALLBACK cvis_release(cef_base_ref_counted_t *s) {
	cvis_t *v = (cvis_t *)s;
	if (atomic_fetch_sub(&v->rc, 1) != 1)
		return 0;
	// An empty jar is never visited: CEF's release is the done signal then.
	cefCookiesDone(v->ud);
	free(v->ud);
	free(v);
	return 1;
}

static int CEF_CALLBACK cvis_has_one(cef_base_ref_counted_t *s) {
	return atomic_load(&((cvis_t *)s)->rc) == 1;
}

static int CEF_CALLBACK cvis_has_atleast(cef_base_ref_counted_t *s) {
	return atomic_load(&((cvis_t *)s)->rc) >= 1;
}

static int CEF_CALLBACK cvis_visit(cef_cookie_visitor_t *self,
    const cef_cookie_t *c, int count, int total, int *del) {
	cvis_t *v = (cvis_t *)self;
	char *name = cef_go_dup(&c->name), *value = cef_go_dup(&c->value);
	char *domain = cef_go_dup(&c->domain), *path = cef_go_dup(&c->path);
	// A cookie whose copy failed (out of memory) is left out of the result.
	// Go answers 0 once the query is cancelled: stop visiting.
	int keep_going = 1;
	if (name && value && domain && path)
		keep_going = cefCookieVisit(v->ud, name, value, domain, path,
		    c->secure, c->httponly, c->has_expires,
		    c->has_expires ? (int64_t)c->expires.val : 0);
	free(name);
	free(value);
	free(domain);
	free(path);
	if (!keep_going)
		return 0;
	if (count + 1 >= total) {
		cefCookiesDone(v->ud);
		return 0;
	}
	return 1;
}

// cvis_new — the visitor with one reference, the caller's; NULL when
// malloc fails.
static cvis_t *cvis_new(void *ud) {
	cvis_t *v = (cvis_t *)calloc(1, sizeof(cvis_t));
	if (!v)
		return NULL;
	v->h.base.size = sizeof(cef_cookie_visitor_t);
	v->h.base.add_ref = cvis_add_ref;
	v->h.base.release = cvis_release;
	v->h.base.has_one_ref = cvis_has_one;
	v->h.base.has_at_least_one_ref = cvis_has_atleast;
	v->h.visit = cvis_visit;
	atomic_init(&v->rc, 1);
	v->ud = ud;
	return v;
}

// cef_get_cookies — visit_url_cookies (HttpOnly included) on the UI thread
// for the already-canonicalized uri. The done signal (cefCookiesDone) comes
// on the last visited cookie; an empty jar is never visited, so it comes
// when CEF releases the visitor; a visit that returns false reports at once.
static void cef_get_cookies(const char *uri, void *ud) {
	cvis_t *v = cvis_new(ud);
	if (!v) {
		cefCookiesDone(ud);
		free(ud); // no visitor took the token over
		return;
	}
	cef_cookie_manager_t *mgr =
	    cef_cookie_manager_get_global_manager(NULL);
	if (!mgr) {
		// Not handed to CEF: this release of the caller's reference is
		// the last one (it reports done and frees visitor and token).
		v->h.base.release(&v->h.base);
		return;
	}
	cef_string_t u = {0};
	cef_set_str(&u, uri);
	// Ownership: passing a client-allocated ref-counted struct to CEF
	// TRANSFERS one reference. libcef's CefCppToCRefCounted::Wrap
	// (third_party/cef/linux64/libcef_dll/cpptoc/cpptoc_ref_counted.h:
	// 30-37) adds "a reference ... that will be released once our
	// structure arrives on the other side", and CefCToCppRefCounted::Wrap
	// (ctocpp/ctocpp_ref_counted.h:134-142) releases it into a CefRefPtr
	// on arrival; cookie_manager_ctocpp.cc:88-91 passes the wrapped
	// visitor straight into visit_url_cookies. So the one reference
	// cvis_new created is consumed by this call whatever it returns
	// (on false, CEF's CefRefPtr releases it as it goes out of scope).
	// The caller must NOT release after the call, or the count can hit 0
	// while CEF still holds the visitor. Only a visitor never handed to
	// CEF (manager unavailable, above) is released here.
	int started = mgr->visit_url_cookies(mgr, &u, 1, &v->h);
	cef_string_utf16_clear(&u);
	if (!started)
		cefCookiesDone(ud);
}

// ---- cookie deletion: visit_all_cookies + *del flag + flush ----
// visit_all (not visit_url) so partitioned cookies are covered too —
// the Go side decides each cookie via cefCookieDelMatch. flush_store
// pushes the deletions to disk so they survive restarts.
REFCOUNT_IMPL(cdel, cef_cookie_visitor_t)

static cdel_t g_cdel;
static int g_cdel_n;
static cef_cookie_manager_t *g_cdel_mgr;

static int CEF_CALLBACK cdel_visit(cef_cookie_visitor_t *self,
    const cef_cookie_t *c, int count, int total, int *del) {
	cdel_t *v = (cdel_t *)self;
	char dom[256], path[1024], name[256];
	cef_go_str(dom, sizeof dom, &c->domain);
	cef_go_str(path, sizeof path, &c->path);
	cef_go_str(name, sizeof name, &c->name);
	if (cefCookieDelMatch(dom, path, name)) {
		*del = 1;
		g_cdel_n++;
	}
	if (count + 1 >= total) {
		if (g_cdel_n > 0 && g_cdel_mgr->flush_store) {
			g_cdel_mgr->flush_store(g_cdel_mgr, NULL);
		}
		cefCookiesDeleted(v->ud, g_cdel_n);
		return 0;
	}
	return 1;
}

// cef_delete_cookies — like cef_get_cookies: runs via the dispatched
// job queue on the CEF main thread. An empty jar never fires visit →
// the Go caller falls back to its wait timeout.
static void cef_delete_cookies(void *data) {
	cdel_init(&g_cdel);
	g_cdel.h.visit = cdel_visit;
	g_cdel.ud = data;
	g_cdel_n = 0;
	g_cdel_mgr = cef_cookie_manager_get_global_manager(NULL);
	if (!g_cdel_mgr ||
	    !g_cdel_mgr->visit_all_cookies(g_cdel_mgr, &g_cdel.h)) {
		cefCookiesDeleted(data, 0);
	}
}
*/
import "C"

import (
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
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
	key      unsafe.Pointer // session token (C memory): map key and callback user data
	trap     string
	wr       *WebpopupResult
	done     chan<- *WebpopupResult
	finished bool
	// topURL is the https page the main frame shows now, "" while it is at
	// a non-https address or has not navigated yet (UI thread only); the
	// popup's owner may read the jar for its origin afterwards.
	topURL string
	// ownerGen is this popup's generation in cefOwnerCtx: a popup that a
	// newer one of the same owner superseded records nothing.
	ownerGen uint64

	// popup cookie wait (nil wait = none); UI thread only except waiter
	wait               *WebpopupCookieWait
	waiter             *cookieWaiter
	cookieQuery        *cookieQuery // read in flight, if any
	cookieCancel       func()       // detaches it
	cookieCheckPending bool         // another check came while one ran

	// DevTools read of Partitioned cookies, the wait's second source (UI
	// thread only). dtReg is the observer registration, released in finish
	// before the browser is closed.
	dtReg        unsafe.Pointer
	dt           devtoolsRead
	dtNextID     int
	dtLastReason string
	dtLastList   string
	// fpCookie is the first-party Partitioned cookie that completed the wait.
	// It is never held by itself: only the final inventory is evidence that
	// the cookie still exists when the popup ends.
	fpCookie *WebpopupCookie
	// closing: the popup ended and its browser stays alive until the end
	// read answers or endTimer fires. endCode is the result that ended it,
	// endSet what the read held, hidden that the surface is already off.
	closing  bool
	endCode  int
	endSet   firstPartySet
	endTimer *time.Timer
	hidden   bool
	del      *cefDeleteJob // a helper browser that only deletes cookies
	// visitorWaiting: the visitor found the awaited cookie and a DevTools
	// read is out to rule out a Partitioned twin before the wait completes.
	// dtUnanswered is when such reads began going unanswered (zero when the
	// last one was answered): past devtoolsReadLimit the visitor stands.
	visitorWaiting bool
	dtUnanswered   time.Time

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

// cefOwnerCtx says which plugin may read the shared CEF jar for which origin.
var cefOwnerCtx popupOwnerContexts

var cefSessions sync.Map // unsafe.Pointer -> *cefSession

// Session token wrappers (C memory, reference counted; see stok_t). The
// session holds one reference until finish(); each per-browser handler
// holds another. Test seams reach the C side only through these.
func cefTokenNew() unsafe.Pointer      { return C.stok_new() }
func cefTokenRetain(k unsafe.Pointer)  { C.stok_retain(k) }
func cefTokenRelease(k unsafe.Pointer) { C.stok_release(k) }
func cefTokensFreed() int64            { return int64(C.stok_freed()) }
func cefTokensMade() int64             { return int64(C.stok_made()) }

// Test seam wrappers (see the cef_test_* helpers in the C preamble).
func cefTestBrowser() unsafe.Pointer                { return C.cef_test_browser() }
func cefTestClient(k unsafe.Pointer) unsafe.Pointer { return C.cef_test_client(k) }
func cefTestClientRelease(c unsafe.Pointer)         { C.cef_test_client_release(c) }
func cefTestAfterCreated(c, b unsafe.Pointer)       { C.cef_test_after_created(c, b) }
func cefTestPaint(c, b unsafe.Pointer, buf []byte, w, h int) {
	C.cef_test_paint(c, b, unsafe.Pointer(&buf[0]), C.int(w), C.int(h))
}

func cefTestDevToolsAttached(b unsafe.Pointer) int { return int(C.cef_test_dt_attached(b)) }
func cefTestDevToolsObserverLive(b unsafe.Pointer) bool {
	return C.cef_test_dt_observer_live(b) != 0
}
func cefTestDevToolsSent(b unsafe.Pointer) (msg string, n int) {
	var cn C.int
	m := C.GoString(C.cef_test_dt_sent(b, &cn))
	return m, int(cn)
}
func cefTestDevToolsDeliver(b unsafe.Pointer, id int, ok bool, json string) bool {
	cs := C.CString(json)
	defer C.free(unsafe.Pointer(cs))
	success := C.int(0)
	if ok {
		success = 1
	}
	return C.cef_test_dt_deliver(b, C.int(id), success, cs) != 0
}

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

// cefCloseBrowser — releases a browser the session no longer owns; a seam
// for the late-creation test.
var cefCloseBrowser = func(browser unsafe.Pointer) { C.cef_close(browser) }

// DevTools seams: the C calls behind the wait's Partitioned-cookie read, so
// ordering tests need no real browser.
var (
	cefDevToolsAttach = func(browser, tok unsafe.Pointer) unsafe.Pointer {
		return C.cef_dt_attach(browser, tok)
	}
	cefDevToolsDetach = func(reg unsafe.Pointer) { C.cef_dt_detach(reg) }
	cefDevToolsSend   = func(browser unsafe.Pointer, msg string) bool {
		cmsg := C.CString(msg)
		defer C.free(unsafe.Pointer(cmsg))
		return C.cef_dt_send(browser, cmsg, C.size_t(len(msg))) != 0
	}
)

// devtoolsResultLimit caps the copy of one DevTools result. Storage.getCookies
// lists the whole browser context, so the limit leaves room for a large
// persistent profile.
const devtoolsResultLimit = 32 << 20

// cefOnDevToolsResult — the result of one DevTools method call (UI thread).
// The result is copied out of CEF's buffer, which is valid only here.
//
//export cefOnDevToolsResult
func cefOnDevToolsResult(data unsafe.Pointer, id, success C.int, result unsafe.Pointer, n C.size_t) {
	s := cefSessionOf(data)
	if s == nil {
		return
	}
	var raw []byte
	if result != nil && n > 0 && n <= devtoolsResultLimit {
		raw = C.GoBytes(result, C.int(n))
	}
	s.devtoolsResult(int(id), success != 0, raw, time.Now())
}

// cefFocusBrowser — a seam so ordering tests need no real browser.
var cefFocusBrowser = func(browser unsafe.Pointer) { C.cef_focus(browser) }

//export cefOnBrowserCreated
func cefOnBrowserCreated(browser, data unsafe.Pointer) {
	s := cefSessionOf(data)
	if s == nil {
		// The session finished before the browser existed: nothing will
		// close it otherwise. cef_close releases the add_ref'd browser.
		cefCloseBrowser(browser)
		return
	}
	s.browser = browser
	if s.wait != nil || s.wr.owner != "" || s.del != nil {
		// Reads of Partitioned cookies need a live browser, for the wait and
		// for the end of the popup. A failed registration leaves the visitor
		// path as the only source.
		s.dtReg = cefDevToolsAttach(browser, s.key)
	}
	cefFocusBrowser(browser)
	webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
		"browser created id=%v", browser)
	if s.del != nil {
		s.deleteStart()
	}
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
	s.noteURL(C.GoString(url), true) // load start is main frame only
	s.checkTrapURI(C.GoString(url))
}

//export cefOnLoadEnd
func cefOnLoadEnd(data unsafe.Pointer) {
	s := cefSessionOf(data)
	if s != nil && s.browser != nil {
		// Fresh DOM — (re)attach the focus tracker + spatial nav.
		C.cef_inject_scripts(s.browser)
	}
	if s != nil {
		s.checkCookie() // a page load is the usual moment a cookie appears
	}
}

//export cefOnAddressChange
func cefOnAddressChange(data unsafe.Pointer, url *C.char, isMain C.int) {
	if s := cefSessionOf(data); s != nil {
		s.noteURL(C.GoString(url), isMain != 0)
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

// cookieQuery — one URL-scoped cookie read: the URL it asks for, the
// cookies CEF visited so far, and where the result goes. Either done
// (webcookies) or session (popup cookie wait) is set.
type cookieQuery struct {
	url       cookieURL
	done      chan<- []WebpopupCookie // buffered (1): one send, never blocks
	session   *cefSession             // popup wait; UI thread only
	mu        sync.Mutex
	cancelled bool // guarded by mu
	visits    []cookieVisit
	// firstParty is the owner's held Partitioned cookie to add to a
	// webcookies()/persistWebCookie result (set before the read starts).
	firstParty *WebpopupCookie
	// firstPartyAmbiguous names a cookie the hold found ambiguous for the URL.
	firstPartyAmbiguous string
	// owner and gen: the plugin the read answers and its popup generation
	// when the read was allowed; a newer popup supersedes the answer.
	owner string
	gen   uint64
}

// cookieQueries — the reads in flight, keyed by the visitor's user data,
// a C-owned token (see cvis_t). An entry is removed exactly once, by
// cefCookiesDone or by cancelCookieQuery, whichever comes first; that
// removal alone decrements cefActiveN.
var cookieQueries sync.Map // unsafe.Pointer -> *cookieQuery

// newCookieToken — the visitor's user data, C memory freed by the visitor;
// nil when malloc fails.
func newCookieToken() unsafe.Pointer { return C.malloc(1) }

// startCookieQuery registers q under a fresh token and returns it with a
// cancel function. Safe from any goroutine; the cancel is idempotent and a
// no-op once CEF completed the query, even if the token's address was
// reused since (the entry is matched by q, not by address alone). ok is
// false when no token could be allocated: nothing is registered and
// cefActiveN is untouched; the caller reports an empty result.
func startCookieQuery(q *cookieQuery) (key unsafe.Pointer, cancel func(), ok bool) {
	key = newCookieToken()
	if key == nil {
		return nil, nil, false
	}
	cookieQueries.Store(key, q)
	cefActiveN.Add(1)
	return key, func() { cancelCookieQuery(key, q) }, true
}

// cancelCookieQuery detaches q: later C callbacks find no entry and do
// nothing (cefCookieVisit answers 0, which stops the visit).
func cancelCookieQuery(key unsafe.Pointer, q *cookieQuery) {
	if !cookieQueries.CompareAndDelete(key, q) {
		return
	}
	cefActiveN.Add(-1)
	q.mu.Lock()
	q.cancelled = true
	q.visits = nil
	q.mu.Unlock()
}

// cefCookieVisit — one cookie of a read. Returns 0 to stop the visit when
// the query is gone or cancelled. Never logs a name or value.
//
//export cefCookieVisit
func cefCookieVisit(data unsafe.Pointer, name, value, domain, path *C.char,
	secure, httpOnly, hasExpires C.int, expires C.int64_t) C.int {
	cv := cookieVisit{
		name: C.GoString(name), value: C.GoString(value),
		domain: C.GoString(domain), path: C.GoString(path),
		secure: secure != 0, httpOnly: httpOnly != 0,
	}
	if hasExpires != 0 {
		if u, ok := cefBaseTimeUnix(int64(expires)); ok {
			cv.expires = u
		}
	}
	if recordCookieVisit(data, cv) {
		return 1
	}
	return 0
}

// recordCookieVisit adds one visited cookie to the query under key; false
// (stop visiting) when the query is gone or cancelled.
func recordCookieVisit(key unsafe.Pointer, cv cookieVisit) bool {
	v, ok := cookieQueries.Load(key)
	if !ok {
		return false
	}
	q := v.(*cookieQuery)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancelled {
		return false
	}
	q.visits = append(q.visits, cv)
	return true
}

//export cefOnConsole
func cefOnConsole(data unsafe.Pointer, msg *C.char) {
	if cefConsoleN < 200 {
		cefConsoleN++
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"console: %s", C.GoString(msg))
	}
}

// cefCookiesDone — the end of one read; a second call for the same key
// finds nothing and does nothing (completion once). Never blocks the UI
// thread, also after WebpopupCookieList gave up on its timeout. A popup
// wait result goes to the session, a webcookies() result to its channel.
//
//export cefCookiesDone
func cefCookiesDone(data unsafe.Pointer) {
	v, ok := cookieQueries.LoadAndDelete(data)
	if !ok {
		return
	}
	cefActiveN.Add(-1)
	q := v.(*cookieQuery)
	q.mu.Lock()
	if q.cancelled {
		q.mu.Unlock()
		return
	}
	visits := q.visits
	q.mu.Unlock()
	if s := q.session; s != nil {
		s.cookieQueryDone(q, visits)
		return
	}
	if !cefOwnerCtx.stillCurrent(q.owner, q.gen) {
		q.done <- []WebpopupCookie{}
		return
	}
	cookies, ambiguous := mergeFirstParty(visits, q.url, q.firstParty, q.firstPartyAmbiguous)
	if ambiguous > 0 {
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"webcookies: %d ambiguous cookie name(s) withheld", ambiguous)
	}
	q.done <- cookies
}

//export cefCookieDelMatch
func cefCookieDelMatch(dom, path, name *C.char) C.int {
	if cookieDeleteMatches(C.GoString(dom), C.GoString(path), C.GoString(name),
		cefDelHost, cefDelPath, cefDelNames) {
		return 1
	}
	return 0
}

//export cefCookiesDeleted
func cefCookiesDeleted(data unsafe.Pointer, n C.int) {
	cefActiveN.Add(-1)
	if cefCookiesDelCh != nil {
		cefCookiesDelCh <- int(n)
	}
}

// ------------------------------------------------------------------
// session lifecycle (main thread)
// ------------------------------------------------------------------

// noteURL remembers the main frame's latest https page; a subframe never
// moves the popup's origin.
func (s *cefSession) noteURL(uri string, isMain bool) {
	if s.closing {
		return // the page the user closed stays the popup's end
	}
	s.topURL = mainFrameURL(s.topURL, uri, isMain)
}

func (s *cefSession) checkTrapURI(uri string) {
	if uri != "" && s.trap != "" && strings.HasPrefix(uri, s.trap) &&
		s.wr.Trapped.URL == "" {
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"Opening %s -- Final URI reached", uri)
		s.wr.Trapped.URL = uri
		s.finish(WebpopupTrappedURL)
	}
}

// checkCookie starts one asynchronous URL-scoped read for the awaited
// cookie. Triggers (page loads, the poll) coalesce: a check that arrives
// while one runs queues exactly one follow-up. The UI thread never waits.
func (s *cefSession) checkCookie() {
	if s.finished || s.closing || s.wait == nil {
		return
	}
	if s.cookieQuery != nil || s.dt.busy(time.Now()) {
		s.cookieCheckPending = true
		return
	}
	s.cookieCheckPending = false
	q := &cookieQuery{url: s.wait.target, session: s}
	key, cancel, ok := startCookieQuery(q)
	if !ok {
		return // out of memory: no read; the next poll or page load retries
	}
	s.cookieQuery, s.cookieCancel = q, cancel
	curi := C.CString(s.wait.target.scheme + "://" + s.wait.target.host +
		s.wait.target.path)
	defer C.free(unsafe.Pointer(curi))
	C.cef_get_cookies(curi, key)
}

// cefCanonURL — uri as cef_parse_url canonicalizes it (see cookieURL).
func cefCanonURL(uri string) (cookieURL, bool) {
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	var scheme, host, path *C.char
	if C.cef_canon_url(curi, &scheme, &host, &path) == 0 {
		return cookieURL{}, false
	}
	u := cookieURL{C.GoString(scheme), C.GoString(host), C.GoString(path)}
	C.free(unsafe.Pointer(scheme))
	C.free(unsafe.Pointer(host))
	C.free(unsafe.Pointer(path))
	return u, true
}

// cefCreateBrowser — the browser-create call, a seam for failure tests.
var cefCreateBrowser = func(key unsafe.Pointer, uri string) bool {
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	return C.cef_browser_create(key, 1280, 720, curi) != nil
}

// cookieQueryDone — a wait read ended (UI thread). The cookie completes the
// wait once; otherwise a coalesced check runs.
func (s *cefSession) cookieQueryDone(q *cookieQuery, visits []cookieVisit) {
	if s.cookieQuery == q {
		s.cookieQuery, s.cookieCancel = nil, nil
	}
	if s.finished || s.closing {
		return
	}
	// The jar is shared: the cookie completes the wait only while the main
	// frame shows the wait origin, judged now and not when the read started.
	// Otherwise the poll and the next page load ask again.
	if cookieWaitSatisfied(visits, q.url, s.wait.Name) && cookieWaitMayComplete(s.topURL, s.wait) {
		// A Partitioned cookie of the same name is invisible to the visitor
		// but is sent with the other one: let DevTools rule that out first.
		now := time.Now()
		if s.dtUnanswered.IsZero() {
			s.dtUnanswered = now
		}
		if now.Sub(s.dtUnanswered) < devtoolsReadLimit && s.startDevToolsRead(now) {
			s.visitorWaiting = true
			return
		}
		s.waiter.Fire(WebpopupCookieFound)
		return
	}
	// The visitor never returns a Partitioned cookie: ask DevTools too. Its
	// result runs the coalesced check.
	if s.startDevToolsRead(time.Now()) {
		return
	}
	if s.cookieCheckPending {
		s.checkCookie()
	}
}

// devtoolsTargetURL is the wait target as DevTools wants it, with the port.
func (s *cefSession) devtoolsTargetURL() string {
	host := s.wait.target.host
	if s.wait.port != "" {
		host += ":" + s.wait.port
	}
	return s.wait.target.scheme + "://" + host + s.wait.target.path
}

// startDevToolsRead sends one Network.getCookies for the wait target, unless
// one is in flight (an unanswered one is abandoned after devtoolsReadLimit)
// or the main frame is not on the wait origin. It reports whether a read is
// now in flight.
func (s *cefSession) startDevToolsRead(now time.Time) bool {
	if s.finished || s.wait == nil || s.dtReg == nil || s.browser == nil ||
		!cookieWaitMayComplete(s.topURL, s.wait) {
		return false
	}
	if s.dt.busy(now) {
		return true
	}
	s.dtNextID++
	if !cefDevToolsSend(s.browser, devtoolsCookiesRequest(s.dtNextID, s.devtoolsTargetURL())) {
		return false
	}
	s.dt.begin(s.dtNextID, now)
	return true
}

// devtoolsResult — a DevTools read ended (UI thread). A first-party
// Partitioned cookie completes the wait once, like the visitor's cookie; the
// cookie is kept for finish() to hand to the owner. Never logs a name or
// value, only the refusal reason when it changes.
func (s *cefSession) devtoolsResult(id int, success bool, raw []byte, now time.Time) {
	if s.del != nil {
		s.deleteResult(id, success, raw)
		return
	}
	if !s.dt.take(id) || s.finished {
		return
	}
	if s.closing {
		s.endResult(success, raw, now)
		return
	}
	if s.wait == nil {
		return
	}
	s.dtUnanswered = time.Time{}
	forVisitor := s.visitorWaiting
	s.visitorWaiting = false
	// DevTools failing leaves the visitor's cookie standing; only a twin seen
	// by DevTools refuses it.
	standing := forVisitor && cookieWaitMayComplete(s.topURL, s.wait)
	reason := "devtools-error"
	if success {
		list, err := parseDevToolsCookies(raw)
		if err != nil {
			reason = "devtools-unreadable"
		} else {
			s.traceDevToolsList(list)
			c, why := selectFirstPartyCookie(list, s.wait.target, waitOrigin(s.wait),
				httpsOriginOf(s.topURL), s.wait.Name, now)
			if c != nil {
				s.fpCookie = c
				if !s.waiter.Fire(WebpopupCookieFound) {
					s.fpCookie = nil
				}
				return
			}
			reason = why
			if why == "ambiguous-cookie" {
				standing = false // a twin: neither cookie may be taken
			}
		}
	}
	if standing {
		s.waiter.Fire(WebpopupCookieFound)
		return
	}
	if reason != s.dtLastReason {
		s.dtLastReason = reason
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef",
			"devtools cookie read: %s", reason)
	}
	if s.cookieCheckPending {
		s.checkCookie()
	}
}

// traceDevToolsList logs, when it changes, how many cookies DevTools listed
// and how many of the awaited name, partitioned or not: counts only.
func (s *cefSession) traceDevToolsList(list []devtoolsCookie) {
	named, part := 0, 0
	for _, c := range list {
		if c.Name == s.wait.Name {
			named++
			if c.PartitionKey != nil || c.PartitionKeyOpaque {
				part++
			}
		}
	}
	sum := fmt.Sprintf("%d listed, %d named, %d partitioned", len(list), named, part)
	if sum != s.dtLastList {
		s.dtLastList = sum
		webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "cef", "devtools cookie read: %s", sum)
	}
}

// finish ends the session. Every terminal source (trap, close, load error,
// cookie found, wait timeout) comes through here and through the waiter's
// Resolve first: the first source to resolve fixes the result, and a
// later source finishes with that result instead of its own. UI thread only.
func (s *cefSession) finish(code int) {
	if s.waiter != nil {
		code = s.waiter.Resolve(code)
	}
	if s.finished || s.closing {
		return
	}
	if s.startEndRead(code) {
		return
	}
	s.completeFinish(code)
}

// ---- deletion of first-party Partitioned cookies ----

// cefDeleteJob — one deletion run by an invisible helper browser: the
// visitor cannot see (so cannot delete) a Partitioned cookie, and no popup
// browser is alive when a plugin forgets its clearance. DevTools lists the
// store with partition keys and deletes exactly the first-party ones.
type cefDeleteJob struct {
	host, path string
	names      map[string]bool
	done       func(n int)
	listID     int
	pending    map[int]bool
	deleted    int
	timer      *time.Timer
}

// deleteJobLimit bounds the whole helper-browser deletion.
const deleteJobLimit = 8 * time.Second

// startDeleteJob starts the deletion on the UI thread; done receives the
// number of cookies removed, exactly once, also on any failure (0).
func startDeleteJob(host, path string, names map[string]bool, done func(n int)) *cefSession {
	s := &cefSession{wr: &WebpopupResult{}, done: make(chan *WebpopupResult, 1), hidden: true}
	s.del = &cefDeleteJob{host: host, path: path, names: names, done: done, pending: map[int]bool{}}
	s.key = cefTokenNew()
	cefSessions.Store(s.key, s)
	if !cefCreateBrowser(s.key, "about:blank") {
		cefActiveN.Add(1) // balance completeFinish(), as for a failed popup create
		s.endDeleteJob()
		return s
	}
	cefActiveN.Add(1)
	s.del.timer = time.AfterFunc(deleteJobLimit, func() { wpPlatformDispatch(s.deleteTimeout) })
	return s
}

func (s *cefSession) deleteSend(msg func(id int) string) (id int, ok bool) {
	if s.dtReg == nil || s.browser == nil {
		return 0, false
	}
	s.dtNextID++
	if !cefDevToolsSend(s.browser, msg(s.dtNextID)) {
		return 0, false
	}
	return s.dtNextID, true
}

func (s *cefSession) deleteStart() {
	id, ok := s.deleteSend(devtoolsStorageRequest)
	if !ok {
		s.endDeleteJob()
		return
	}
	s.del.listID = id
}

func (s *cefSession) deleteResult(id int, success bool, raw []byte) {
	j := s.del
	if s.finished || j == nil {
		return
	}
	switch {
	case id == j.listID:
		j.listID = 0
		if !success {
			s.endDeleteJob()
			return
		}
		list, err := parseDevToolsCookies(raw)
		if err != nil {
			s.endDeleteJob()
			return
		}
		for _, c := range deletableFirstParty(list, j.host, j.path, j.names) {
			c := c
			if did, ok := s.deleteSend(func(id int) string { return devtoolsDeleteRequest(id, c) }); ok {
				j.pending[did] = true
			}
		}
		if len(j.pending) == 0 {
			s.endDeleteJob()
		}
	case j.pending[id]:
		delete(j.pending, id)
		if success {
			j.deleted++
		}
		if len(j.pending) == 0 {
			s.endDeleteJob()
		}
	}
}

func (s *cefSession) deleteTimeout() {
	if s.del != nil && !s.finished {
		s.endDeleteJob()
	}
}

// endDeleteJob reports the count once and closes the helper browser.
func (s *cefSession) endDeleteJob() {
	j := s.del
	if j == nil || s.finished {
		return
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	// A hold for a cookie that is gone must not be served again.
	cefOwnerCtx.dropFirstParty(j.host, j.path, j.names)
	n := j.deleted
	s.del = nil
	s.completeFinish(WebpopupLoadError)
	j.done(n)
}

// endReadLimit bounds how long a popup that has ended keeps its browser for
// the DevTools read of first-party Partitioned cookies.
const endReadLimit = 2 * time.Second

// startEndRead reads, before the browser goes away, the first-party
// Partitioned cookies of the page the popup ended on, so that the owner's
// persistWebCookie right after the popup still finds them (the visitor
// cannot see them and the browser is destroyed). It applies to any result
// that records an owner context, only for an owner and an https page, and
// holds nothing but cookies the first-party rule accepts for exactly that
// origin. The read is bounded by endReadLimit; true means the end is
// deferred to its answer or the bound.
func (s *cefSession) startEndRead(code int) bool {
	if s.dtReg == nil || s.browser == nil || s.wr.owner == "" ||
		popupContextURL(code, s.topURL, s.wait) == "" {
		return false
	}
	if httpsOriginOf(s.topURL) == "" {
		return false
	}
	now := time.Now()
	s.dtNextID++
	// The whole browser context, not the page's URL: a first-party cookie of
	// the origin may sit on another path than the page the popup ended on.
	if !cefDevToolsSend(s.browser, devtoolsStorageRequest(s.dtNextID)) {
		return false
	}
	s.dt.begin(s.dtNextID, now)
	s.closing, s.endCode = true, code
	s.hideSurface()
	s.endTimer = time.AfterFunc(endReadLimit, func() { wpPlatformDispatch(s.endTimeout) })
	return true
}

// endResult takes the answer of the end read (UI thread).
func (s *cefSession) endResult(success bool, raw []byte, now time.Time) {
	if success {
		if list, err := parseDevToolsCookies(raw); err == nil {
			s.endSet.cookies, s.endSet.blockers = selectFirstPartyInventory(list, httpsOriginOf(s.topURL), now)
		}
	}
	s.completeFinish(s.endCode)
}

// endTimeout is the bound of the end read: finish with nothing held.
func (s *cefSession) endTimeout() {
	if s.closing && !s.finished {
		s.completeFinish(s.endCode)
	}
}

func (s *cefSession) hideSurface() {
	if !s.hidden && wpSurfaceGlueCef.hide != nil {
		s.hidden = true
		wpSurfaceGlueCef.hide()
	}
}

// completeFinish is the end of the session once nothing is left to read.
func (s *cefSession) completeFinish(code int) {
	if s.finished {
		return
	}
	if s.endTimer != nil {
		s.endTimer.Stop()
		s.endTimer = nil
	}
	s.finished = true
	s.wr.ResultCode = code
	ctxURL := popupContextURL(code, s.topURL, s.wait)
	set := s.endSet
	// Only a successful final inventory is held. When it failed, timed out
	// or never ran, nothing is: the cookie the wait found may be deleted by
	// now, and the plugin's next attempt retries.
	cefOwnerCtx.recordFirstPartySet(s.wr.owner, s.ownerGen, ctxURL, time.Now(), set)
	s.fpCookie, s.endSet = nil, firstPartySet{}
	if s.dtReg != nil {
		// Before the close: the registration is released on the UI thread
		// while the browser is alive.
		cefDevToolsDetach(s.dtReg)
		s.dtReg = nil
	}
	if s.cookieCancel != nil {
		s.cookieCancel()
		s.cookieQuery, s.cookieCancel = nil, nil
	}
	cefActiveN.Add(-1)
	s.hideSurface()
	if s.browser != nil {
		cefCloseBrowser(s.browser)
		s.browser = nil
	}
	if s.key != nil {
		// Unmap first, then drop the session's reference: the token memory
		// lives on while a handler can still call back, so its address
		// cannot be reused while a stale map entry could match it.
		cefSessions.Delete(s.key)
		cefTokenRelease(s.key)
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
			// Chromium just installed its own SIGTERM/SIGINT
			// handlers — re-arm ours so a later signal reaches the
			// app again instead of dying in the browser loop.
			arch.SetSignalHandler(func(sig os.Signal) {
				if webpopupDeps.signalShutdown != nil {
					webpopupDeps.signalShutdown()
				}
			})
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

var cefUAOnce sync.Once
var cefUA string

// wpPlatformUserAgent — the exact UA CEF sends: Chromium's reduced UA
// carries Chrome/<major>.0.0.0 (user_agent_utils.cc), with the major
// taken from the loaded library via cef_version_info — mirrors CEF's
// own default so a plugin pinning $ua gets the UA the challenge was
// solved on. Computed lazily so webuseragent() works before the first
// popup too.
func wpPlatformUserAgent() string {
	cefUAOnce.Do(func() {
		cefUA = "Mozilla/5.0 (" + cefUAPlatform() + ") " +
			"AppleWebKit/537.36 (KHTML, like Gecko) " +
			"Chrome/" + strconv.Itoa(int(C.cef_version_info(4))) +
			".0.0.0 Safari/537.36"
	})
	return cefUA
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
func wpNewSession(uri, trap string, wait *WebpopupCookieWait, wr *WebpopupResult,
	done chan<- *WebpopupResult) *cefSession {
	s := &cefSession{trap: trap, wait: wait, wr: wr, done: done}
	s.ownerGen = cefOwnerCtx.begin(wr.owner)
	s.key = cefTokenNew()
	cefSessions.Store(s.key, s)
	if wait != nil {
		// Match cookies against CEF's own canonical form of the target
		// (dot segments, punycode), still confined to the opener's host.
		t, ok := canonWaitTarget(cefCanonURL, uri, wait.target)
		if !ok {
			cefActiveN.Add(1) // balance finish(), as for a failed create
			s.finish(WebpopupLoadError)
			return s
		}
		wait.target = t
	}
	if !cefCreateBrowser(s.key, uri) {
		// No browser was created, so finish() must not decrement the
		// active count: balance it first.
		cefActiveN.Add(1)
		s.finish(WebpopupLoadError)
		return s
	}
	cefActiveN.Add(1)

	if wpSurfaceGlueCef.show == nil || !wpSurfaceGlueCef.show(s) {
		s.finish(WebpopupLoadError)
		return s
	}
	if wait != nil {
		s.waiter = startCookieWaiter(wait.Timeout, cookieWaitPollEvery,
			func() { wpPlatformDispatch(s.checkCookie) },
			func(code int) { wpPlatformDispatch(func() { s.finish(code) }) })
		s.checkCookie() // the first page load asks again if no page is shown yet
	}
	return s
}

// wpPlatformRunPopup — runs on the main thread; completion arrives
// via trap or user close on this same context.
func wpPlatformRunPopup(uri, title, trap string, wait *WebpopupCookieWait,
	wr *WebpopupResult, done chan<- *WebpopupResult) {
	if cefEnsureInit() != 1 {
		wr.ResultCode = WebpopupLoadError
		done <- wr
		return
	}
	wpNewSession(uri, trap, wait, wr, done)
}

// wpPlatformBrowser — detached browsing shares the embedded surface.
func wpPlatformBrowser(uri, title string) {
	if cefEnsureInit() != 1 {
		return
	}
	wr := &WebpopupResult{}
	done := make(chan *WebpopupResult, 1)
	wpNewSession(uri, "", nil, wr, done)
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

// wpPlatformCookies — URL-scoped cookie read via the global cookie
// manager, for an owner only on the origin its own popup ended on (the
// ownerless legacy read of webcookies() is not restricted). Runs on the UI thread and only starts the query; the result goes
// to done. A URL CEF cannot parse answers at once with an empty list, and
// a CEF that cannot start with nil (unsupported).
func wpPlatformCookies(uri string, r *cookieRead) {
	if cefEnsureInit() != 1 {
		r.done <- nil
		return
	}
	gen, allowed := cefOwnerCtx.readAllowedGen(r.owner, uri)
	if !allowed {
		// The jar is shared by all plugins: an owner gets a cookie only
		// from the origin its own popup ended on.
		r.done <- []WebpopupCookie{}
		return
	}
	u, ok := cefCanonURL(uri)
	if !ok {
		r.done <- []WebpopupCookie{}
		return
	}
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	q := &cookieQuery{url: u, done: r.done, owner: r.owner, gen: gen}
	if r.owner != "" && r.name != "" {
		// The popup's browser is gone, and the visitor cannot see a
		// Partitioned cookie: add what the owner's wait read, if still held.
		var amb bool
		q.firstParty, amb = cefOwnerCtx.firstPartyLookup(r.owner, uri, u, r.name, time.Now())
		if amb {
			q.firstPartyAmbiguous = r.name
		}
	}
	key, cancel, ok := startCookieQuery(q)
	if !ok {
		r.done <- []WebpopupCookie{}
		return
	}
	if !r.Attach(cancel) {
		// The caller gave up before the UI thread got here: no visitor
		// was created, so the token is still ours to free.
		cancel()
		C.free(key)
		return
	}
	C.cef_get_cookies(curi, key)
}

var cefCookiesDelCh chan int
var cefDelHost, cefDelPath string
var cefDelNames map[string]bool

// wpPlatformDeleteCookies — deletion via visit_all_cookies with the
// *del flag; each cookie is filtered Go-side in cefCookieDelMatch
// (domain/path/name). done receives the number of deleted cookies. CEF has
// one cookie store shared by every plugin, so the owner cannot scope the
// deletion: it only keeps the signature the Android backend needs.
func wpPlatformDeleteCookies(owner, host, path string, names map[string]bool,
	done chan int) {
	if cefEnsureInit() != 1 {
		done <- 0
		return
	}
	cefDelHost = host
	cefDelPath = path
	if cefDelPath == "" {
		cefDelPath = "/"
	}
	cefDelNames = names
	delPath := cefDelPath
	// The visitor never saw a Partitioned cookie, so the deletion below
	// cannot reach one; what a wait read and holds must not outlive it.
	cefOwnerCtx.dropFirstParty(host, delPath, names)
	vis := make(chan int, 1)
	cefCookiesDelCh = vis
	cefActiveN.Add(1)
	C.cef_delete_cookies(nil)
	// Then the first-party Partitioned ones, through DevTools. Never block
	// the UI thread on the visitor: wait for it on a goroutine and start
	// the helper browser back on the UI thread.
	go func() {
		n := 0
		select {
		case n = <-vis:
		case <-time.After(deleteJobLimit):
		}
		wpPlatformDispatch(func() {
			startDeleteJob(host, delPath, names, func(m int) { done <- n + m })
		})
	}()
}
