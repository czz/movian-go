/*
 * connman_glue.h — declarations for the C shims in connman_glue.c and
 * the Go-exported callbacks in connman_export.go.
 *
 * C: src/networking/connman.c — pieces that cgo cannot express:
 * GDBusInterfaceVTable / GSourceFuncs tables, g_signal_connect,
 * g_bus_own_name, and the variadic g_variant_new / g_variant_builder_add
 * / g_variant_iter_loop / g_dbus_method_invocation_return_error calls.
 */
#ifndef CONNMAN_GLUE_H
#define CONNMAN_GLUE_H

#include <gio/gio.h>
#include <stdint.h>

/* ---- Go-exported callbacks (connman_export.go) ---- */

gboolean movianCourierCheck(uintptr_t h);
gboolean movianCourierDispatch(uintptr_t h);
void movianMgrSignal(char *sender, char *signal, GVariant *params);
void movianSvcSignal(uintptr_t cs, char *sender, char *signal,
		     GVariant *params);
void movianConnectDone(uintptr_t cs, GAsyncResult *res);
void movianAgentMethodCall(GDBusConnection *conn, char *sender,
			   char *path, char *iface,
			   char *method, GVariant *params,
			   GDBusMethodInvocation *inv);
void movianBusAcquired(GDBusConnection *conn, char *name,
		       GDBusProxy *mgr);

/* ---- courier GSource (C: prop_glib_courier.c) ---- */

GSource *movian_courier_source_new(uintptr_t h);

/* ---- signal connect shims ---- */

void movian_connect_mgr_signal(GDBusProxy *proxy);
void movian_connect_svc_signal(GDBusProxy *proxy, uintptr_t cs);

/* ---- async Connect call (C: connman_service_connect) ---- */

void movian_call_connect(GDBusProxy *proxy, uintptr_t cs);

/* ---- agent name + object registration (C: on_bus_acquired) ---- */

guint movian_own_agent_name(GDBusProxy *mgr);
void movian_register_agent_object(GDBusConnection *conn);

/* ---- variadic g_variant_* / g_dbus_* wrappers ---- */

GVariantBuilder *movian_vardict_builder_new(void);
GVariant *movian_variant_new_o(const char *path);
GVariant *movian_variant_new_sv_bool(const char *key, gboolean val);
GVariant *movian_variant_new_tuple_builder(GVariantBuilder *b);
void movian_builder_add_sv_string(GVariantBuilder *b, const char *key,
				  const char *val);
gboolean movian_iter_loop_sv(GVariantIter *it, char **key, GVariant **val);
int movian_variant_type_is_array(GVariant *v);
void movian_inv_return_error_invalid_args(GDBusMethodInvocation *inv,
					  const char *detail);
void movian_inv_return_error_unknown_method(GDBusMethodInvocation *inv,
					    const char *method);

#endif
