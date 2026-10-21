// Package protocol defines the JSON messages exchanged over the websocket
// and the HTTP bodies used for pairing.
package protocol

const (
	TypeHello = "hello" // server -> client after connect
	TypeOffer = "offer" // server -> client: a transfer is ready to download
	TypeClip  = "clip"  // both directions (plan 2)
)

type Message struct {
	Type string `json:"type"`

	// hello
	Name string `json:"name,omitempty"`

	// offer: client fetches GET /transfer/{ID}; Name is the file or folder name
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`

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
