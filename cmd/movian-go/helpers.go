package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"os"

	"github.com/czz/movian-go/internal/htsmsg"
)

// generateDeviceID — C: generate_device_id (main.c:304-332).
// Always refreshes gconf.running_instance from random bytes; keeps a
// device_id already set by the arch layer (linux_init's MAC-MD5);
// otherwise reuses the persisted htsmsg-store id, or generates and
// persists a new random one.
func generateDeviceID(gc *gconf.T, store *htsmsg.Store) string {
	rand.Read(gc.RunningInstance[:])

	if gc.DeviceID != "" {
		return gc.DeviceID
	}

	var s string
	if store != nil {
		s = store.GetStr("deviceid", "deviceid")
	}
	if s != "" {
		gc.DeviceID = s
		return s
	}

	d := make([]byte, 20)
	if _, err := rand.Read(d); err != nil {
		gc.DeviceID = fmt.Sprintf("fallback-%d", os.Getpid())
		return gc.DeviceID
	}
	uuid := hex.EncodeToString(d[:16])
	gc.DeviceID = uuid
	if store != nil {
		store.Set("deviceid", "deviceid", htsmsg.HmfStr, uuid)
	}
	return uuid
}
