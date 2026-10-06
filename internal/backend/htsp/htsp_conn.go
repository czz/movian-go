// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"crypto/sha1"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/keyring"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: htsp_recv (htsp.c:198-226)
func htspRecv(hc *htspConnection) *htsmsg.HTSMsg {
	tc := hc.tc
	var lenb [4]byte

	if tc.TCPReadData(lenb[:], nil, nil) < 0 {
		return nil
	}

	l := uint32(lenb[0])<<24 | uint32(lenb[1])<<16 | uint32(lenb[2])<<8 | uint32(lenb[3])
	if l > 16*1024*1024 {
		return nil
	}

	// C: buf_t *buf = buf_create(l) — payload buffer; Go's
	// DeserializeBinary consumes the wire format including the
	// 4-byte length prefix, so assemble len-prefix + payload.
	buf := make([]byte, 4+int(l))

	copy(buf[:4], lenb[:])
	var m *htsmsg.HTSMsg
	if tc.TCPReadData(buf[4:], nil, nil) < 0 {
		m = nil
	} else {
		m, _ = htsmsg.DeserializeBinary(buf)
	}
	return m
}

// C: htsp_reqreply (htsp.c:232-363)
func htspReqreply(hc *htspConnection, m *htsmsg.HTSMsg) *htsmsg.HTSMsg {
	var reply *htsmsg.HTSMsg
	tc := hc.tc
	var hm *htspMsg
	retry := 0

	if tc == nil {
		return nil
	}

	// Generate a sequence number for our message
	seq := uint32(hc.seqGenerator.Add(1))
	m.AddU32("seq", seq)

	for {
		id := snprintf(100, "htsp://%s:%d", hc.hostname, hc.port)

		var username, password string
		flags := keyring.FlagShowRememberMe | keyring.FlagRememberMeSet
		if retry != 0 {
			flags |= keyring.FlagQueryUser
		}
		r := keyring.NotFound
		if hc.kr != nil {
			r = hc.kr.Lookup(id, &username, &password, nil, nil,
				"TV client", "Access denied", flags)
		}

		if r == -1 {
			// User rejected
			return nil
		}

		if r == 0 {
			// Got auth credentials
			m.DeleteField("username")
			m.DeleteField("digest")

			// C: if(username != NULL) — Go's setstr writes "" when
			// the keyring field is absent; NULL-ness is lost there.
			if username != "" {
				m.AddStr("username", username)
			}

			if password != "" {
				h := sha1.New()
				h.Write([]byte(password))
				h.Write(hc.challenge[:])
				d := h.Sum(nil)
				m.AddBin("digest", d)
			}
		}

		// C: htsmsg_binary_serialize(m, &buf, &len, -1) — C's -1 means
		// unlimited (size_t); Go requires a positive maxlen.
		buf, err := htsmsg.SerializeBinary(m, 1<<30)
		if err != nil {
			m.Release()
			return nil
		}

		if hc.isAsync {
			// Async, set up a struct that will be signalled when we
			// get a reply
			hm = &htspMsg{seq: seq}
			hc.rpcMutex.Lock()
			hc.rpcQueue = append(hc.rpcQueue, hm)
			hc.rpcMutex.Unlock()
		}

		if tc.TCPWriteData(buf) != 0 {
			m.Release()

			if hm != nil {
				hc.rpcMutex.Lock()
				htspMsgRemove(&hc.rpcQueue, hm)
				hc.rpcMutex.Unlock()
			}
			return nil
		}

		if hm != nil {
			hc.rpcMutex.Lock()
			for {
				if hm.error {
					htspMsgRemove(&hc.rpcQueue, hm)
					hc.rpcMutex.Unlock()
					m.Release()
					return nil
				}

				if hm.msg != nil {
					break
				}

				hc.rpcCond.Wait()
			}

			htspMsgRemove(&hc.rpcQueue, hm)
			hc.rpcMutex.Unlock()
			reply = hm.msg
		} else {
			reply = htspRecv(hc)
			if reply == nil {
				m.Release()
				return nil
			}
		}

		if v, err := reply.GetU32("noaccess"); err == nil && v != 0 {
			retry++
			continue // C: goto again
		}

		m.Release() // Destroy original message
		return reply
	}
}

// C: htsp_login (htsp.c:370-406)
func htspLogin(hc *htspConnection) int {
	m := htsmsg.NewMap()
	m.AddStr("clientname", app.AppNameUser)
	m.AddU32("htspversion", 1)
	m.AddStr("method", "hello")

	m = htspReqreply(hc, m)
	if m == nil {
		return -1
	}

	ch, err := m.GetBin("challenge")
	if err != nil || len(ch) != 32 {
		m.Release()
		return -1
	}
	copy(hc.challenge[:], ch)

	servername, ok := htsmsgGetStr(m, "servername")
	propSetStringOrVoid(hc.sys.pm, hc.serverName, servername, ok)

	m.Release()

	m = htsmsg.NewMap()
	m.AddStr("method", "login")
	m.AddU32("htspversion", htspProtoVersion)

	m = htspReqreply(hc, m)
	if m == nil {
		return -1
	}

	m.Release()

	return 0
}

// C: htsp_worker_thread (htsp.c:996-1065)
//
// We keep another thread for dispatching unsolicited (asynchronous)
// messages. Reason is that these messages may in turn cause additional
// inqueries to the HTSP server and we don't want to block the main input
// thread with this. Not to mention if the request needs to be retried
// with new authorization credentials.
func htspWorkerThread(hc *htspConnection) {
	for {
		hc.workerMutex.Lock()

		for len(hc.workerQueue) == 0 {
			hc.workerCond.Wait()
		}

		hm := hc.workerQueue[0]
		hc.workerQueue = hc.workerQueue[1:]
		hc.workerMutex.Unlock()

		m := hm.msg

		if m == nil {
			break
		}

		if method, ok := htsmsgGetStr(m, "method"); ok {
			switch method {
			case "channelAdd":
				htspChannelAddUpdate(hc, m, 1)
			case "channelUpdate":
				htspChannelAddUpdate(hc, m, 0)
			case "channelDelete":
				htspChannelDelete(hc, m)
			case "tagAdd":
				htspTagAddUpdate(hc, m, 1)
			case "tagUpdate":
				htspTagAddUpdate(hc, m, 0)
			case "tagDelete":
				htspTagDelete(hc, m)
			case "subscriptionStart":
				htspSubscriptionStart(hc, m)
			case "subscriptionStop":
				htspSubscriptionStop(hc, m)
			case "subscriptionStatus":
				htspSubscriptionStatus(hc, m)
			case "queueStatus":
				htspQueueStatus(hc, m)
			case "signalStatus":
				htspSignalStatus(hc, m)
			case "dvrEntryAdd":
				htspDvrEntryAdd(hc, m)
			case "dvrEntryUpdate":
				htspDvrEntryUpdate(hc, m)
			case "dvrEntryDelete":
				htspDvrEntryDelete(hc, m)
			case "timeshiftStatus":
				// nop for us
			case "initialSyncCompleted":
				// nop for us
			default:
				hc.sys.ts.Trace(trace.TRACE_INFO, "HTSP",
					"Unknown async method '%s' received", method)
				m.Print("HTSP INPUT")
			}
		}
		m.Release()
	}
}

// C: htsp_dispatch_disconnect (htsp.c:1071-1091)
func htspDispatchDisconnect(hc *htspConnection) {
	hc.rpcMutex.Lock()

	for _, hm := range hc.rpcQueue {
		hm.error = true
		hc.rpcCond.Broadcast()
	}
	hc.rpcMutex.Unlock()

	hc.subscriptionMutex.Lock()

	for _, hs := range hc.subscriptions {
		mediacore.MpEnqueueEvent(hs.mp, &mediacore.MediaEvent{
			Type: int(event.EVENT_EXIT),
			Data: &event.Event{Type: event.EVENT_EXIT},
		})
	}

	hc.subscriptionMutex.Unlock()
}

// C: htsp_msg_dispatch (htsp.c:1097-1151)
func htspMsgDispatch(hc *htspConnection, m *htsmsg.HTSMsg) int {
	// Grab streaming input at once
	if method, ok := htsmsgGetStr(m, "method"); ok && method == "muxpkt" {
		htspMuxInput(hc, m)
		m.Release()
		return 0
	}

	if seq, err := m.GetU32("seq"); err == nil && seq != 0 {
		// Reply ..
		hc.rpcMutex.Lock()
		var hm *htspMsg
		for _, x := range hc.rpcQueue {
			if seq == x.seq {
				hm = x
				break
			}
		}

		if hm != nil {
			hm.msg = m
			hc.rpcCond.Broadcast()
			m = nil
		} else {
			hc.rpcMutex.Unlock()
			m.Release()
			return -1
		}

		if m != nil {
			m.Release()
		}
		hc.rpcMutex.Unlock()

		return 0
	}

	// Unsolicited meta message
	// Async updates are sent to another worker thread
	hm := &htspMsg{msg: m}

	hc.workerMutex.Lock()
	hc.workerQueue = append(hc.workerQueue, hm)
	hc.workerCond.Signal()
	hc.workerMutex.Unlock()
	return 0
}

// C: htsp_thread (htsp.c:1157-1214)
func htspThread(hc *htspConnection) {
	for {
		m := htsmsg.NewMap()
		m.AddStr("method", "enableAsyncMetadata")
		m = htspReqreply(hc, m)
		if m == nil {
			return
		}
		m.Release()

		hc.isAsync = true

		for {
			m = htspRecv(hc)
			if m == nil {
				break
			}

			if htspMsgDispatch(hc, m) != 0 {
				break
			}
		}

		hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP", "Disconnected from %s:%d",
			hc.hostname, hc.port)

		hc.tc.TCPClose()
		hc.tc = nil
		hc.isAsync = false

		htspDispatchDisconnect(hc)

		for {
			var terr error
			hc.tc, terr = tcpcon.TCPConnect(hc.hostname, hc.port,
				3000, 0, nil)
			if hc.tc != nil {
				break
			}

			hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
				"Connection to %s:%d failed: %s",
				hc.hostname, hc.port, terr)
			time.Sleep(time.Second) // C: sleep(1)
			continue
		}

		hc.sys.ts.Trace(trace.TRACE_INFO, "HTSP", "Reconnected to %s:%d",
			hc.hostname, hc.port)

		tagDeleteAll(hc)
		channelDeleteAll(hc)
		htspLogin(hc)
	}
}

// C: htsp_connection_find (htsp.c:1266-1390)
func (sys *System) htspConnectionFind(url string, path []byte) (*htspConnection, error) {
	pm := sys.pm
	var port int
	var hostname [hostnameMax]byte

	misc.UrlSplit(nil, 0, nil, 0, hostname[:], len(hostname), &port,
		path, len(path), url)

	if port < 0 {
		port = 9982
	}

	sys.mu.Lock()

	hostnameStr := misc.CStr(hostname[:])
	for _, hc := range sys.connections {
		if hc.hostname == hostnameStr && hc.port == port {
			hc.refcount++
			sys.mu.Unlock()
			return hc, nil
		}
	}

	sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "Connecting to %s:%d",
		hostnameStr, port)

	tc, terr := tcpcon.TCPConnect(hostnameStr, port, 3000, 0, nil)
	if tc == nil {
		sys.mu.Unlock()
		sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
			"Connection to %s:%d failed: %s",
			hostnameStr, port, terr)
		return nil, terr
	}

	sys.ts.Trace(trace.TRACE_INFO, "HTSP", "Connected to %s:%d",
		hostnameStr, port)

	hc := &htspConnection{sys: sys, kr: sys.kr}

	hc.serverName = pm.CreateRoot("")

	// ---------

	hc.tagsModel = pm.CreateRoot("")
	hc.tagsNodes = pm.CreateEx(hc.tagsModel, "nodes", nil, false, false)
	pm.SetStringEx(pm.CreateEx(hc.tagsModel, "type", nil, false, false),
		nil, "directory", propcore.StringUTF8)
	meta := pm.CreateEx(hc.tagsModel, "metadata", nil, false, false)
	pm.SetStringEx(pm.CreateEx(meta, "title", nil, false, false),
		nil, "Channel groups", propcore.StringUTF8)

	// ---------

	hc.dvrNodes = pm.CreateRoot("")
	hc.dvrModel = pm.CreateRoot("")
	hc.dvrSorted = pm.CreateEx(hc.dvrModel, "nodes", nil, false, false)

	nf := propcore.PropNFCreate(hc.dvrSorted, hc.dvrNodes, nil,
		propcore.PropNFAutoDestroy)

	propcore.PropNFSort(nf, "node.metadata.start", true, 0, nil, false)
	nf.Release()

	// ---------

	hc.channelsNodes = pm.CreateRoot("")

	hc.channelsModel = pm.CreateRoot("")
	hc.channelsSorted = pm.CreateEx(hc.channelsModel, "nodes", nil, false, false)

	nf = propcore.PropNFCreate(hc.channelsSorted, hc.channelsNodes, nil,
		propcore.PropNFAutoDestroy)

	propcore.PropNFSort(nf, "node.metadata.channelNumber", false, 0,
		nil, false)
	nf.Release()

	pm.SetStringEx(pm.CreateEx(hc.channelsModel, "type", nil, false, false),
		nil, "directory", propcore.StringUTF8)
	meta = pm.CreateEx(hc.channelsModel, "metadata", nil, false, false)
	pm.SetStringEx(pm.CreateEx(meta, "title", nil, false, false),
		nil, "All channels", propcore.StringUTF8)
	pm.SetStringEx(pm.CreateEx(hc.channelsModel, "url", nil, false, false),
		nil, fmt.Sprintf("htsp://%s:%d/channels", hostnameStr, port),
		propcore.StringUTF8)
	pm.SetStringEx(pm.CreateEx(hc.channelsModel, "type", nil, false, false),
		nil, "directory", propcore.StringUTF8)

	hc.rpcCond = sync.NewCond(&hc.rpcMutex)

	hc.workerCond = sync.NewCond(&hc.workerMutex)

	hc.tc = tc
	hc.seqGenerator.Store(1)
	hc.sidGenerator.Store(1)
	hc.hostname = hostnameStr // C: strdup(hostname)
	hc.port = port

	hc.refcount = 1

	sys.connections = slices.Insert(sys.connections, 0, hc)

	// ---------
	makeRootModel(hc)

	htspLogin(hc)

	// C: hts_thread_create_detached("HTSP main", htsp_thread, hc, THREAD_PRIO_DEMUXER)
	go htspThread(hc)
	// C: hts_thread_create_detached("HTSP worker", htsp_worker_thread, hc, THREAD_PRIO_METADATA)
	go htspWorkerThread(hc)

	sys.mu.Unlock()
	return hc, nil
}

// C: htsp_set_subtitles (htsp.c:1558-1572)
func (sys *System) htspSetSubtitles(mp *mediacore.MediaPipe, id string, manual int) {
	pm := sys.pm
	if id == "off" {
		mp.Video.Stream2 = -1
		pm.SetStringEx(mp.PropSubtitleTrackCurrent, nil, "sub:off",
			propcore.StringUTF8)
	} else {
		idx := misc.Atoi(id)

		mp.Video.Stream2 = idx
		pm.SetStringEx(mp.PropSubtitleTrackCurrent, nil,
			fmt.Sprintf("sub:%d", idx), propcore.StringUTF8)
	}
	pm.SetIntEx(mp.PropSubtitleTrackCurrentManual, nil, manual)
}

// C: htsp_set_audio (htsp.c:1578-1592)
func (sys *System) htspSetAudio(mp *mediacore.MediaPipe, id string, byUser int) {
	pm := sys.pm
	if id == "off" {
		mp.Audio.Stream = -1
		pm.SetStringEx(mp.PropAudioTrackCurrent, nil, "audio:off",
			propcore.StringUTF8)
	} else {
		idx := misc.Atoi(id)

		mp.Audio.Stream = idx
		pm.SetStringEx(mp.PropAudioTrackCurrent, nil,
			fmt.Sprintf("audio:%d", idx), propcore.StringUTF8)
	}
	pm.SetIntEx(mp.PropAudioTrackCurrentManual, nil, byUser)
}
