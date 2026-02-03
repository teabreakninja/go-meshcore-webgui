package models

import "time"

// Contact represents a contact on the mesh network.
type Contact struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"lastSeen"`
}

// Channel represents a channel on the mesh network.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Message represents a message sent over the mesh network.
type Message struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	IsChannel   bool      `json:"isChannel"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	Status      string    `json:"status,omitempty"`      // "pending", "sent", "delivered"
	RoundTripMs uint32    `json:"roundTripMs,omitempty"` // Round trip time in milliseconds
	AckCode     uint32    `json:"ackCode,omitempty"`     // Used to match SendConfirmed pushes
	PathLen     uint8     `json:"pathLen,omitempty"`     // Number of hops (0xFF = direct)
	SenderTime  time.Time `json:"senderTime,omitempty"`  // When sender created the message
}

// DeviceInfo holds structured information about the connected node.
type DeviceInfo struct {
	FirmwareVersion string `json:"firmwareVersion"`
	FirmwareBuild   string `json:"firmwareBuild"`
	Model           string `json:"model"`
	MaxContacts     int    `json:"maxContacts"`
	MaxChannels     int    `json:"maxChannels"`
	PublicKey       string `json:"publicKey"` // hex-encoded public key
	UserName        string `json:"userName"`  // user's advertising name
}

// MeshContact represents the low-level contact data structure from the mesh device.
type MeshContact struct {
	PublicKey  [32]byte
	Type       byte
	Flags      byte
	OutPathLen int8
	OutPath    [64]byte
	AdvName    [32]byte
	LastAdvert uint32
	AdvLat     uint32
	AdvLon     uint32
	LastMod    uint32
}
