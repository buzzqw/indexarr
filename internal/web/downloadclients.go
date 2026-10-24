package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/store"
)

// downloadClientKindLabels are product names: they are not translated.
var downloadClientKindLabels = map[string]string{
	"qbittorrent":  "qBittorrent",
	"transmission": "Transmission",
	"deluge":       "Deluge",
	"rtorrent":     "rTorrent",
	"sabnzbd":      "SABnzbd",
	"nzbget":       "NZBGet",
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
}

func (s *Server) downloadClientForm(r *http.Request, title string, client store.DownloadClient, isNew bool) downloadClientFormPage {
	return downloadClientFormPage{
		base:            s.newBaseT(r, title, "applications"),
		IsNew:           isNew,
		Client:          client,
		Kinds:           []string{"qbittorrent", "transmission", "deluge", "rtorrent", "sabnzbd", "nzbget"},
		KindName:        downloadClientKindLabel(client.Kind),
		HasPassword:     client.PasswordEnc != "",
		HasAPIKey:       client.APIKeyEnc != "",
		DefaultProtocol: store.DefaultDownloadClientProtocol(client.Kind),
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
	if in.Protocol != store.ProtocolTorrent && in.Protocol != store.ProtocolUsenet {
		problems = append(problems, s.translate(r, "client.error.protocol"))
	}
	parsed, err := url.Parse(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
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
	return in, problems
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
		if protocol != store.ProtocolUsenet {
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
