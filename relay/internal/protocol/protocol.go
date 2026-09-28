// Package protocol defines the JSON messages exchanged over the relay WebSocket
// and validates them. Validation re-encodes every message from a typed struct,
// so only whitelisted fields are ever forwarded to other clients.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// MaxMessageBytes is the largest message the relay accepts.
const MaxMessageBytes = 4096

// Role identifies what kind of client is on the other end of a connection.
type Role string

const (
	RoleController Role = "controller" // phone page
	RolePlayer     Role = "player"     // browser extension
)

// Message types.
const (
	TypeAuth      = "auth"
	TypeAuthOK    = "auth_ok"
	TypeAuthError = "auth_error"
	TypePresence  = "presence"
	TypeError     = "error"
	TypePing      = "ping"
	TypePong      = "pong"

	// Controller -> player.
	TypePlay         = "play"
	TypePause        = "pause"
	TypeToggle       = "toggle"
	TypeSeekRelative = "seek_relative"
	TypeTimerStart   = "timer_start"
	TypeTimerAdjust  = "timer_adjust"
	TypeTimerPause   = "timer_pause"
	TypeTimerResume  = "timer_resume"
	TypeTimerStop    = "timer_stop"
	TypeRequestState = "request_state"

	// Player -> controllers.
	TypeState = "state"
	TypeAck   = "ack"
)

// Limits on command arguments.
const (
	MaxSeekSeconds    = 60
	MaxTimerSeconds   = 3600
	MaxTimerAdjustSec = 600
	maxIDLen          = 64
	maxClientNameLen  = 64
	maxPasswordLen    = 256
	maxAckErrorLen    = 200
	maxMediaSeconds   = 24 * 60 * 60
	maxEpochMillis    = 1 << 53
)

var (
	ErrMalformed   = errors.New("malformed message")
	ErrUnknownType = errors.New("unknown message type")
	ErrNotAllowed  = errors.New("message type not allowed for role")
	ErrOutOfRange  = errors.New("field out of range")
)

// Auth is the first message every client must send.
type Auth struct {
	Type       string `json:"type"`
	Password   string `json:"password"`
	Role       Role   `json:"role"`
	ClientName string `json:"clientName,omitempty"`
}

// ParseAuth decodes and checks the handshake message.
func ParseAuth(data []byte) (Auth, error) {
	var a Auth
	if err := json.Unmarshal(data, &a); err != nil {
		return a, ErrMalformed
	}
	if a.Type != TypeAuth {
		return a, fmt.Errorf("%w: expected auth", ErrMalformed)
	}
	if a.Role != RoleController && a.Role != RolePlayer {
		return a, fmt.Errorf("%w: role", ErrMalformed)
	}
	if a.Password == "" || len(a.Password) > maxPasswordLen {
		return a, fmt.Errorf("%w: password", ErrMalformed)
	}
	if len(a.ClientName) > maxClientNameLen {
		a.ClientName = a.ClientName[:maxClientNameLen]
	}
	return a, nil
}

// Command is a controller -> player message.
type Command struct {
	Type        string   `json:"type"`
	ID          string   `json:"id,omitempty"`
	Seconds     *float64 `json:"seconds,omitempty"`
	DurationSec *int     `json:"durationSec,omitempty"`
	PauseVideo  *bool    `json:"pauseVideo,omitempty"`
	DeltaSec    *int     `json:"deltaSec,omitempty"`
}

// TimerState describes the overlay countdown as reported by the player.
type TimerState struct {
	Running      bool    `json:"running"`
	Paused       bool    `json:"paused"`
	EndsAt       int64   `json:"endsAt"`
	RemainingSec float64 `json:"remainingSec"`
	DurationSec  int     `json:"durationSec"`
}

// State is a player -> controllers status report.
type State struct {
	Type        string      `json:"type"`
	VideoFound  bool        `json:"videoFound"`
	Paused      bool        `json:"paused"`
	CurrentTime float64     `json:"currentTime"`
	Duration    float64     `json:"duration"`
	Timer       *TimerState `json:"timer"`
}

// Ack is a player -> controllers reply to a command.
type Ack struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Presence tells controllers who is connected.
type Presence struct {
	Type        string `json:"type"`
	Players     int    `json:"players"`
	Controllers int    `json:"controllers"`
}

// ServerMessage is a small server-generated message (auth replies, errors, pong).
type ServerMessage struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

// Validate checks a post-handshake message from a client with the given role.
// It returns the message type and a canonical re-encoding that is safe to forward.
func Validate(role Role, data []byte) (string, []byte, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", nil, ErrMalformed
	}
	if head.Type == TypePing {
		return TypePing, nil, nil
	}
	switch role {
	case RoleController:
		return validateCommand(head.Type, data)
	case RolePlayer:
		return validatePlayerMessage(head.Type, data)
	}
	return "", nil, ErrNotAllowed
}

func validateCommand(typ string, data []byte) (string, []byte, error) {
	var c Command
	if err := json.Unmarshal(data, &c); err != nil {
		return "", nil, ErrMalformed
	}
	if len(c.ID) > maxIDLen {
		return "", nil, fmt.Errorf("%w: id", ErrOutOfRange)
	}
	out := Command{Type: c.Type, ID: c.ID}
	switch typ {
	case TypePlay, TypePause, TypeToggle, TypeTimerPause, TypeTimerResume, TypeTimerStop, TypeRequestState:
	case TypeSeekRelative:
		if c.Seconds == nil || !finite(*c.Seconds) || math.Abs(*c.Seconds) > MaxSeekSeconds {
			return "", nil, fmt.Errorf("%w: seconds", ErrOutOfRange)
		}
		out.Seconds = c.Seconds
	case TypeTimerStart:
		if c.DurationSec == nil || *c.DurationSec < 1 || *c.DurationSec > MaxTimerSeconds {
			return "", nil, fmt.Errorf("%w: durationSec", ErrOutOfRange)
		}
		out.DurationSec = c.DurationSec
		pause := true
		if c.PauseVideo != nil {
			pause = *c.PauseVideo
		}
		out.PauseVideo = &pause
	case TypeTimerAdjust:
		if c.DeltaSec == nil || *c.DeltaSec == 0 || abs(*c.DeltaSec) > MaxTimerAdjustSec {
			return "", nil, fmt.Errorf("%w: deltaSec", ErrOutOfRange)
		}
		out.DeltaSec = c.DeltaSec
	case TypeState, TypeAck:
		return "", nil, ErrNotAllowed
	default:
		return "", nil, ErrUnknownType
	}
	b, err := json.Marshal(out)
	return typ, b, err
}

func validatePlayerMessage(typ string, data []byte) (string, []byte, error) {
	switch typ {
	case TypeState:
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			return "", nil, ErrMalformed
		}
		if !finite(s.CurrentTime) || s.CurrentTime < 0 || s.CurrentTime > maxMediaSeconds {
			return "", nil, fmt.Errorf("%w: currentTime", ErrOutOfRange)
		}
		// Duration is NaN/Infinity in the browser until metadata loads; players send 0 then.
		if !finite(s.Duration) || s.Duration < 0 || s.Duration > maxMediaSeconds {
			return "", nil, fmt.Errorf("%w: duration", ErrOutOfRange)
		}
		if t := s.Timer; t != nil {
			if !finite(t.RemainingSec) || t.RemainingSec < 0 || t.RemainingSec > 2*MaxTimerSeconds ||
				t.DurationSec < 0 || t.DurationSec > 2*MaxTimerSeconds ||
				t.EndsAt < 0 || t.EndsAt > maxEpochMillis {
				return "", nil, fmt.Errorf("%w: timer", ErrOutOfRange)
			}
		}
		b, err := json.Marshal(s)
		return typ, b, err
	case TypeAck:
		var a Ack
		if err := json.Unmarshal(data, &a); err != nil {
			return "", nil, ErrMalformed
		}
		if a.ID == "" || len(a.ID) > maxIDLen {
			return "", nil, fmt.Errorf("%w: id", ErrOutOfRange)
		}
		if len(a.Error) > maxAckErrorLen {
			a.Error = a.Error[:maxAckErrorLen]
		}
		b, err := json.Marshal(a)
		return typ, b, err
	case TypePlay, TypePause, TypeToggle, TypeSeekRelative, TypeTimerStart, TypeTimerAdjust,
		TypeTimerPause, TypeTimerResume, TypeTimerStop, TypeRequestState:
		return "", nil, ErrNotAllowed
	}
	return "", nil, ErrUnknownType
}

// Encode marshals a server-generated message. It panics only on programmer error.
func Encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
