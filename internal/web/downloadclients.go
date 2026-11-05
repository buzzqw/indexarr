package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

// downloadClientKindOrder is the order of the type selector. Gextto follows
// the other torrent clients because it speaks the same API.
var downloadClientKindOrder = []string{
	"qbittorrent", "transmission", "deluge", "rtorrent", "gextto",
	"sabnzbd", "nzbget", "amule", "torrentblackhole", "usenetblackhole",
	"aria2", "utorrent", "flood", "downloadstation", "nzbvortex", "hadouken",
}

// downloadClientKindDefault carries the standard values shown in gray in the
// form. They are examples, not saved values: an empty input keeps them as its
// placeholder.
type downloadClientKindDefault struct {
	Name     string
	URL      string
	URLKey   string
	Username string
	Category string
	SavePath string
}

// downloadClientKindDefaults are the usual addresses and folders of each
// client. When a client is local, the loopback address is the one that makes
// the form immediately useful; the operator changes the host for a NAS.
var downloadClientKindDefaults = map[string]downloadClientKindDefault{
	"qbittorrent":      {Name: "qBittorrent", URL: "http://127.0.0.1:8080", Username: "admin", Category: "indexarr", SavePath: "/downloads"},
	"transmission":     {Name: "Transmission", URL: "http://127.0.0.1:9091", Username: "admin", SavePath: "/downloads"},
	"deluge":           {Name: "Deluge", URL: "http://127.0.0.1:8112", SavePath: "/downloads"},
	"rtorrent":         {Name: "rTorrent", URL: "http://127.0.0.1:8080/RPC2", Username: "admin", SavePath: "/downloads"},
	"gextto":           {Name: "Gextto / gx-torrent", URL: "http://127.0.0.1:5000", Username: "admin", Category: "indexarr", SavePath: "/downloads"},
	"sabnzbd":          {Name: "SABnzbd", URL: "http://127.0.0.1:8080", Category: "indexarr", SavePath: "/downloads"},
	"nzbget":           {Name: "NZBGet", URL: "http://127.0.0.1:6789", Username: "nzbget", Category: "indexarr", SavePath: "/downloads"},
	"amule":            {Name: "aMule", URL: "http://127.0.0.1:4711"},
	"torrentblackhole": {Name: "Torrent Blackhole", URLKey: "client.urlNotUsed", SavePath: "/downloads/watch"},
	"usenetblackhole":  {Name: "Usenet Blackhole", URLKey: "client.urlNotUsed", SavePath: "/downloads/watch"},
	"aria2":            {Name: "Aria2", URL: "http://127.0.0.1:6800", SavePath: "/downloads"},
	"utorrent":         {Name: "uTorrent", URL: "http://127.0.0.1:8080/gui/", Username: "admin"},
	"flood":            {Name: "Flood", URL: "http://127.0.0.1:3000", Username: "admin", Category: "indexarr", SavePath: "/downloads"},
	"downloadstation":  {Name: "Synology Download Station", URL: "http://127.0.0.1:5000", Username: "admin", SavePath: "/downloads"},
	"nzbvortex":        {Name: "NZBVortex", URL: "http://127.0.0.1:4321", Category: "indexarr", SavePath: "/downloads"},
	"hadouken":         {Name: "Hadouken", URL: "http://127.0.0.1:7890", Username: "admin", Category: "indexarr", SavePath: "/downloads"},
}

// downloadClientKindLabels are product names: they are not translated.
var downloadClientKindLabels = map[string]string{
	"qbittorrent":      "qBittorrent",
	"transmission":     "Transmission",
	"deluge":           "Deluge",
	"rtorrent":         "rTorrent",
	"sabnzbd":          "SABnzbd",
	"nzbget":           "NZBGet",
	"amule":            "aMule",
	"torrentblackhole": "Torrent Blackhole",
	"usenetblackhole":  "Usenet Blackhole",
	"aria2":            "Aria2",
	"utorrent":         "uTorrent",
	"flood":            "Flood",
	"downloadstation":  "Synology Download Station",
	"nzbvortex":        "NZBVortex",
	"hadouken":         "Hadouken",
	"gextto":           "Gextto / gx-torrent",
}

// clientHelpFallback carries the English text of help keys that are not in the
// shipped catalogs yet. i18n.T wins as soon as a catalog has the key, so adding
// the translations later needs no template change.
var clientHelpFallback = map[string]string{
	"client.help.torrentblackhole": "Writes each .torrent file, or a .magnet file when only a magnet exists, into the save path. Leave the URL empty.",
	"client.help.usenetblackhole":  "Writes each .nzb file into the save path. Leave the URL empty.",
	"client.help.aria2":            "Uses JSON-RPC at /jsonrpc. Put the RPC secret in the password or API key field; the save path becomes aria2's dir option and Add paused sets pause=true.",
	"client.help.utorrent":         "Uses the Web UI under /gui and fetches a token from token.html. Enter the Web UI address plus username and password; save path and category are not used.",
	"client.help.flood":            "Uses the REST API under /api. Enter its address, username and password; the save path becomes destination and the category becomes a tag.",
	"client.help.downloadstation":  "Logs in through auth.cgi and creates tasks through task.cgi. Enter the NAS address, username and password; the save path becomes destination. Category is not used.",
	"client.help.nzbvortex":        "Uses the API under /api and an API key. It accepts .nzb files only; the category becomes the group name.",
	"client.help.hadouken":         "Uses JSON-RPC under /api. Enter its address, username and password; the category becomes the label.",
	"client.help.gextto":           "Uses the qBittorrent-compatible /api/v2 endpoint served by gx-torrent and Gextto. Enter the daemon address; leave the password empty when the Web UI is open on the LAN, or use an API key when one is configured.",
	"client.urlNotUsed":            "not used",
	"client.help.url":              "Blackhole leaves this empty. The other clients use the address of their API; the gray example is the standard one for the selected client.",
	"client.help.savePath":         "Blackhole: the folder that receives the files. Aria2: the dir option. Flood and Download Station: the destination. Other clients ignore it.",
	"client.help.apiKey":           "Aria2 accepts its RPC secret here or in the password field. SABnzbd, NZBGet and NZBVortex use the API key.",
	"client.help.category":         "Flood uses it as a tag, NZBVortex as a group and Hadouken as a label. Other clients ignore it.",
	"client.help.paused":           "Aria2 and Flood honour it; the other clients may keep their own default.",
	"client.error.savePath":        "Blackhole needs a save folder.",
}

func blackholeKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "torrentblackhole", "usenetblackhole":
		return true
	}
	return false
}

func clientHelpText(lang i18n.Language, key string) string {
	text := i18n.T(lang, key)
	if text != key {
		return text
	}
	if fallback, ok := clientHelpFallback[key]; ok {
		return fallback
	}
	return text
}

func downloadClientKindLabel(kind string) string {
	if label, ok := downloadClientKindLabels[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return label
	}
	return kind
}

func validDownloadClientKind(kind string) bool {
	_, ok := downloadClientKindLabels[strings.ToLower(strings.TrimSpace(kind))]
	return ok
}

type downloadClientRow struct {
	Client      store.DownloadClient
	KindLabel   string
	Host        string
	HasSecret   bool
	ProtocolKey string
}

// downloadClientKindOption is one row of the type selector, with the gray
// examples the form shows when the operator picks it.
type downloadClientKindOption struct {
	Value               string
	Label               string
	NamePlaceholder     string
	URLPlaceholder      string
	UsernamePlaceholder string
	CategoryPlaceholder string
	SavePathPlaceholder string
}

func downloadClientKindOptions(lang i18n.Language) []downloadClientKindOption {
	options := make([]downloadClientKindOption, 0, len(downloadClientKindOrder))
	for _, kind := range downloadClientKindOrder {
		defaults := downloadClientKindDefaults[kind]
		url := defaults.URL
		if defaults.URLKey != "" {
			url = clientHelpText(lang, defaults.URLKey)
		}
		options = append(options, downloadClientKindOption{
			Value:               kind,
			Label:               downloadClientKindLabel(kind),
			NamePlaceholder:     defaults.Name,
			URLPlaceholder:      url,
			UsernamePlaceholder: defaults.Username,
			CategoryPlaceholder: defaults.Category,
			SavePathPlaceholder: defaults.SavePath,
		})
	}
	return options
}

type downloadClientFormPage struct {
	base
	IsNew           bool
	Client          store.DownloadClient
	Kinds           []downloadClientKindOption
	Defaults        downloadClientKindOption
	KindName        string
	HasPassword     bool
	HasAPIKey       bool
	DefaultProtocol string
	URLRequired     bool
}

func (s *Server) downloadClientForm(r *http.Request, title string, client store.DownloadClient, isNew bool) downloadClientFormPage {
	kinds := downloadClientKindOptions(requestLanguage(r))
	defaults := kinds[0]
	for _, option := range kinds {
		if option.Value == client.Kind {
			defaults = option
			break
		}
	}
	return downloadClientFormPage{
		base:            s.newBaseT(r, title, "applications"),
		IsNew:           isNew,
		Client:          client,
		Kinds:           kinds,
		Defaults:        defaults,
		KindName:        downloadClientKindLabel(client.Kind),
		HasPassword:     client.PasswordEnc != "",
		HasAPIKey:       client.APIKeyEnc != "",
		DefaultProtocol: store.DefaultDownloadClientProtocol(client.Kind),
		URLRequired:     !blackholeKind(client.Kind),
	}
}

func (s *Server) downloadClientsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/applications#clients", http.StatusFound)
}

func (s *Server) downloadClientNew(w http.ResponseWriter, r *http.Request) {
	client := store.DownloadClient{
		Kind: "qbittorrent", Protocol: "torrent", Enabled: true,
		Name: downloadClientKindLabel("qbittorrent"),
	}
	s.render(w, r, "download_client_form", s.downloadClientForm(r, "client.titleNew", client, true), http.StatusOK)
}

func (s *Server) downloadClientEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	client, err := s.store.GetDownloadClient(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, err.Error())
		return
	}
	s.render(w, r, "download_client_form", s.downloadClientForm(r, "client.titleEdit", client, false), http.StatusOK)
}

// downloadClientInput reads the non-secret fields and keeps the stored secrets
// when the fields are empty.
func (s *Server) downloadClientInput(r *http.Request, current store.DownloadClient) (store.DownloadClient, []string) {
	in := current
	in.Name = strings.TrimSpace(r.FormValue("name"))
	in.Kind = strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	in.Protocol = strings.ToLower(strings.TrimSpace(r.FormValue("protocol")))
	in.URL = strings.TrimRight(strings.TrimSpace(r.FormValue("url")), "/")
	in.Username = strings.TrimSpace(r.FormValue("username"))
	in.Category = strings.TrimSpace(r.FormValue("category"))
	in.SavePath = strings.TrimSpace(r.FormValue("save_path"))
	in.Paused = r.FormValue("paused") != ""
	in.Enabled = r.FormValue("enabled") != ""
	in.IsDefault = r.FormValue("is_default") != ""
	if in.Protocol == "" {
		in.Protocol = store.DefaultDownloadClientProtocol(in.Kind)
	}
	var problems []string
	if in.Name == "" {
		problems = append(problems, s.translate(r, "client.error.name"))
	}
	if !validDownloadClientKind(in.Kind) {
		problems = append(problems, s.translate(r, "client.error.kind"))
	}
	if in.Protocol != store.ProtocolTorrent && in.Protocol != store.ProtocolUsenet && in.Protocol != store.ProtocolEd2k {
		problems = append(problems, s.translate(r, "client.error.protocol"))
	}
	parsed, err := url.Parse(in.URL)
	if blackholeKind(in.Kind) {
		// Blackhole writes into SavePath and needs no address. If one is
		// entered anyway, it must still be a valid http(s) address.
		if in.SavePath == "" {
			problems = append(problems, s.clientHelp(r, "client.error.savePath"))
		}
		if strings.TrimSpace(in.URL) != "" && (err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "") {
			problems = append(problems, s.translate(r, "client.error.url"))
		}
	} else if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		problems = append(problems, s.translate(r, "client.error.url"))
	}
	// Secrets are sealed only when the operator enters a new value.
	if password := r.FormValue("password"); password != "" {
		sealed, err := s.sealSecret(password)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			in.PasswordEnc = sealed
		}
	}
	if r.FormValue("password_remove") != "" {
		in.PasswordEnc = ""
		in.ClearPassword = true
	}
	if apiKey := strings.TrimSpace(r.FormValue("api_key")); apiKey != "" {
		sealed, err := s.sealSecret(apiKey)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			in.APIKeyEnc = sealed
		}
	}
	if r.FormValue("api_key_remove") != "" {
		in.APIKeyEnc = ""
		in.ClearAPIKey = true
	}
	// aMule authenticates with the Web UI password only; without one the
	// client cannot even log in.
	if in.Kind == "amule" && in.PasswordEnc == "" && strings.TrimSpace(r.FormValue("password")) == "" {
		problems = append(problems, s.translate(r, "client.error.amulePassword"))
	}
	return in, problems
}

func (s *Server) clientHelp(r *http.Request, key string) string {
	return clientHelpText(requestLanguage(r), key)
}

func (s *Server) sealSecret(value string) (string, error) {
	if s.vault == nil {
		return "", fmt.Errorf("secrets vault unavailable")
	}
	sealed, err := s.vault.SealString(value)
	if err != nil {
		return "", err
	}
	return sealed, nil
}

func (s *Server) openSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if s.vault == nil {
		return "", fmt.Errorf("secrets vault unavailable")
	}
	return s.vault.OpenString(value)
}

func (s *Server) downloadClientCreate(w http.ResponseWriter, r *http.Request) {
	in, problems := s.downloadClientInput(r, store.DownloadClient{Enabled: true})
	if len(problems) > 0 {
		page := s.downloadClientForm(r, "client.titleNew", in, true)
		page.Error = strings.Join(problems, " ")
		s.render(w, r, "download_client_form", page, http.StatusBadRequest)
		return
	}
	if _, err := s.store.CreateDownloadClient(r.Context(), in); err != nil {
		page := s.downloadClientForm(r, "client.titleNew", in, true)
		page.Error = err.Error()
		s.render(w, r, "download_client_form", page, http.StatusBadRequest)
		return
	}
	redirect(w, r, "/applications#clients", s.translate(r, "client.created", in.Name), "")
}

func (s *Server) downloadClientUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	current, err := s.store.GetDownloadClient(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	in, problems := s.downloadClientInput(r, current)
	in.ID = id
	if len(problems) > 0 {
		page := s.downloadClientForm(r, "client.titleEdit", in, false)
		page.Error = strings.Join(problems, " ")
		s.render(w, r, "download_client_form", page, http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateDownloadClient(r.Context(), in); err != nil {
		page := s.downloadClientForm(r, "client.titleEdit", in, false)
		page.Error = err.Error()
		s.render(w, r, "download_client_form", page, http.StatusBadRequest)
		return
	}
	redirect(w, r, "/applications#clients", s.translate(r, "client.saved", in.Name), "")
}

func (s *Server) downloadClientDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	if err := s.store.DeleteDownloadClient(r.Context(), id); err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	redirect(w, r, "/applications#clients", s.translate(r, "client.deleted"), "")
}

func (s *Server) downloadClientDefault(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	if err := s.store.SetDefaultDownloadClient(r.Context(), id); err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	redirect(w, r, "/applications#clients", s.translate(r, "client.defaultSet"), "")
}

// downloadClientTestForm tests the values currently typed in the new/edit
// form, before they are saved. It accepts the same fields as the create form;
// the hidden id, when present, preserves the stored secrets that the empty
// password and API-key fields mean to keep.
func (s *Server) downloadClientTestForm(w http.ResponseWriter, r *http.Request) {
	current := store.DownloadClient{Enabled: true}
	if raw := strings.TrimSpace(r.FormValue("id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			if stored, err := s.store.GetDownloadClient(r.Context(), id); err == nil {
				current = stored
			}
		}
	}
	isNew := current.ID == 0
	title := "client.titleNew"
	if !isNew {
		title = "client.titleEdit"
	}
	cfg, problems := s.downloadClientTestConfig(r, current)
	message := ""
	ok := false
	status := http.StatusOK
	if len(problems) > 0 {
		message = strings.Join(problems, " ")
		status = http.StatusBadRequest
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		description, err := downloadclient.Test(ctx, cfg)
		if err != nil {
			s.log.Warn("download client form test failed", "kind", cfg.Kind, "error", err)
			message = s.translate(r, "client.testFailed", err.Error())
		} else {
			ok = true
			message = s.translate(r, "client.testOK", description)
		}
	}

	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "message": message})
		return
	}

	in := s.downloadClientFormValues(r, current)
	page := s.downloadClientForm(r, title, in, isNew)
	if ok {
		page.Message = message
	} else {
		page.Error = message
	}
	s.render(w, r, "download_client_form", page, status)
}

// downloadClientFormValues keeps the non-secret fields the operator typed so a
// server-rendered test result shows the same form. Secrets are never copied
// back into the HTML: the stored markers come from the saved client.
func (s *Server) downloadClientFormValues(r *http.Request, current store.DownloadClient) store.DownloadClient {
	in := current
	in.Name = strings.TrimSpace(r.FormValue("name"))
	in.Kind = strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	in.Protocol = strings.ToLower(strings.TrimSpace(r.FormValue("protocol")))
	in.URL = strings.TrimRight(strings.TrimSpace(r.FormValue("url")), "/")
	in.Username = strings.TrimSpace(r.FormValue("username"))
	in.Category = strings.TrimSpace(r.FormValue("category"))
	in.SavePath = strings.TrimSpace(r.FormValue("save_path"))
	in.Paused = r.FormValue("paused") != ""
	in.Enabled = r.FormValue("enabled") != ""
	in.IsDefault = r.FormValue("is_default") != ""
	if in.Protocol == "" {
		in.Protocol = store.DefaultDownloadClientProtocol(in.Kind)
	}
	return in
}

// downloadClientTestConfig builds the configuration from the form without
// sealing or saving the secrets. An empty field means "use the stored value".
func (s *Server) downloadClientTestConfig(r *http.Request, current store.DownloadClient) (downloadclient.Config, []string) {
	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	rawURL := strings.TrimRight(strings.TrimSpace(r.FormValue("url")), "/")
	var problems []string
	if !validDownloadClientKind(kind) {
		problems = append(problems, s.translate(r, "client.error.kind"))
	}
	parsed, err := url.Parse(rawURL)
	if blackholeKind(kind) {
		if strings.TrimSpace(r.FormValue("save_path")) == "" {
			problems = append(problems, s.clientHelp(r, "client.error.savePath"))
		}
		if rawURL != "" && (err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "") {
			problems = append(problems, s.translate(r, "client.error.url"))
		}
	} else if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		problems = append(problems, s.translate(r, "client.error.url"))
	}

	password := r.FormValue("password")
	if password == "" && r.FormValue("password_remove") == "" && current.PasswordEnc != "" {
		opened, err := s.openSecret(current.PasswordEnc)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			password = opened
		}
	}
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	if apiKey == "" && r.FormValue("api_key_remove") == "" && current.APIKeyEnc != "" {
		opened, err := s.openSecret(current.APIKeyEnc)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			apiKey = opened
		}
	}
	if kind == "amule" && password == "" {
		problems = append(problems, s.translate(r, "client.error.amulePassword"))
	}

	return downloadclient.Config{
		Kind: kind, URL: rawURL,
		Username: strings.TrimSpace(r.FormValue("username")),
		Password: password, APIKey: apiKey,
		Category: strings.TrimSpace(r.FormValue("category")),
		SavePath: strings.TrimSpace(r.FormValue("save_path")),
		Paused:   r.FormValue("paused") != "",
	}, problems
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "fetch")
}

func (s *Server) downloadClientTest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	client, err := s.store.GetDownloadClient(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	cfg, err := s.downloadClientConfig(client)
	if err != nil {
		redirect(w, r, "/applications#clients", "", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	description, err := downloadclient.Test(ctx, cfg)
	if err != nil {
		s.log.Warn("download client test failed", "client", client.Name, "kind", client.Kind, "error", err)
		_ = s.store.RecordDownloadClientTest(r.Context(), client.ID, err.Error())
		redirect(w, r, "/applications#clients", "", s.translate(r, "client.testFailed", err.Error()))
		return
	}
	_ = s.store.RecordDownloadClientTest(r.Context(), client.ID, "")
	redirect(w, r, "/applications#clients", s.translate(r, "client.testOK", description), "")
}

// downloadClientConfig opens the sealed secrets only when a client is used.
func (s *Server) downloadClientConfig(client store.DownloadClient) (downloadclient.Config, error) {
	password, err := s.openSecret(client.PasswordEnc)
	if err != nil {
		return downloadclient.Config{}, err
	}
	apiKey, err := s.openSecret(client.APIKeyEnc)
	if err != nil {
		return downloadclient.Config{}, err
	}
	return downloadclient.Config{
		Kind: client.Kind, URL: client.URL, Username: client.Username,
		Password: password, APIKey: apiKey, Category: client.Category,
		SavePath: client.SavePath, Paused: client.Paused,
	}, nil
}

// resolveDownloadClient applies the protocol rule: torrent releases go to the
// torrent client, Usenet releases to the Usenet client. When no client of the
// protocol exists it falls back to the default, then to the legacy single
// setting so existing installations keep sending releases.
func (s *Server) resolveDownloadClient(ctx context.Context, protocol string) (downloadclient.Config, string, error) {
	return s.resolveDownloadClientFor(ctx, protocol, 0)
}

// resolveDownloadClientFor applies the per-indexer pin first: when the source
// that supplied the release names a client, that client wins, provided it is
// enabled and speaks the release protocol. Otherwise the protocol rule runs.
func (s *Server) resolveDownloadClientFor(ctx context.Context, protocol string, indexerID int64) (downloadclient.Config, string, error) {
	if indexerID > 0 {
		if indexer, err := s.store.GetIndexer(ctx, indexerID); err == nil && indexer.DownloadClientID > 0 {
			if client, err := s.store.GetDownloadClient(ctx, indexer.DownloadClientID); err == nil &&
				client.Enabled && strings.EqualFold(client.Protocol, protocol) {
				cfg, err := s.downloadClientConfig(client)
				if err == nil {
					name := client.Name
					if name == "" {
						name = downloadClientKindLabel(client.Kind)
					}
					return cfg, name, nil
				}
			}
		}
	}
	clients, err := s.store.ListDownloadClients(ctx)
	if err != nil {
		return downloadclient.Config{}, "", err
	}
	if len(clients) > 0 {
		protocol = strings.ToLower(strings.TrimSpace(protocol))
		if protocol != store.ProtocolUsenet && protocol != store.ProtocolEd2k {
			protocol = store.ProtocolTorrent
		}
		pick := store.DownloadClient{}
		for _, client := range clients {
			if !client.Enabled {
				continue
			}
			if client.Protocol == protocol {
				if pick.ID == 0 || client.IsDefault {
					pick = client
				}
				if client.IsDefault {
					break
				}
			}
		}
		// An eD2k release must never fall back to a torrent or Usenet client:
		// the client would receive an ed2k:// link it cannot use. Without an
		// aMule row the release page simply hides the send button.
		if protocol == store.ProtocolEd2k && pick.ID == 0 {
			return downloadclient.Config{}, "", fmt.Errorf("no eD2k download client configured")
		}
		if pick.ID == 0 {
			for _, client := range clients {
				if client.Enabled && client.IsDefault {
					pick = client
					break
				}
			}
		}
		if pick.ID == 0 {
			for _, client := range clients {
				if client.Enabled {
					pick = client
					break
				}
			}
		}
		if pick.ID == 0 {
			return downloadclient.Config{}, "", fmt.Errorf("no download client configured")
		}
		cfg, err := s.downloadClientConfig(pick)
		if err != nil {
			return downloadclient.Config{}, "", err
		}
		name := pick.Name
		if name == "" {
			name = downloadClientKindLabel(pick.Kind)
		}
		return cfg, name, nil
	}
	if strings.EqualFold(strings.TrimSpace(protocol), store.ProtocolEd2k) {
		return downloadclient.Config{}, "", fmt.Errorf("no eD2k download client configured")
	}
	// Legacy fallback: the single client in the settings table.
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return downloadclient.Config{}, "", err
	}
	if settings.DownloadClient == "" || settings.DownloadClient == "none" {
		return downloadclient.Config{}, "", fmt.Errorf("no download client configured")
	}
	return downloadclient.Config{
		Kind: settings.DownloadClient, URL: settings.DownloadClientURL,
		Username: settings.DownloadClientUser, Password: settings.DownloadClientPassword,
		Category: settings.DownloadClientCategory, SavePath: settings.DownloadClientSavePath,
		Paused: settings.DownloadClientPaused,
	}, downloadClientKindLabel(settings.DownloadClient), nil
}
