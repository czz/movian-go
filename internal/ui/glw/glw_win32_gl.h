#ifndef GLW_WIN32_GL_H
#define GLW_WIN32_GL_H

/* GL 1.2+/extension entry points on Windows — opengl32.dll exports
 * only GL 1.1; the rest is resolved through wglGetProcAddress
 * (the windows counterpart of glXGetProcAddress). Preambles that
 * call C.gl<Ext>* on _WIN32 include this header so the call sites
 * stay identical to the unix ones. */

void ml_glActiveTexture(GLenum texture);
#define glActiveTexture ml_glActiveTexture
void ml_glAttachShader(GLuint program, GLuint shader);
#define glAttachShader ml_glAttachShader
void ml_glBindAttribLocation(GLuint program, GLuint index, const GLchar *name);
#define glBindAttribLocation ml_glBindAttribLocation
void ml_glBindBuffer(GLenum target, GLuint buffer);
#define glBindBuffer ml_glBindBuffer
void ml_glBindFramebufferEXT(GLenum target, GLuint framebuffer);
#define glBindFramebufferEXT ml_glBindFramebufferEXT
void ml_glBlendFuncSeparate(GLenum sf, GLenum df, GLenum sa, GLenum da);
#define glBlendFuncSeparate ml_glBlendFuncSeparate
void ml_glBufferData(GLenum target, GLsizeiptr size, const void *data, GLenum usage);
#define glBufferData ml_glBufferData
void ml_glCompileShader(GLuint shader);
#define glCompileShader ml_glCompileShader
GLuint ml_glCreateProgram(void);
#define glCreateProgram ml_glCreateProgram
GLuint ml_glCreateShader(GLenum type);
#define glCreateShader ml_glCreateShader
void ml_glDeleteBuffers(GLsizei n, const GLuint *buffers);
#define glDeleteBuffers ml_glDeleteBuffers
void ml_glDeleteFramebuffersEXT(GLsizei n, const GLuint *framebuffers);
#define glDeleteFramebuffersEXT ml_glDeleteFramebuffersEXT
void ml_glDeleteProgram(GLuint program);
#define glDeleteProgram ml_glDeleteProgram
void ml_glDeleteShader(GLuint shader);
#define glDeleteShader ml_glDeleteShader
void ml_glDisableVertexAttribArray(GLuint index);
#define glDisableVertexAttribArray ml_glDisableVertexAttribArray
void ml_glEnableVertexAttribArray(GLuint index);
#define glEnableVertexAttribArray ml_glEnableVertexAttribArray
void ml_glFramebufferTexture2DEXT(GLenum t, GLenum a, GLenum tt, GLuint tex, GLint level);
#define glFramebufferTexture2DEXT ml_glFramebufferTexture2DEXT
void ml_glGenBuffers(GLsizei n, GLuint *buffers);
#define glGenBuffers ml_glGenBuffers
void ml_glGenFramebuffersEXT(GLsizei n, GLuint *framebuffers);
#define glGenFramebuffersEXT ml_glGenFramebuffersEXT
void ml_glGetProgramInfoLog(GLuint p, GLsizei bs, GLsizei *len, GLchar *log);
#define glGetProgramInfoLog ml_glGetProgramInfoLog
void ml_glGetProgramiv(GLuint p, GLenum pname, GLint *params);
#define glGetProgramiv ml_glGetProgramiv
void ml_glGetShaderInfoLog(GLuint s, GLsizei bs, GLsizei *len, GLchar *log);
#define glGetShaderInfoLog ml_glGetShaderInfoLog
void ml_glGetShaderiv(GLuint s, GLenum pname, GLint *params);
#define glGetShaderiv ml_glGetShaderiv
GLint ml_glGetUniformLocation(GLuint p, const GLchar *name);
#define glGetUniformLocation ml_glGetUniformLocation
void ml_glLinkProgram(GLuint program);
#define glLinkProgram ml_glLinkProgram
void * ml_glMapBuffer(GLenum target, GLenum access);
#define glMapBuffer ml_glMapBuffer
void ml_glShaderSource(GLuint s, GLsizei n, const GLchar *const*src, const GLint *len);
#define glShaderSource ml_glShaderSource
void ml_glUniform1f(GLint loc, GLfloat v0);
#define glUniform1f ml_glUniform1f
void ml_glUniform1i(GLint loc, GLint v0);
#define glUniform1i ml_glUniform1i
void ml_glUniform3f(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2);
#define glUniform3f ml_glUniform3f
void ml_glUniform4f(GLint loc, GLfloat v0, GLfloat v1, GLfloat v2, GLfloat v3);
#define glUniform4f ml_glUniform4f
void ml_glUniformMatrix4fv(GLint loc, GLsizei n, GLboolean tr, const GLfloat *v);
#define glUniformMatrix4fv ml_glUniformMatrix4fv
GLboolean ml_glUnmapBuffer(GLenum target);
#define glUnmapBuffer ml_glUnmapBuffer
void ml_glUseProgram(GLuint program);
#define glUseProgram ml_glUseProgram
void ml_glVertexAttribPointer(GLuint i, GLint size, GLenum t, GLboolean n, GLsizei stride, const void *p);
#define glVertexAttribPointer ml_glVertexAttribPointer

#endif
