package asyncio

// C: src/networking/asyncio_posix.c — SSL context API.
// C's asyncio_ssl_create_server/client/free return SSL_CTX* (asyncio.h
// + ENABLE_OPENSSL section). The Go port backs these with crypto/tls;
// handshake/read/write run through tls.Conn instead of OpenSSL's
// in-loop WANT_READ/WANT_WRITE state machine — documented divergence
// (see tcp.go tlsStartHandshake).

import "crypto/tls"

// C: void *asyncio_ssl_create_server (net_openssl.c path)
func SSLCreateServer(privateKeyFile, certFile string) (any,
	error) {
	cert, err := tls.LoadX509KeyPair(certFile, privateKeyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// C: void *asyncio_ssl_create_client (asyncio.h:96)
func SSLCreateClient() any {
	return &tls.Config{} // C: SSL_CTX* — Go uses *tls.Config
}

// C: void asyncio_ssl_free (asyncio.h:100)
func SSLFree(ctx any) {
	// C: SSL_CTX_free — *tls.Config needs no release
}
