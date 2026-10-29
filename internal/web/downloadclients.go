package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

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
	"client.help.url":              "Blackhole leaves this empty. The other new clients use the address of their API, for example http://192.168.1.30:6800.",
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

type downloadClientFormPage struct {
	base
	IsNew           bool
	Client          store.DownloadClient
	Kinds           []string
	KindName        string
	HasPassword     bool
	HasAPIKey       bool
	DefaultProtocol string
	URLRequired     bool
}

func (s *Server) downloadClientForm(r *http.Request, title string, client store.DownloadClient, isNew bool) downloadClientFormPage {
	return downloadClientFormPage{
		base:            s.newBaseT(r, title, "applications"),
		IsNew:           isNew,
		Client:          client,
		Kinds:           []string{"qbittorrent", "transmission", "deluge", "rtorrent", "sabnzbd", "nzbget", "amule", "torrentblackhole", "usenetblackhole", "aria2", "utorrent", "flood", "downloadstation", "nzbvortex", "hadouken"},
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
	client := store.DownloadClient{Kind: "qbittorrent", Protocol: "torrent", Enabled: true}
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
		redirect(w, r, "/applications#clients", "", s.translate(r, "client.testFailed", err.Error()))
		return
	}
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
