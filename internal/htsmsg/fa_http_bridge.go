// fa_http_bridge.go — bridges fa_http.c's htsmsg dependencies into
// fileaccess/core without an import cycle (this package already imports
// fileaccess/core):
//
//   - cookie persistence — C: htsmsg_store_save/load("httpcookies")
//     (fa_http.c cookie_persist / load_cookies)
//   - WebDAV PROPFIND XML — C: htsmsg_xml_deserialize_buf
//     (fa_http.c dav_propfind)

package htsmsg

import (
	"math"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// HTTPCookieStoreSave — C: cookie_persist's htsmsg_create_list +
// htsmsg_store_save(m, "httpcookies").
func HTTPCookieStoreSave(storeDep any,
	records []fileaccesscore.HTTPCookieRecord) {
	s, _ := storeDep.(*Store)
	m := NewList()
	for _, r := range records {
		c := NewMap()
		c.AddStr("name", r.Name)
		c.AddStr("path", r.Path)
		c.AddStr("domain", r.Domain)
		c.AddStr("value", r.Value)
		c.AddS32("expire", clampExpire(r.Expire))
		if r.Secure {
			c.AddS32("secure", 1)
		}
		if r.HostOnly {
			c.AddS32("hostonly", 1)
		}
		if r.Imported {
			c.AddS32("imported", 1)
			c.AddStr("ua", r.UA)
			c.AddStr("scopeorigin", r.ScopeOrigin)
			c.AddStr("scopeowner", r.ScopeOwner)
		}
		m.AddMsg("", c)
	}
	if s != nil {
		s.Save(m, "httpcookies")
	}
	m.Release()
}

// clampExpire fits a Unix expiry into the store's signed 32-bit field;
// larger values (far-future Max-Age) keep the latest representable date
// instead of wrapping to the past.
func clampExpire(t int64) int32 {
	if t > math.MaxInt32 {
		return math.MaxInt32
	}
	if t < math.MinInt32 {
		return math.MinInt32
	}
	return int32(t)
}

// HTTPCookieStoreLoad — C: load_cookies htsmsg_store_load.
func HTTPCookieStoreLoad(storeDep any) []fileaccesscore.HTTPCookieRecord {
	s, _ := storeDep.(*Store)
	if s == nil {
		return nil
	}
	m, err := s.Load("httpcookies")
	if m == nil || err != nil {
		return nil
	}
	defer m.Release()

	var out []fileaccesscore.HTTPCookieRecord
	for _, f := range m.fields {
		o := f.GetMap()
		if o == nil {
			continue
		}
		var r fileaccesscore.HTTPCookieRecord
		r.Name = o.GetStr("name")
		r.Path = o.GetStr("path")
		r.Domain = o.GetStr("domain")
		r.Value = o.GetStr("value")
		exp := o.GetS32OrDefault("expire", -1)
		r.Expire = int64(exp)
		// Records written before these fields existed have none: false.
		r.Secure = o.GetS32OrDefault("secure", 0) != 0
		r.HostOnly = o.GetS32OrDefault("hostonly", 0) != 0
		if o.GetS32OrDefault("imported", 0) != 0 {
			r.Imported = true
			r.UA = o.GetStr("ua")
			r.ScopeOrigin = o.GetStr("scopeorigin")
			r.ScopeOwner = o.GetStr("scopeowner")
		}
		out = append(out, r)
	}
	return out
}

// DAVXMLDeserializeBridge — C: htsmsg_xml_deserialize_buf; converts the
// resulting message into the DAVXMLMap model consumed by parse_propfind.
func DAVXMLDeserializeBridge(buf []byte) (*fileaccesscore.DAVXMLMap, error) {
	m, err := DeserializeXMLBuf(buf)
	if err != nil {
		return nil, err
	}
	defer m.Release()
	return htsmsgToDAVXML(m), nil
}

// htsmsgToDAVXML converts an htsmsg map into the DAVXMLMap field model.
// An htsmsg_xml element field is HMF_MAP when childless/child-only and
// HMF_STR when it has text (children may still be retained); attributes
// are STR fields flagged HMF_XML_ATTRIBUTE.
func htsmsgToDAVXML(m *HTSMsg) *fileaccesscore.DAVXMLMap {
	if m == nil {
		return nil
	}
	out := &fileaccesscore.DAVXMLMap{}
	for _, f := range m.fields {
		df := &fileaccesscore.DAVXMLField{
			Name:   f.name,
			IsAttr: f.flags&HmfXmlAttribute != 0,
			Childs: htsmsgToDAVXML(f.childs),
		}
		if f.fieldType == HmfStr {
			df.Str = f.strValue
			df.HasStr = true
		}
		out.Fields = append(out.Fields, df)
	}
	return out
}
