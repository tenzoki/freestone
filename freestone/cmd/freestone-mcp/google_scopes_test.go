package main

import (
	"slices"
	"testing"

	"github.com/kai/freestone/internal/config"
)

func TestGoogleScopes(t *testing.T) {
	const (
		calendarList = "https://www.googleapis.com/auth/calendar.calendarlist.readonly"
		events       = "https://www.googleapis.com/auth/calendar.events"
		gmailModify  = "https://www.googleapis.com/auth/gmail.modify"
	)
	google := &config.GoogleProviderCfg{}
	cases := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{"calendar only", config.Config{Calendar: config.CalendarCfg{Google: google}}, []string{calendarList, events}},
		{"mail only", config.Config{Mail: config.MailCfg{Google: google}}, []string{gmailModify}},
		{"both", config.Config{Calendar: config.CalendarCfg{Google: google}, Mail: config.MailCfg{Google: google}}, []string{calendarList, events, gmailModify}},
		{"neither", config.Config{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := googleScopes(&tc.cfg); !slices.Equal(got, tc.want) {
				t.Errorf("googleScopes = %v, want %v", got, tc.want)
			}
		})
	}
}
