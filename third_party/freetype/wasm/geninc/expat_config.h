/* expat_config.h — minimal wasi build of expat 2.8.3 */
#define BYTEORDER 1234
#define HAVE_MEMMOVE 1
#define HAVE_BCOPY 1
#define XML_CONTEXT_BYTES 1024
#define XML_STATIC 1
#define XML_GE 1
#define HAVE_GETENTROPY 1
#define XML_ATTR_INFO 1
/* keep off: XML_NS, XML_DTD? fontconfig needs doctype decl handler */
#define XML_DTD 1
#define XML_NS 1
