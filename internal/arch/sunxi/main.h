/*
 * Minimal main.h for the vendored tlsf.c — C: src/main.h provides
 * TRACE() + hexdump(); tlsf uses them only in tlsf_check_heap's
 * integrity walker (sunxi never calls tlsf_check_heap).
 */
#pragma once
#include <stdio.h>
#include <ctype.h>

#define TRACE_ERROR 0
#define TRACE(level, tag, fmt, ...) \
	fprintf(stderr, "TLSF: " fmt "\n", ##__VA_ARGS__)

/* C: hexdump (misc/hexdump.c) — minimal equivalent for the tlsf
 * integrity-failure dump path. */
static inline void
hexdump(const char *pfx, const void *data_, int len)
{
	const unsigned char *data = data_;
	int i, j;
	for(i = 0; i < len; i += 16) {
		fprintf(stderr, "%s %04x: ", pfx, i);
		for(j = 0; j < 16 && i + j < len; j++)
			fprintf(stderr, "%02x ", data[i + j]);
		fprintf(stderr, "\n");
	}
}
