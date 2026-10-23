package simmock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// SABnzbd

func (s *Server) handleSABnzbd(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		mode := strings.ToLower(r.URL.Query().Get("mode"))
		if mode == "" {
			mode = strings.ToLower(r.URL.Query().Get("t"))
		}
		if mode == "version" {
			writeJSON(w, http.StatusOK, map[string]string{"version": "4.3.0-sim"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "error": "unsupported mode"})
		return
	}

	contentType := r.Header.Get("Content-Type")
	record := AddRecord{Kind: "sabnzbd"}
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "error": "invalid multipart form"})
			return
		}
		record.Mode = r.FormValue("mode")
		record.Title = r.FormValue("nzbname")
		record.Category = r.FormValue("cat")
		if file, header, err := r.FormFile("name"); err == nil {
			defer file.Close()
			body, _ := io.ReadAll(io.LimitReader(file, 4<<20))
			record.Name = header.Filename
			record.Bytes = len(body)
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "error": "invalid form"})
			return
		}
		record.Mode = r.FormValue("mode")
		record.Title = r.FormValue("nzbname")
		record.Category = r.FormValue("cat")
		record.URL = r.FormValue("name")
		record.Name = record.Title
	}

	if record.Mode != "addurl" && record.Mode != "addfile" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "error": "unsupported mode"})
		return
	}

	s.mu.Lock()
	s.sabCount++
	record.Name = strings.TrimSpace(record.Name)
	s.state.Sabnzbd = append(s.state.Sabnzbd, record)
	count := s.sabCount
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   true,
		"nzo_ids":  []string{fmt.Sprintf("SABnzbd_nzo_sim_%d", count)},
		"nzb_name": record.Title,
	})
}

// ---------------------------------------------------------------------------
// NZBGet

type rpcRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     int               `json:"id"`
}

type rpcResponse struct {
	Result any `json:"result"`
	Error  any `json:"error"`
	ID     int `json:"id"`
}

func (s *Server) handleNZBGet(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Result: nil, Error: map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	id := req.ID
	if id == 0 {
		id = 1
	}

	switch req.Method {
	case "version":
		writeJSON(w, http.StatusOK, rpcResponse{Result: "21.1-sim", Error: nil, ID: id})
	case "append", "appendurl":
		record := AddRecord{Kind: "nzbget", Mode: req.Method}
		if len(req.Params) > 0 {
			record.Name = rawString(req.Params[0])
		}
		if req.Method == "appendurl" {
			if len(req.Params) > 1 {
				record.URL = rawString(req.Params[1])
			}
		} else if len(req.Params) > 1 {
			if decoded, err := base64.StdEncoding.DecodeString(rawString(req.Params[1])); err == nil {
				record.Bytes = len(decoded)
			}
		}
		if len(req.Params) > 2 {
			record.Category = rawString(req.Params[2])
		}
		if len(req.Params) > 4 {
			var paused bool
			_ = json.Unmarshal(req.Params[4], &paused)
			record.Paused = paused
		}
		s.mu.Lock()
		s.nzbgetCount++
		s.state.NZBGet = append(s.state.NZBGet, record)
		result := s.nzbgetCount
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, rpcResponse{Result: result, Error: nil, ID: id})
	default:
		writeJSON(w, http.StatusOK, rpcResponse{
			Result: nil,
			Error:  map[string]any{"code": -32601, "message": "unknown method: " + req.Method},
			ID:     id,
		})
	}
}

func rawString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// ---------------------------------------------------------------------------
// Deluge

func (s *Server) handleDeluge(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Result: nil, Error: map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	id := req.ID
	if id == 0 {
		id = 1
	}

	authenticated := false
	if cookie, err := r.Cookie("_session_id"); err == nil && cookie.Value == "demo-deluge-session" {
		authenticated = true
	}

	switch req.Method {
	case "auth.login":
		username, password := "", ""
		if len(req.Params) > 0 {
			username = rawString(req.Params[0])
		}
		if len(req.Params) > 1 {
			password = rawString(req.Params[1])
		}
		ok := username == "demo" && password == "demo"
		if ok {
			http.SetCookie(w, &http.Cookie{
				Name:     "_session_id",
				Value:    "demo-deluge-session",
				Path:     "/",
				HttpOnly: true,
			})
		}
		writeJSON(w, http.StatusOK, rpcResponse{Result: ok, Error: nil, ID: id})
	case "auth.check_session":
		writeJSON(w, http.StatusOK, rpcResponse{Result: authenticated, Error: nil, ID: id})
	case "core.get_session_state":
		writeJSON(w, http.StatusOK, rpcResponse{Result: []string{"demo-deluge-hash"}, Error: nil, ID: id})
	case "core.add_torrent_magnet", "core.add_torrent_file", "core.add_torrent_url":
		if !authenticated {
			writeJSON(w, http.StatusUnauthorized, rpcResponse{
				Result: nil,
				Error:  map[string]any{"code": 1, "message": "not authenticated"},
				ID:     id,
			})
			return
		}
		record := AddRecord{Kind: "deluge", Mode: req.Method}
		switch req.Method {
		case "core.add_torrent_magnet":
			if len(req.Params) > 0 {
				record.Magnet = rawString(req.Params[0])
			}
		case "core.add_torrent_url":
			if len(req.Params) > 0 {
				record.URL = rawString(req.Params[0])
			}
		case "core.add_torrent_file":
			if len(req.Params) > 0 {
				record.Name = rawString(req.Params[0])
			}
			if len(req.Params) > 1 {
				if decoded, err := base64.StdEncoding.DecodeString(rawString(req.Params[1])); err == nil {
					record.Bytes = len(decoded)
				}
			}
		}
		s.mu.Lock()
		s.state.Deluge = append(s.state.Deluge, record)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, rpcResponse{Result: "demo-deluge-hash", Error: nil, ID: id})
	default:
		writeJSON(w, http.StatusOK, rpcResponse{
			Result: nil,
			Error:  map[string]any{"code": -32601, "message": "unknown method: " + req.Method},
			ID:     id,
		})
	}
}

// ---------------------------------------------------------------------------
// rTorrent (XML-RPC)

type xmlRPCCall struct {
	XMLName xml.Name `xml:"methodCall"`
	Method  string   `xml:"methodName"`
	Params  []struct {
		Value struct {
			String string `xml:"string"`
			Base64 string `xml:"base64"`
			I4     string `xml:"i4"`
		} `xml:"value"`
	} `xml:"params>param"`
}

func (s *Server) handleRTorrent(w http.ResponseWriter, r *http.Request) {
	body := readAll(r.Body)
	var call xmlRPCCall
	if err := xml.Unmarshal(body, &call); err != nil {
		writeXMLRPCFault(w, "invalid XML-RPC request")
		return
	}

	switch call.Method {
	case "system.listMethods":
		writeXMLRPCArray(w, []string{
			"system.listMethods", "load.start", "load.normal", "d.load.raw",
			"d.start", "d.directory.set", "d.name",
		})
	case "load.start", "load.normal":
		record := AddRecord{Kind: "rtorrent", Mode: call.Method}
		if len(call.Params) > 1 {
			uri := call.Params[1].Value.String
			if strings.HasPrefix(strings.ToLower(uri), "magnet:") {
				record.Magnet = uri
			} else {
				record.URL = uri
			}
		}
		s.mu.Lock()
		s.state.RTorrent = append(s.state.RTorrent, record)
		s.mu.Unlock()
		writeXMLRPCInt(w, 0)
	case "d.load.raw":
		record := AddRecord{Kind: "rtorrent", Mode: call.Method}
		if len(call.Params) > 1 {
			if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(call.Params[1].Value.Base64)); err == nil {
				record.Bytes = len(decoded)
			}
		}
		s.mu.Lock()
		s.state.RTorrent = append(s.state.RTorrent, record)
		s.mu.Unlock()
		writeXMLRPCString(w, "0123456789abcdef0123456789abcdef01234567")
	case "d.start", "d.directory.set":
		writeXMLRPCInt(w, 0)
	default:
		writeXMLRPCFault(w, "unknown method: "+call.Method)
	}
}

func xmlRPCHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
}

func writeXMLRPCString(w http.ResponseWriter, value string) {
	xmlRPCHeader(w)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodResponse><params><param><value><string>`)
	_ = xml.EscapeText(&b, []byte(value))
	b.WriteString(`</string></value></param></params></methodResponse>`)
	_, _ = w.Write(b.Bytes())
}

func writeXMLRPCInt(w http.ResponseWriter, value int) {
	xmlRPCHeader(w)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><i4>%d</i4></value></param></params></methodResponse>`, value)
}

func writeXMLRPCArray(w http.ResponseWriter, values []string) {
	xmlRPCHeader(w)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>`)
	for _, value := range values {
		b.WriteString(`<value><string>`)
		_ = xml.EscapeText(&b, []byte(value))
		b.WriteString(`</string></value>`)
	}
	b.WriteString(`</data></array></value></param></params></methodResponse>`)
	_, _ = w.Write(b.Bytes())
}

func writeXMLRPCFault(w http.ResponseWriter, message string) {
	xmlRPCHeader(w)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodResponse><fault><value><struct>`)
	b.WriteString(`<member><name>faultCode</name><value><i4>-32601</i4></value></member>`)
	b.WriteString(`<member><name>faultString</name><value><string>`)
	_ = xml.EscapeText(&b, []byte(message))
	b.WriteString(`</string></value></member></struct></value></fault></methodResponse>`)
	_, _ = w.Write(b.Bytes())
}
