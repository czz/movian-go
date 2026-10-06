//go:build !mgos

package kvstore

// mgosSkipUnimportant — C: #ifdef STOS in kvstore.c.
const mgosSkipUnimportant = false
