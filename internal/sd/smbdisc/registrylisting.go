package smbdisc

import "github.com/czz/movian-go/internal/service"

// registryListing lists the host entries as managed services in the
// service registry. The service is its handle.
type registryListing struct {
	ss *service.ServiceSystem
}

func (l registryListing) add(id, title, url string) listingHandle {
	// C: service_create_managed(id, name, url, "server", NULL, 0, 0,
	//    SVC_ORIGIN_DISCOVERED), as nmb.c and movian6's src/sd/wsd.c
	//    call it: probe off, not on the home screen.
	return l.ss.ServiceCreateManaged(id, title, url, "server", "",
		false, false, service.SvcOriginDiscovered)
}

func (l registryListing) setURL(h listingHandle, url string) {
	l.ss.ServiceSetURL(h.(*service.Service), url)
}

func (l registryListing) remove(h listingHandle) {
	l.ss.ServiceDestroy(h.(*service.Service))
}
