//go:build mgos

package kvstore

// mgosSkipUnimportant — C: #ifdef STOS in kvstore.c (deferred-write skip
// for KVSTORE_UNIMPORTANT entries).
const mgosSkipUnimportant = true
