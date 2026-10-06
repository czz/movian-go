//go:build linux && connman && !godbus

/*
 * connman_glue.c — C shims for the Go port of src/networking/connman.c
 * (plus src/prop/prop_glib_courier.c). Contains everything cgo cannot
 * express directly: the agent GDBusInterfaceVTable, the courier
 * GSourceFuncs, g_signal_connect / g_bus_own_name trampolines and the
 * variadic g_variant / g_dbus_method_invocation calls.
 */
#include "connman_glue.h"

/*
 * Courier GSource — C: prop_glib_courier.c:24-97.
 * glib_courier_t embeds a GSource followed by the courier pointer; here
 * the Go *Courier crosses as a cgo.Handle stored in `h`.
 */
typedef struct {
	GSource s;
	uintptr_t h;
} movian_courier_source_t;

/* C: glib_courier_prepare (prop_glib_courier.c:45-49) */
static gboolean
courier_source_prepare(GSource *s, gint *timeout)
{
	*timeout = -1;
	return FALSE;
}

/* C: glib_courier_check (prop_glib_courier.c:56-60) */
static gboolean
courier_source_check(GSource *s)
{
	movian_courier_source_t *mc = (movian_courier_source_t *)s;
	return movianCourierCheck(mc->h);
}

/* C: glib_courier_dispatch (prop_glib_courier.c:67-72) */
static gboolean
courier_source_dispatch(GSource *s, GSourceFunc callback, gpointer aux)
{
	movian_courier_source_t *mc = (movian_courier_source_t *)s;
	return movianCourierDispatch(mc->h);
}

/* C: source_funcs (prop_glib_courier.c:78-82) */
static GSourceFuncs courier_source_funcs = {
	courier_source_prepare,
	courier_source_check,
	courier_source_dispatch,
	NULL,
};

/* C: glib_courier_create (prop_glib_courier.c:89-97) — the source half;
 * the prop_courier_t is created Go-side with NewCourierNotify. */
GSource *
movian_courier_source_new(uintptr_t h)
{
	GSource *s = g_source_new(&courier_source_funcs,
				  sizeof(movian_courier_source_t));
	movian_courier_source_t *mc = (movian_courier_source_t *)s;
	mc->h = h;
	return s;
}

/*
 * g-signal trampolines — C: connman_mgr_signal (connman.c:480-497) and
 * connman_svc_signal (connman.c:196-213). `user_data` carries the
 * cgo.Handle of the Go connmanService for service proxies.
 */
static void
mgr_signal_tramp(GDBusProxy *proxy, gchar *sender_name,
		 gchar *signal_name, GVariant *parameters, gpointer user_data)
{
	movianMgrSignal(sender_name, signal_name, parameters);
}

static void
svc_signal_tramp(GDBusProxy *proxy, gchar *sender_name,
		 gchar *signal_name, GVariant *parameters, gpointer user_data)
{
	movianSvcSignal((uintptr_t)user_data, sender_name, signal_name,
			parameters);
}

void
movian_connect_mgr_signal(GDBusProxy *proxy)
{
	g_signal_connect(G_OBJECT(proxy), "g-signal",
			 G_CALLBACK(mgr_signal_tramp), NULL);
}

void
movian_connect_svc_signal(GDBusProxy *proxy, uintptr_t cs)
{
	g_signal_connect(G_OBJECT(proxy), "g-signal",
			 G_CALLBACK(svc_signal_tramp), (gpointer)cs);
}

/*
 * C: g_dbus_proxy_call(cs->cs_proxy, "Connect", ..., connman_connect_cb, cs)
 * (connman.c:170-172).
 */
static void
connect_done_tramp(GObject *source_object, GAsyncResult *res,
		   gpointer user_data)
{
	movianConnectDone((uintptr_t)user_data, res);
}

void
movian_call_connect(GDBusProxy *proxy, uintptr_t cs)
{
	g_dbus_proxy_call(proxy, "Connect", NULL,
			  G_DBUS_CALL_FLAGS_NONE, 600 * 1000, NULL,
			  connect_done_tramp, (gpointer)cs);
}

/*
 * Agent method dispatch — C: connman_agent_vtable (connman.c:670-674)
 * wrapping handle_method_call (connman.c:625-666).
 */
static void
agent_method_call_tramp(GDBusConnection *connection, const gchar *sender,
			const gchar *object_path, const gchar *interface_name,
			const gchar *method_name, GVariant *parameters,
			GDBusMethodInvocation *invocation, gpointer user_data)
{
	movianAgentMethodCall(connection, (char *)sender, (char *)object_path,
			      (char *)interface_name, (char *)method_name,
			      parameters, invocation);
}

/* C: connman_agent_vtable (connman.c:670-674) */
static const GDBusInterfaceVTable movian_agent_vtable = {
	agent_method_call_tramp,
	NULL,
	NULL,
};

/* C: connman_agent_xml (connman.c:45-71) — verbatim */
static const char *connman_agent_xml =
	"<node name=\"/net/connman/Agent\">"
	"  <interface name=\"net.connman.Agent\">"
	"    <method name=\"ReportError\">"
	"       <annotation name=\"org.freedesktop.DBus.GLib.Async\" value=\"true\"/>"
	"       <arg type=\"o\" direction=\"in\"/>"
	"       <arg type=\"s\" direction=\"in\"/>"
	"    </method>"
	"    <method name=\"RequestInput\">"
	"      <annotation name=\"org.freedesktop.DBus.GLib.Async\" value=\"true\"/>"
	"      <arg type=\"o\" direction=\"in\"/>"
	"      <arg type=\"a{sv}\" direction=\"in\"/>"
	"      <arg type=\"a{sv}\" direction=\"out\"/>"
	"    </method>"
	"    <method name=\"Cancel\">"
	"      <annotation name=\"org.freedesktop.DBus.GLib.Async\" value=\"\"/>"
	"    </method>"
	"    <method name=\"Release\">"
	"      <annotation name=\"org.freedesktop.DBus.GLib.Async\" value=\"\"/>"
	"   </method>"
	"  </interface>"
	"</node>";

/* C: g_dbus_connection_register_object block in on_bus_acquired
 * (connman.c:690-695) */
void
movian_register_agent_object(GDBusConnection *conn)
{
	GDBusNodeInfo *node_info =
		g_dbus_node_info_new_for_xml(connman_agent_xml, NULL);

	g_dbus_connection_register_object(conn,
					  "/showtime/netagent",
					  node_info->interfaces[0],
					  &movian_agent_vtable,
					  NULL, NULL, NULL);
}

/*
 * C: g_bus_own_name (connman.c:789-795). on_bus_acquired is real work
 * (agent registration); on_name_acquired / on_name_lost are empty in C
 * (connman.c:713-727) so they stay as no-op C trampolines.
 */
static void
bus_acquired_tramp(GDBusConnection *connection, const gchar *name,
		   gpointer user_data)
{
	movianBusAcquired(connection, (char *)name, (GDBusProxy *)user_data);
}

static void
name_acquired_tramp(GDBusConnection *connection, const gchar *name,
		    gpointer user_data)
{
}

static void
name_lost_tramp(GDBusConnection *connection, const gchar *name,
		gpointer user_data)
{
}

guint
movian_own_agent_name(GDBusProxy *mgr)
{
	return g_bus_own_name(G_BUS_TYPE_SYSTEM,
			      "com.showtimemediacenter.showtime.network.agent",
			      G_BUS_NAME_OWNER_FLAGS_NONE,
			      bus_acquired_tramp,
			      name_acquired_tramp,
			      name_lost_tramp,
			      mgr,
			      NULL);
}

/* ---- variadic wrappers ---- */

/* C: g_variant_builder_new(G_VARIANT_TYPE("a{sv}")) (connman.c:262) */
GVariantBuilder *
movian_vardict_builder_new(void)
{
	return g_variant_builder_new(G_VARIANT_TYPE("a{sv}"));
}

/* C: g_variant_new("(o)", "/showtime/netagent") (connman.c:697) */
GVariant *
movian_variant_new_o(const char *path)
{
	return g_variant_new("(o)", path);
}

/* C: g_variant_new("(sv)", "Powered", g_variant_new_boolean(enable))
 * (connman.c:754-755) */
GVariant *
movian_variant_new_sv_bool(const char *key, gboolean val)
{
	return g_variant_new("(sv)", key, g_variant_new_boolean(val));
}

/* C: g_variant_new("(a{sv})", builder) (connman.c:271) */
GVariant *
movian_variant_new_tuple_builder(GVariantBuilder *b)
{
	return g_variant_new("(a{sv})", b);
}

/* C: g_variant_builder_add(builder, "{sv}", key, g_variant_new_string(val))
 * (connman.c:264-265, 268-269) */
void
movian_builder_add_sv_string(GVariantBuilder *b, const char *key,
			     const char *val)
{
	g_variant_builder_add(b, "{sv}", key, g_variant_new_string(val));
}

/* C: g_variant_iter_loop(&iter, "{sv}", &key, &value)
 * (prop_gvariant.c:111) */
gboolean
movian_iter_loop_sv(GVariantIter *it, char **key, GVariant **val)
{
	return g_variant_iter_loop(it, "{sv}", key, val);
}

/* C: g_variant_type_is_array(T) — the macro form g_variant_get_type +
 * g_variant_type_is_array is callable but G_VARIANT_TYPE_* constants are
 * macro casts; keep the array test here for clarity. */
int
movian_variant_type_is_array(GVariant *v)
{
	return g_variant_type_is_array(g_variant_get_type(v));
}

/* C: g_dbus_method_invocation_return_error(inv, G_DBUS_ERROR,
 * G_DBUS_ERROR_INVALID_ARGS, "Unknown service") (connman.c:645-649) */
void
movian_inv_return_error_invalid_args(GDBusMethodInvocation *inv,
				     const char *detail)
{
	g_dbus_method_invocation_return_error_literal(inv,
						    G_DBUS_ERROR,
						    G_DBUS_ERROR_INVALID_ARGS,
						    detail);
}

/* C: g_dbus_method_invocation_return_error(inv, G_DBUS_ERROR,
 * G_DBUS_ERROR_INVALID_ARGS, "Unknown method %s", method_name)
 * (connman.c:660-663) — variadic */
void
movian_inv_return_error_unknown_method(GDBusMethodInvocation *inv,
				       const char *method)
{
	g_dbus_method_invocation_return_error(inv,
					      G_DBUS_ERROR,
					      G_DBUS_ERROR_INVALID_ARGS,
					      "Unknown method %s",
					      method);
}
