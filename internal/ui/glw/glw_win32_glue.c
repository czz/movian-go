/* Canonical GL extension loader for the windows glw build.
 * Upstream resolved these through glXGetProcAddress (linux) or had
 * them exported by libGL; opengl32.dll exports GL 1.1 only, so the
 * shader/FBO/VBO entry points are resolved via wglGetProcAddress,
 * lazily on first use (a GL context is current by then). */

#if defined(_WIN32)

#include <windows.h>
#include <GL/gl.h>
#include <GL/glext.h>

typedef void (APIENTRY *PFN_GLACTIVETEXTURE)(GLenum texture);
static PFN_GLACTIVETEXTURE p_glActiveTexture;
void ml_glActiveTexture(GLenum texture) {
    if (!p_glActiveTexture)
        p_glActiveTexture = (PFN_GLACTIVETEXTURE)wglGetProcAddress("glActiveTexture");
    p_glActiveTexture(texture);
}

typedef void (APIENTRY *PFN_GLATTACHSHADER)(GLuint program, GLuint shader);
static PFN_GLATTACHSHADER p_glAttachShader;
void ml_glAttachShader(GLuint program, GLuint shader) {
    if (!p_glAttachShader)
        p_glAttachShader = (PFN_GLATTACHSHADER)wglGetProcAddress("glAttachShader");
    p_glAttachShader(program, shader);
}

typedef void (APIENTRY *PFN_GLBINDATTRIBLOCATION)(GLuint program, GLuint index, const GLchar *name);
static PFN_GLBINDATTRIBLOCATION p_glBindAttribLocation;
void ml_glBindAttribLocation(GLuint program, GLuint index, const GLchar *name) {
    if (!p_glBindAttribLocation)
        p_glBindAttribLocation = (PFN_GLBINDATTRIBLOCATION)wglGetProcAddress("glBindAttribLocation");
    p_glBindAttribLocation(program, index, name);
}

typedef void (APIENTRY *PFN_GLBINDBUFFER)(GLenum target, GLuint buffer);
static PFN_GLBINDBUFFER p_glBindBuffer;
void ml_glBindBuffer(GLenum target, GLuint buffer) {
    if (!p_glBindBuffer)
        p_glBindBuffer = (PFN_GLBINDBUFFER)wglGetProcAddress("glBindBuffer");
    p_glBindBuffer(target, buffer);
}

typedef void (APIENTRY *PFN_GLBINDFRAMEBUFFEREXT)(GLenum target, GLuint framebuffer);
static PFN_GLBINDFRAMEBUFFEREXT p_glBindFramebufferEXT;
void ml_glBindFramebufferEXT(GLenum target, GLuint framebuffer) {
    if (!p_glBindFramebufferEXT)
        p_glBindFramebufferEXT = (PFN_GLBINDFRAMEBUFFEREXT)wglGetProcAddress("glBindFramebufferEXT");
    p_glBindFramebufferEXT(target, framebuffer);
}

typedef void (APIENTRY *PFN_GLBLENDFUNCSEPARATE)(GLenum sf, GLenum df, GLenum sa, GLenum da);
static PFN_GLBLENDFUNCSEPARATE p_glBlendFuncSeparate;
void ml_glBlendFuncSeparate(GLenum sf, GLenum df, GLenum sa, GLenum da) {
    if (!p_glBlendFuncSeparate)
        p_glBlendFuncSeparate = (PFN_GLBLENDFUNCSEPARATE)wglGetProcAddress("glBlendFuncSeparate");
    p_glBlendFuncSeparate(sf, df, sa, da);
}

typedef void (APIENTRY *PFN_GLBUFFERDATA)(GLenum target, GLsizeiptr size, const void *data, GLenum usage);
static PFN_GLBUFFERDATA p_glBufferData;
void ml_glBufferData(GLenum target, GLsizeiptr size, const void *data, GLenum usage) {
    if (!p_glBufferData)
        p_glBufferData = (PFN_GLBUFFERDATA)wglGetProcAddress("glBufferData");
    p_glBufferData(target, size, data, usage);
}

typedef void (APIENTRY *PFN_GLCOMPILESHADER)(GLuint shader);
static PFN_GLCOMPILESHADER p_glCompileShader;
void ml_glCompileShader(GLuint shader) {
    if (!p_glCompileShader)
        p_glCompileShader = (PFN_GLCOMPILESHADER)wglGetProcAddress("glCompileShader");
    p_glCompileShader(shader);
}

typedef GLuint (APIENTRY *PFN_GLCREATEPROGRAM)(void);
static PFN_GLCREATEPROGRAM p_glCreateProgram;
GLuint ml_glCreateProgram(void) {
    if (!p_glCreateProgram)
        p_glCreateProgram = (PFN_GLCREATEPROGRAM)wglGetProcAddress("glCreateProgram");
    return p_glCreateProgram();
}

typedef GLuint (APIENTRY *PFN_GLCREATESHADER)(GLenum type);
static PFN_GLCREATESHADER p_glCreateShader;
GLuint ml_glCreateShader(GLenum type) {
    if (!p_glCreateShader)
        p_glCreateShader = (PFN_GLCREATESHADER)wglGetProcAddress("glCreateShader");
    return p_glCreateShader(type);
}

typedef void (APIENTRY *PFN_GLDELETEBUFFERS)(GLsizei n, const GLuint *buffers);
static PFN_GLDELETEBUFFERS p_glDeleteBuffers;
void ml_glDeleteBuffers(GLsizei n, const GLuint *buffers) {
    if (!p_glDeleteBuffers)
        p_glDeleteBuffers = (PFN_GLDELETEBUFFERS)wglGetProcAddress("glDeleteBuffers");
    p_glDeleteBuffers(n, buffers);
}

typedef void (APIENTRY *PFN_GLDELETEFRAMEBUFFERSEXT)(GLsizei n, const GLuint *framebuffers);
static PFN_GLDELETEFRAMEBUFFERSEXT p_glDeleteFramebuffersEXT;
void ml_glDeleteFramebuffersEXT(GLsizei n, const GLuint *framebuffers) {
    if (!p_glDeleteFramebuffersEXT)
        p_glDeleteFramebuffersEXT = (PFN_GLDELETEFRAMEBUFFERSEXT)wglGetProcAddress("glDeleteFramebuffersEXT");
    p_glDeleteFramebuffersEXT(n, framebuffers);
}

typedef void (APIENTRY *PFN_GLDELETEPROGRAM)(GLuint program);
static PFN_GLDELETEPROGRAM p_glDeleteProgram;
void ml_glDeleteProgram(GLuint program) {
    if (!p_glDeleteProgram)
        p_glDeleteProgram = (PFN_GLDELETEPROGRAM)wglGetProcAddress("glDeleteProgram");
    p_glDeleteProgram(program);
}

typedef void (APIENTRY *PFN_GLDELETESHADER)(GLuint shader);
static PFN_GLDELETESHADER p_glDeleteShader;
void ml_glDeleteShader(GLuint shader) {
    if (!p_glDeleteShader)
        p_glDeleteShader = (PFN_GLDELETESHADER)wglGetProcAddress("glDeleteShader");
    p_glDeleteShader(shader);
}

typedef void (APIENTRY *PFN_GLDISABLEVERTEXATTRIBARRAY)(GLuint index);
static PFN_GLDISABLEVERTEXATTRIBARRAY p_glDisableVertexAttribArray;
void ml_glDisableVertexAttribArray(GLuint index) {
    if (!p_glDisableVertexAttribArray)
        p_glDisableVertexAttribArray = (PFN_GLDISABLEVERTEXATTRIBARRAY)wglGetProcAddress("glDisableVertexAttribArray");
    p_glDisableVertexAttribArray(index);
}

typedef void (APIENTRY *PFN_GLENABLEVERTEXATTRIBARRAY)(GLuint index);
static PFN_GLENABLEVERTEXATTRIBARRAY p_glEnableVertexAttribArray;
void ml_glEnableVertexAttribArray(GLuint index) {
    if (!p_glEnableVertexAttribArray)
        p_glEnableVertexAttribArray = (PFN_GLENABLEVERTEXATTRIBARRAY)wglGetProcAddress("glEnableVertexAttribArray");
    p_glEnableVertexAttribArray(index);
}

typedef void (APIENTRY *PFN_GLFRAMEBUFFERTEXTURE2DEXT)(GLenum t, GLenum a, GLenum tt, GLuint tex, GLint level);
static PFN_GLFRAMEBUFFERTEXTURE2DEXT p_glFramebufferTexture2DEXT;
void ml_glFramebufferTexture2DEXT(GLenum t, GLenum a, GLenum tt, GLuint tex, GLint level) {
    if (!p_glFramebufferTexture2DEXT)
        p_glFramebufferTexture2DEXT = (PFN_GLFRAMEBUFFERTEXTURE2DEXT)wglGetProcAddress("glFramebufferTexture2DEXT");
    p_glFramebufferTexture2DEXT(t, a, tt, tex, level);
}

typedef void (APIENTRY *PFN_GLGENBUFFERS)(GLsizei n, GLuint *buffers);
static PFN_GLGENBUFFERS p_glGenBuffers;
void ml_glGenBuffers(GLsizei n, GLuint *buffers) {
    if (!p_glGenBuffers)
        p_glGenBuffers = (PFN_GLGENBUFFERS)wglGetProcAddress("glGenBuffers");
    p_glGenBuffers(n, buffers);
}

typedef void (APIENTRY *PFN_GLGENFRAMEBUFFERSEXT)(GLsizei n, GLuint *framebuffers);
static PFN_GLGENFRAMEBUFFERSEXT p_glGenFramebuffersEXT;
void ml_glGenFramebuffersEXT(GLsizei n, GLuint *framebuffers) {
    if (!p_glGenFramebuffersEXT)
        p_glGenFramebuffersEXT = (PFN_GLGENFRAMEBUFFERSEXT)wglGetProcAddress("glGenFramebuffersEXT");
    p_glGenFramebuffersEXT(n, framebuffers);
}

typedef void (APIENTRY *PFN_GLGETPROGRAMINFOLOG)(GLuint p, GLsizei bs, GLsizei *len, GLchar *log);
static PFN_GLGETPROGRAMINFOLOG p_glGetProgramInfoLog;
void ml_glGetProgramInfoLog(GLuint p, GLsizei bs, GLsizei *len, GLchar *log) {
    if (!p_glGetProgramInfoLog)
        p_glGetProgramInfoLog = (PFN_GLGETPROGRAMINFOLOG)wglGetProcAddress("glGetProgramInfoLog");
    p_glGetProgramInfoLog(p, bs, len, log);
}

typedef void (APIENTRY *PFN_GLGETPROGRAMIV)(GLuint p, GLenum pname, GLint *params);
static PFN_GLGETPROGRAMIV p_glGetProgramiv;
void ml_glGetProgramiv(GLuint p, GLenum pname, GLint *params) {
    if (!p_glGetProgramiv)
        p_glGetProgramiv = (PFN_GLGETPROGRAMIV)wglGetProcAddress("glGetProgramiv");
    p_glGetProgramiv(p, pname, params);
}

typedef void (APIENTRY *PFN_GLGETSHADERINFOLOG)(GLuint s, GLsizei bs, GLsizei *len, GLchar *log);
static PFN_GLGETSHADERINFOLOG p_glGetShaderInfoLog;
void ml_glGetShaderInfoLog(GLuint s, GLsizei bs, GLsizei *len, GLchar *log) {
    if (!p_glGetShaderInfoLog)
        p_glGetShaderInfoLog = (PFN_GLGETSHADERINFOLOG)wglGetProcAddress("glGetShaderInfoLog");
    p_glGetShaderInfoLog(s, bs, len, log);
}

typedef void (APIENTRY *PFN_GLGETSHADERIV)(GLuint s, GLenum pname, GLint *params);
static PFN_GLGETSHADERIV p_glGetShaderiv;
void ml_glGetShaderiv(GLuint s, GLenum pname, GLint *params) {
    if (!p_glGetShaderiv)
        p_glGetShaderiv = (PFN_GLGETSHADERIV)wglGetProcAddress("glGetShaderiv");
    p_glGetShaderiv(s, pname, params);
}

typedef GLint (APIENTRY *PFN_GLGETUNIFORMLOCATION)(GLuint p, const GLchar *name);
static PFN_GLGETUNIFORMLOCATION p_glGetUniformLocation;
GLint ml_glGetUniformLocation(GLuint p, const GLchar *name) {
    if (!p_glGetUniformLocation)
        p_glGetUniformLocation = (PFN_GLGETUNIFORMLOCATION)wglGetProcAddress("glGetUniformLocation");
    return p_glGetUniformLocation(p, name);
}

typedef void (APIENTRY *PFN_GLLINKPROGRAM)(GLuint program);
static PFN_GLLINKPROGRAM p_glLinkProgram;
void ml_glLinkProgram(GLuint program) {
    if (!p_glLinkProgram)
        p_glLinkProgram = (PFN_GLLINKPROGRAM)wglGetProcAddress("glLinkProgram");
    p_glLinkProgram(program);
}

typedef void * (APIENTRY *PFN_GLMAPBUFFER)(GLenum target, GLenum access);
static PFN_GLMAPBUFFER p_glMapBuffer;
void * ml_glMapBuffer(GLenum target, GLenum access) {
    if (!p_glMapBuffer)
        p_glMapBuffer = (PFN_GLMAPBUFFER)wglGetProcAddress("glMapBuffer");
    return p_glMapBuffer(target, access);
}

typedef void (APIENTRY *PFN_GLSHADERSOURCE)(GLuint s, GLsizei n, const GLchar *const*src, const GLint *len);
static PFN_GLSHADERSOURCE p_glShaderSource;
void ml_glShaderSource(GLuint s, GLsizei n, const GLchar *const*src, const GLint *len) {
    if (!p_glShaderSource)
        p_glShaderSource = (PFN_GLSHADERSOURCE)wglGetProcAddress("glShaderSource");
    p_glShaderSource(s, n, src, len);
}

typedef void (APIENTRY *PFN_GLUNIFORM1F)(GLint loc, GLfloat v0);
static PFN_GLUNIFORM1F p_glUniform1f;
void ml_glUniform1f(GLint loc, GLfloat v0) {
    if (!p_glUniform1f)
        p_glUniform1f = (PFN_GLUNIFORM1F)wglGetProcAddress("glUniform1f");
    p_glUniform1f(loc, v0);
}

typedef void (APIENTRY *PFN_GLUNIFORM1I)(GLint loc, GLint v0);
static PFN_GLUNIFORM1I p_glUniform1i;
void ml_glUniform1i(GLint loc, GLint v0) {
    if (!p_glUniform1i)
        p_glUniform1i = (PFN_GLUNIFORM1I)wglGetProcAddress("glUniform1i");
    p_glUniform1i(loc, v0);
}

typedef void (APIENTRY *PFN_GLUNIFORM3F)(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2);
static PFN_GLUNIFORM3F p_glUniform3f;
void ml_glUniform3f(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2) {
    if (!p_glUniform3f)
        p_glUniform3f = (PFN_GLUNIFORM3F)wglGetProcAddress("glUniform3f");
    p_glUniform3f(loc, v0, v1, v2);
}

typedef void (APIENTRY *PFN_GLUNIFORM4F)(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2, GLfloat v3);
static PFN_GLUNIFORM4F p_glUniform4f;
void ml_glUniform4f(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2, GLfloat v3) {
    if (!p_glUniform4f)
        p_glUniform4f = (PFN_GLUNIFORM4F)wglGetProcAddress("glUniform4f");
    p_glUniform4f(loc, v0, v1, v2, v3);
}

typedef void (APIENTRY *PFN_GLUNIFORMMATRIX4FV)(GLint loc, GLsizei n, GLboolean tr, const GLfloat *v);
static PFN_GLUNIFORMMATRIX4FV p_glUniformMatrix4fv;
void ml_glUniformMatrix4fv(GLint loc, GLsizei n, GLboolean tr, const GLfloat *v) {
    if (!p_glUniformMatrix4fv)
        p_glUniformMatrix4fv = (PFN_GLUNIFORMMATRIX4FV)wglGetProcAddress("glUniformMatrix4fv");
    p_glUniformMatrix4fv(loc, n, tr, v);
}

typedef GLboolean (APIENTRY *PFN_GLUNMAPBUFFER)(GLenum target);
static PFN_GLUNMAPBUFFER p_glUnmapBuffer;
GLboolean ml_glUnmapBuffer(GLenum target) {
    if (!p_glUnmapBuffer)
        p_glUnmapBuffer = (PFN_GLUNMAPBUFFER)wglGetProcAddress("glUnmapBuffer");
    return p_glUnmapBuffer(target);
}

typedef void (APIENTRY *PFN_GLUSEPROGRAM)(GLuint program);
static PFN_GLUSEPROGRAM p_glUseProgram;
void ml_glUseProgram(GLuint program) {
    if (!p_glUseProgram)
        p_glUseProgram = (PFN_GLUSEPROGRAM)wglGetProcAddress("glUseProgram");
    p_glUseProgram(program);
}

typedef void (APIENTRY *PFN_GLVERTEXATTRIBPOINTER)(GLuint i, GLint size, GLenum t, GLboolean n, GLsizei stride, const void *p);
static PFN_GLVERTEXATTRIBPOINTER p_glVertexAttribPointer;
void ml_glVertexAttribPointer(GLuint i, GLint size, GLenum t, GLboolean n, GLsizei stride, const void *p) {
    if (!p_glVertexAttribPointer)
        p_glVertexAttribPointer = (PFN_GLVERTEXATTRIBPOINTER)wglGetProcAddress("glVertexAttribPointer");
    p_glVertexAttribPointer(i, size, t, n, stride, p);
}

#endif /* _WIN32 */
