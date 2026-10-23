package downloadclient

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// rtorrentClient speaks XML-RPC to rTorrent (usually /RPC2).
type rtorrentClient struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func newRTorrent(cfg Config) (*rtorrentClient, error) {
	parsed, err := parseBaseURL(cfg.URL, "rTorrent")
	if err != nil {
		return nil, err
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/RPC2"
	}
	return &rtorrentClient{
		cfg:      cfg,
		endpoint: parsed.String(),
		http:     newHTTPClient(nil),
	}, nil
}

func (c *rtorrentClient) Send(ctx context.Context, item Item) error {
	switch {
	case len(item.Torrent) > 0:
		raw, err := c.call(ctx, "d.load.raw", []rpcParam{
			rpcString(""),
			rpcBase64(item.Torrent),
		})
		if err != nil {
			return err
		}
		hash := strings.TrimSpace(raw.Text)
		if hash == "" {
			return fmt.Errorf("rTorrent d.load.raw returned no info hash")
		}
		if hash == "0" {
			// Older rTorrent releases return 0 instead of the info hash.
			// The torrent is loaded; there is no hash to start or configure.
			return nil
		}
		if c.cfg.SavePath != "" {
			if _, err := c.call(ctx, "d.directory.set", []rpcParam{
				rpcString(hash), rpcString(c.cfg.SavePath),
			}); err != nil {
				return err
			}
		}
		if !c.cfg.Paused {
			if _, err := c.call(ctx, "d.start", []rpcParam{rpcString(hash)}); err != nil {
				return err
			}
		}
		return nil
	case strings.TrimSpace(item.Magnet) != "":
		return c.load(ctx, item.Magnet)
	case strings.TrimSpace(item.URL) != "":
		return c.load(ctx, item.URL)
	default:
		return fmt.Errorf("release has neither a .torrent nor a magnet")
	}
}

func (c *rtorrentClient) load(ctx context.Context, uri string) error {
	// load.normal loads without starting, which is how rTorrent expresses a
	// paused add; load.start both loads and starts.
	method := "load.start"
	if c.cfg.Paused {
		method = "load.normal"
	}
	params := []rpcParam{rpcString(""), rpcString(uri)}
	if c.cfg.SavePath != "" {
		params = append(params, rpcString("d.directory.set="+c.cfg.SavePath))
	}
	_, err := c.call(ctx, method, params)
	return err
}

func (c *rtorrentClient) Test(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "system.listMethods", nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("rTorrent OK (%d methods)", len(raw.Items)), nil
}

type rpcParam struct {
	kind string
	text string
	data []byte
}

func rpcString(value string) rpcParam {
	return rpcParam{kind: "string", text: value}
}

func rpcBase64(value []byte) rpcParam {
	return rpcParam{kind: "base64", data: value}
}

func (p rpcParam) write(builder *strings.Builder) {
	builder.WriteString("<value>")
	switch p.kind {
	case "base64":
		builder.WriteString("<base64>")
		builder.WriteString(base64.StdEncoding.EncodeToString(p.data))
		builder.WriteString("</base64>")
	default:
		builder.WriteString("<string>")
		_ = xml.EscapeText(builder, []byte(p.text))
		builder.WriteString("</string>")
	}
	builder.WriteString("</value>")
}

func (c *rtorrentClient) call(ctx context.Context, method string, params []rpcParam) (rpcValue, error) {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><methodCall><methodName>`)
	_ = xml.EscapeText(&body, []byte(method))
	body.WriteString("</methodName><params>")
	for _, param := range params {
		body.WriteString("<param>")
		param.write(&body)
		body.WriteString("</param>")
	}
	body.WriteString("</params></methodCall>")

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(body.String()))
	if err != nil {
		return rpcValue{}, err
	}
	request.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if c.cfg.Username != "" {
		request.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return rpcValue{}, requestError("rTorrent "+method, err, c.endpoint, c.cfg.Password)
	}
	payload := readBody(response.Body, 4<<20)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return rpcValue{}, httpFailure("rTorrent "+method, response, payload, c.cfg.Password)
	}
	var document rpcMethodResponse
	if err := xml.Unmarshal(payload, &document); err != nil {
		return rpcValue{}, fmt.Errorf("rTorrent response: %w", err)
	}
	if document.Fault != nil {
		message := document.Fault.Value.member("faultString")
		if message == "" {
			message = "XML-RPC fault"
		}
		return rpcValue{}, fmt.Errorf("rTorrent %s: %s",
			method, redactText(message, maxErrorBody, c.cfg.Password))
	}
	if len(document.Params) == 0 {
		return rpcValue{}, fmt.Errorf("rTorrent %s: empty response", method)
	}
	return document.Params[0].Value, nil
}

type rpcValue struct {
	Kind    string
	Text    string
	Members map[string]rpcValue
	Items   []rpcValue
}

func (v rpcValue) member(name string) string {
	if member, ok := v.Members[name]; ok {
		return member.Text
	}
	return ""
}

func (v *rpcValue) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var direct strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch element := token.(type) {
		case xml.CharData:
			direct.Write(element)
		case xml.StartElement:
			switch element.Name.Local {
			case "string", "i4", "int", "i8", "boolean", "double", "dateTime.iso8601":
				var text string
				if err := decoder.DecodeElement(&text, &element); err != nil {
					return err
				}
				v.Kind = element.Name.Local
				v.Text = text
			case "base64":
				var text string
				if err := decoder.DecodeElement(&text, &element); err != nil {
					return err
				}
				v.Kind = "base64"
				v.Text = strings.TrimSpace(text)
			case "nil":
				v.Kind = "nil"
				if err := decoder.Skip(); err != nil {
					return err
				}
			case "struct":
				var structure struct {
					Members []struct {
						Name  string   `xml:"name"`
						Value rpcValue `xml:"value"`
					} `xml:"member"`
				}
				if err := decoder.DecodeElement(&structure, &element); err != nil {
					return err
				}
				v.Kind = "struct"
				v.Members = make(map[string]rpcValue, len(structure.Members))
				for _, member := range structure.Members {
					v.Members[member.Name] = member.Value
				}
			case "array":
				var array struct {
					Values []rpcValue `xml:"data>value"`
				}
				if err := decoder.DecodeElement(&array, &element); err != nil {
					return err
				}
				v.Kind = "array"
				v.Items = array.Values
			default:
				if err := decoder.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if element.Name == start.Name {
				if v.Kind == "" {
					v.Kind = "string"
					v.Text = strings.TrimSpace(direct.String())
				}
				return nil
			}
		}
	}
}

type rpcParamValue struct {
	Value rpcValue `xml:"value"`
}

type rpcMethodResponse struct {
	XMLName xml.Name        `xml:"methodResponse"`
	Params  []rpcParamValue `xml:"params>param"`
	Fault   *rpcParamValue  `xml:"fault"`
}
