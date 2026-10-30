package smbdisc

// NetBIOS host discovery (internal/fileaccess/smb/nmb.go) asks the
// master browser for its server list and reports here which servers
// the list gains and loses. NetBIOS gives a name only. It reports with
// its own mutex held, so the lock order is that mutex, then the engine's.

// NetBIOSFound reports that the master browser lists the server name
// in workgroup; the host table merges it.
func (e *Engine) NetBIOSFound(name, workgroup string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	e.table.netbiosFound(name, workgroup)
}

// NetBIOSLost reports that NetBIOS no longer lists the server name in
// workgroup.
func (e *Engine) NetBIOSLost(name, workgroup string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	e.table.netbiosLost(name, workgroup, e.clock.Now())
}
