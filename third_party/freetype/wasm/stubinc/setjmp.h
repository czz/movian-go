#ifndef _SETJMP_H
#define _SETJMP_H
typedef struct __jmp_buf_tag { unsigned long __jb[8+1+128/4]; } jmp_buf[1];
__attribute__((returns_twice)) int setjmp(jmp_buf);
__attribute__((noreturn)) void longjmp(jmp_buf, int);
#endif
