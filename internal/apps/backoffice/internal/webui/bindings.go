package webui

import (
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// BindingInvitationView contains pending metadata without a token or digest.
type BindingInvitationView struct {
	ID        string
	Version   uint64
	ExpiresAt time.Time
	Expired   bool
	Current   bool
}

// BindingReveal is the one-time issued value, excluded from serialization.
type BindingReveal struct {
	Payload   string       `json:"-"`
	Command   string       `json:"-"`
	DMCommand string       `json:"-"`
	BotURL    template.URL `json:"-"`
	ExpiresAt time.Time
}

// BindingForm is one configured channel's separate account-binding interface.
type BindingForm struct {
	ChannelType     string
	Integration     usercmd.BindingIntegration
	UserID          string
	UserVersion     uint64
	CSRFToken       string
	BotName         string
	BotUsername     string
	Endpoint        template.URL
	Ready           bool
	Disabled        bool
	CommandsEnabled bool
	Active          bool
	Pending         []BindingInvitationView
	Reveal          *BindingReveal
	Error           string
	Preview         bool
}

// ProjectBindingForm projects configured provider metadata and safe invitation state.
func ProjectBindingForm(info usercmd.BindingChannel, user usercmd.User, csrf string, pending []usercmd.BindingInvitation, now time.Time) BindingForm {
	form := BindingForm{ChannelType: info.Integration.ChannelType, Integration: info.Integration, UserID: user.ID, UserVersion: user.Version, CSRFToken: csrf, BotName: info.Name, BotUsername: info.BotUsername, Endpoint: channelURL(info.Integration.ChannelType, info.Endpoint), Ready: info.Integration.Key != "", Disabled: user.Status != usercmd.StatusActive, CommandsEnabled: info.CommandsEnabled}
	for _, invitation := range pending {
		if invitation.Integration.ChannelType != form.ChannelType {
			continue
		}
		item := BindingInvitationView{ID: invitation.ID, Version: invitation.Version, ExpiresAt: invitation.ExpiresAt, Expired: !now.Before(invitation.ExpiresAt), Current: invitation.Integration == info.Integration}
		form.Pending = append(form.Pending, item)
		if item.Current && !item.Expired {
			form.Active = true
		}
	}
	return form
}

// ProjectBindingReveal constructs transport-specific actions only from verified metadata.
func ProjectBindingReveal(form BindingForm, issued usercmd.IssuedBindingInvitation) BindingReveal {
	reveal := BindingReveal{Payload: issued.Payload, ExpiresAt: issued.Invitation.ExpiresAt}
	switch form.ChannelType {
	case "telegram":
		reveal.Command = "/start " + issued.Payload
		if form.BotUsername != "" {
			reveal.BotURL = channelURL(form.ChannelType, "https://t.me/"+url.PathEscape(form.BotUsername)+"?start="+url.QueryEscape(issued.Payload))
		}
	case "slackagent":
		reveal.Command = "/balda start " + issued.Payload
		reveal.BotURL = form.Endpoint
	case "zulip":
		reveal.Command = "/start " + issued.Payload
		reveal.BotURL = form.Endpoint
	case "mattermost":
		if form.CommandsEnabled {
			reveal.Command = "/balda start " + issued.Payload
		}
		if form.BotUsername != "" {
			reveal.DMCommand = "/msg @" + form.BotUsername + " " + issued.Payload
		}
		reveal.BotURL = form.Endpoint
	}
	return reveal
}

func channelURL(channel, raw string) template.URL {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme == "https" || parsed.Scheme == "http" {
		return template.URL(raw)
	} // #nosec G203 -- server-verified provider URL; scheme and authority checked.
	if channel == "slackagent" && parsed.Scheme == "slack" && parsed.Host == "user" && parsed.Path == "" && parsed.Query().Get("team") != "" && parsed.Query().Get("id") != "" && !strings.ContainsAny(raw, "\r\n") {
		return template.URL(raw) // #nosec G203 -- allow only the verified Slack user deep-link form.
	}
	return ""
}
