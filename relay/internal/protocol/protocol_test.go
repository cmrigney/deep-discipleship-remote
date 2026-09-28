package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseAuth(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"ok controller", `{"type":"auth","password":"pw","role":"controller"}`, false},
		{"ok player", `{"type":"auth","password":"pw","role":"player","clientName":"Mac"}`, false},
		{"wrong type", `{"type":"play","password":"pw","role":"player"}`, true},
		{"bad role", `{"type":"auth","password":"pw","role":"admin"}`, true},
		{"empty password", `{"type":"auth","password":"","role":"player"}`, true},
		{"not json", `hello`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseAuth([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateController(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string // expected canonical JSON, "" to skip
		wantErr error
	}{
		{"play", `{"type":"play","id":"a1"}`, `{"type":"play","id":"a1"}`, nil},
		{"extra fields stripped", `{"type":"pause","id":"a1","evil":"x","seconds":5}`, `{"type":"pause","id":"a1"}`, nil},
		{"seek ok", `{"type":"seek_relative","id":"x","seconds":-10}`, `{"type":"seek_relative","id":"x","seconds":-10}`, nil},
		{"seek too far", `{"type":"seek_relative","seconds":-600}`, "", ErrOutOfRange},
		{"seek missing", `{"type":"seek_relative"}`, "", ErrOutOfRange},
		{"timer default pause", `{"type":"timer_start","id":"t","durationSec":600}`, `{"type":"timer_start","id":"t","durationSec":600,"pauseVideo":true}`, nil},
		{"timer no pause", `{"type":"timer_start","durationSec":60,"pauseVideo":false}`, `{"type":"timer_start","durationSec":60,"pauseVideo":false}`, nil},
		{"timer too long", `{"type":"timer_start","durationSec":7200}`, "", ErrOutOfRange},
		{"timer zero", `{"type":"timer_start","durationSec":0}`, "", ErrOutOfRange},
		{"adjust ok", `{"type":"timer_adjust","deltaSec":-60}`, `{"type":"timer_adjust","deltaSec":-60}`, nil},
		{"adjust zero", `{"type":"timer_adjust","deltaSec":0}`, "", ErrOutOfRange},
		{"controller cannot send state", `{"type":"state","videoFound":true}`, "", ErrNotAllowed},
		{"unknown", `{"type":"reboot"}`, "", ErrUnknownType},
		{"long id", `{"type":"play","id":"` + strings.Repeat("x", 65) + `"}`, "", ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, err := Validate(RoleController, []byte(tt.in))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tt.want != "" && string(got) != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestValidatePlayer(t *testing.T) {
	state := `{"type":"state","videoFound":true,"paused":false,"currentTime":12.5,"duration":4351.8,
		"timer":{"running":true,"paused":false,"endsAt":1790000000000,"remainingSec":412,"durationSec":600},"extra":1}`
	typ, got, err := Validate(RolePlayer, []byte(state))
	if err != nil || typ != TypeState {
		t.Fatalf("state: typ=%q err=%v", typ, err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["extra"]; ok {
		t.Fatal("extra field was forwarded")
	}

	if _, _, err := Validate(RolePlayer, []byte(`{"type":"state","currentTime":-1}`)); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("negative time: err = %v", err)
	}
	if _, _, err := Validate(RolePlayer, []byte(`{"type":"state","timer":null}`)); err != nil {
		t.Fatalf("null timer: %v", err)
	}
	if _, _, err := Validate(RolePlayer, []byte(`{"type":"ack","id":"a","ok":false,"error":"no_video"}`)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, _, err := Validate(RolePlayer, []byte(`{"type":"ack","ok":true}`)); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("ack without id: err = %v", err)
	}
	if _, _, err := Validate(RolePlayer, []byte(`{"type":"play"}`)); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("player sending command: err = %v", err)
	}
	if typ, _, err := Validate(RolePlayer, []byte(`{"type":"ping"}`)); err != nil || typ != TypePing {
		t.Fatalf("ping: typ=%q err=%v", typ, err)
	}
}
