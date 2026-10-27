// Package protocol defines the JSON messages exchanged over the websocket
// and the HTTP bodies used for pairing.
package protocol

const (
	TypeHello    = "hello"    // server -> client after connect
	TypeOffer    = "offer"    // server -> client: a transfer is ready to download
	TypeClip     = "clip"     // both directions
	TypeAck      = "ack"      // client -> server: an offer finished (Status)
	TypeProgress = "progress" // client -> server: download offset of an offer
	TypeToken    = "token"    // server -> client: a rotated device token
	TypeCancel   = "cancel"   // server -> client: the laptop cancelled an offer

	// Version is the protocol revision this daemon speaks. Revision 2 adds
	// resumable uploads, ranged downloads and per-file folder manifests.
	Version = 2

	// MaxClipBytes is the largest clipboard text either side sends or applies.
	MaxClipBytes = 1 << 20
	// MaxMessageBytes bounds one websocket frame: a clip of MaxClipBytes whose
	// JSON escaping at worst expands every byte, plus envelope.
	MaxMessageBytes = 8 << 20

	StatusOK        = "ok"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	// HeaderProto is sent by clients that speak revision 2 or newer.
	HeaderProto = "X-Sharing-Proto"
)

type Message struct {
	Type string `json:"type"`

	// hello
	Name  string   `json:"name,omitempty"`
	Proto int      `json:"proto,omitempty"`
	Caps  []string `json:"caps,omitempty"`

	// offer: client fetches GET /transfer/{ID}; Name is the file or folder name
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`
	// Count is the number of files in a folder offer.
	Count int `json:"count,omitempty"`

	// ack: Status is one of the Status* values; progress: Offset is bytes downloaded
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	Offset int64  `json:"offset,omitempty"`

	// token: the new device token
	Token string `json:"token,omitempty"`

	// clip
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
	TS   int64  `json:"ts,omitempty"`
}

type PairRequest struct {
	Token    string `json:"token"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
}

type PairResponse struct {
	PeerToken  string `json:"peerToken"`
	DeviceID   string `json:"deviceId"`
	LaptopName string `json:"laptopName"`
}
