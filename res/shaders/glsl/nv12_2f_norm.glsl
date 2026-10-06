#ifdef GL_ES
precision highp float;
#endif

uniform sampler2D u_t0;  // Y  surface A
uniform sampler2D u_t1;  // UV surface A
uniform sampler2D u_t2;  // Y  surface B
uniform sampler2D u_t3;  // UV surface B
uniform mat4      u_colormtx;
uniform vec4      u_color;
uniform float     u_blend;

varying vec2 f_tex0;
varying vec2 f_tex1;

void main()
{
  vec3 rgb1 = vec3(u_colormtx * vec4(texture2D(u_t0, f_tex0).r,
				     texture2D(u_t1, f_tex0).r,
				     texture2D(u_t1, f_tex0).g,
				     1));

  vec3 rgb2 = vec3(u_colormtx * vec4(texture2D(u_t2, f_tex1).r,
				     texture2D(u_t3, f_tex1).r,
				     texture2D(u_t3, f_tex1).g,
				     1));

  gl_FragColor = vec4(mix(rgb2, rgb1, u_blend), u_color.a);
}
