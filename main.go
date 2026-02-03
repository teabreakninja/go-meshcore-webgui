package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"go-meshcore-webgui/app/client"
	"go-meshcore-webgui/app/models"

	"github.com/gorilla/websocket"
)

// WebSocket hub to broadcast events to all connected clients.

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Adjust this for stricter origin checks in production.
		return true
	},
}

type Client struct {
	hub  *Hub // TODO: This is a struct from main.go
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

func (c *Client) readPump(mesh client.MeshcoreClient) {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(5120)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			break
		}
		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Println("invalid message:", err)
			continue
		}
		handleIncoming(c, mesh, msg)
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Println("write error:", err)
			break
		}
	}
}

// WebSocket message protocol

type IncomingMessage struct {
	Type       string `json:"type"`
	TargetID   string `json:"targetId,omitempty"`
	TargetType string `json:"targetType,omitempty"` // "contact" or "channel"
	Content    string `json:"content,omitempty"`
}

type OutgoingMessage struct {
	Type     string      `json:"type"`
	Payload  interface{} `json:"payload,omitempty"`
	ErrorMsg string      `json:"error,omitempty"`
}

type StatePayload struct {
	Contacts  []models.Contact `json:"contacts"`
	Channels  []models.Channel `json:"channels"`
	Messages  []models.Message `json:"messages"`
	Favorites []string         `json:"favorites"`
}

type SingleMessagePayload struct {
	Message models.Message `json:"message"`
}

type NodeStatusPayload struct {
	Connected  bool              `json:"connected"`
	DeviceInfo models.DeviceInfo `json:"deviceInfo,omitempty"`
}

type MessageStatusPayload struct {
	MessageID   string `json:"messageId"`
	Status      string `json:"status"` // "pending", "sent", "delivered"
	RoundTripMs uint32 `json:"roundTripMs,omitempty"`
}

func handleIncoming(c *Client, mesh client.MeshcoreClient, msg IncomingMessage) {
	switch msg.Type {
	case "subscribe_state":
		contacts, err1 := mesh.GetContacts()
		channels, err2 := mesh.GetChannels()
		messages, err3 := mesh.GetRecentMessages()
		favorites, err4 := mesh.GetFavorites()
		mesh.EnablePushMode()
		mesh.FetchWaitingMessages() // Proactively fetch any waiting messages from device
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			resp := OutgoingMessage{Type: "error", ErrorMsg: "failed to load initial state"}
			log.Println("error loading state:", err1, err2, err3, err4)
			c.sendJSON(resp)
			return
		}
		resp := OutgoingMessage{
			Type: "state",
			Payload: StatePayload{
				Contacts:  contacts,
				Channels:  channels,
				Messages:  messages,
				Favorites: favorites,
			},
		}
		c.sendJSON(resp)

	case "send_message":
		if msg.TargetID == "" || msg.Content == "" {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "targetId and content are required"})
			return
		}
		if len(msg.Content) > 150 {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "message is too long (max 150 characters)"})
			log.Printf("message rejected: content length %d exceeds 150 character limit", len(msg.Content))
			return
		}
		isChannel := msg.TargetType == "channel"
		m, err := mesh.SendMessage(msg.TargetID, isChannel, msg.Content)
		if err != nil {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "failed to send message"})
			return
		}

		// Broadcast new message to all clients
		broadcastJSON(c.hub, OutgoingMessage{
			Type:    "message",
			Payload: SingleMessagePayload{Message: m},
		})

	case "toggle_favorite":
		if msg.TargetID == "" {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "targetId is required"})
			return
		}
		if err := mesh.ToggleFavorite(msg.TargetID); err != nil {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: fmt.Sprintf("failed to toggle favorite: %v", err)})
			return
		}
		// Send updated favorites list
		favorites, err := mesh.GetFavorites()
		if err != nil {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "failed to get favorites"})
			return
		}
		c.sendJSON(OutgoingMessage{
			Type:    "favorites_updated",
			Payload: favorites,
		})

	case "send_zero_hop_advert":
		if err := mesh.SendZeroHopAdvert(); err != nil {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: fmt.Sprintf("failed to send zero-hop advert: %v", err)})
			return
		}
		c.sendJSON(OutgoingMessage{Type: "advert_sent", Payload: "zero-hop"})

	case "send_flood_advert":
		if err := mesh.SendFloodAdvert(); err != nil {
			c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: fmt.Sprintf("failed to send flood advert: %v", err)})
			return
		}
		c.sendJSON(OutgoingMessage{Type: "advert_sent", Payload: "flood"})

	default:
		c.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "unknown message type"})
	}
}

func (c *Client) sendJSON(msg OutgoingMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Println("marshal error:", err)
		return
	}
	select {
	case c.send <- data:
	default:
		log.Println("client send buffer full, dropping message")
	}
}

func broadcastJSON(h *Hub, msg OutgoingMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Println("marshal error:", err)
		return
	}
	h.broadcast <- data
}

func serveWs(hub *Hub, mesh client.MeshcoreClient, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256)}
	hub.register <- client
	go client.writePump()
	// Send initial node connection status to this client
	client.sendJSON(OutgoingMessage{
		Type: "node_status",
		Payload: NodeStatusPayload{
			Connected:  mesh.IsConnected(),
			DeviceInfo: mesh.GetDeviceInfo(),
		},
	})

	// Proactively send the full state to the new client if connected.
	// This avoids the 15-second delay observed on the frontend.
	if mesh.IsConnected() {
		contacts, err1 := mesh.GetContacts()
		channels, err2 := mesh.GetChannels()
		messages, err3 := mesh.GetRecentMessages()
		favorites, err4 := mesh.GetFavorites()
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			log.Printf("error loading initial state for new client: %v, %v, %v, %v", err1, err2, err3, err4)
			client.sendJSON(OutgoingMessage{Type: "error", ErrorMsg: "failed to load initial state"})
		} else {
			client.sendJSON(OutgoingMessage{
				Type: "state",
				Payload: StatePayload{
					Contacts:  contacts,
					Channels:  channels,
					Messages:  messages,
					Favorites: favorites,
				},
			})
		}
	}
	client.readPump(mesh)
}

type connectRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type connectResponse struct {
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	Connected bool   `json:"connected"`
	Address   string `json:"address"`
	Firmware  string `json:"firmware,omitempty"`
}

func main() {
	addr := "127.0.0.1:8080"
	if fromEnv := os.Getenv("MESHCORE_GUI_ADDR"); fromEnv != "" {
		addr = fromEnv
	}

	// In real implementation, construct a meshcore client that talks to your node.
	meshClient := client.NewInMemoryMeshcoreClient()

	hub := NewHub()
	go hub.Run()

	// When the mesh client receives an incoming message from the node,
	// broadcast it to all connected WebSocket clients.
	meshClient.SetIncomingMessageHandler(func(m models.Message) {
		log.Printf("hub: broadcasting incoming mesh msg from=%s to=%s content=%q",
			m.From, m.To, m.Content)
		broadcastJSON(hub, OutgoingMessage{
			Type:    "message",
			Payload: SingleMessagePayload{Message: m},
		})
	})

	// When a message status updates (sent/delivered), broadcast to all clients
	meshClient.SetMessageStatusUpdateHandler(func(messageID string, status string, roundTripMs uint32) {
		log.Printf("hub: broadcasting message status update for %s: %s (roundTrip=%dms)",
			messageID, status, roundTripMs)
		broadcastJSON(hub, OutgoingMessage{
			Type: "message_status",
			Payload: MessageStatusPayload{
				MessageID:   messageID,
				Status:      status,
				RoundTripMs: roundTripMs,
			},
		})
	})

	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/index.html")
	})

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, meshClient, w, r)
	})

	http.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req connectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(connectResponse{Success: false, Error: "invalid request"})
			return
		}

		if err := meshClient.Connect(req.Host, req.Port); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(connectResponse{Success: false, Error: err.Error(), Connected: false})
			return
		}

		_ = json.NewEncoder(w).Encode(connectResponse{
			Success:   true,
			Connected: true,
			Address:   meshClient.Address(),
			Firmware:  meshClient.FirmwareVersion(),
		})

		// Broadcast node status to all connected WebSocket clients
		broadcastJSON(hub, OutgoingMessage{
			Type: "node_status",
			Payload: NodeStatusPayload{
				Connected:  true,
				DeviceInfo: meshClient.GetDeviceInfo(),
			},
		})

		// Proactively fetch and broadcast state to avoid frontend delay
		go func() {
			contacts, err1 := meshClient.GetContacts()
			channels, err2 := meshClient.GetChannels()
			messages, err3 := meshClient.GetRecentMessages()
			favorites, err4 := meshClient.GetFavorites()
			meshClient.EnablePushMode()
			meshClient.FetchWaitingMessages()
			if err1 == nil && err2 == nil && err3 == nil && err4 == nil {
				broadcastJSON(hub, OutgoingMessage{
					Type: "state",
					Payload: StatePayload{
						Contacts:  contacts,
						Channels:  channels,
						Messages:  messages,
						Favorites: favorites,
					},
				})
			}
		}()
	})

	http.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		_ = meshClient.Disconnect()

		_ = json.NewEncoder(w).Encode(connectResponse{
			Success:   true,
			Connected: false,
			Address:   "",
			Firmware:  "",
		})

		broadcastJSON(hub, OutgoingMessage{
			Type: "node_status",
			Payload: NodeStatusPayload{
				Connected: false,
			},
		})
	})

	http.HandleFunc("/api/add-channel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ChannelIdx int    `json:"channelIdx"`
			Name       string `json:"name"`
			Secret     string `json:"secret"` // hex-encoded 16-byte secret
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "invalid request",
			})
			return
		}

		// Decode hex secret
		secret, err := hex.DecodeString(req.Secret)
		if err != nil || len(secret) != 16 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "secret must be 32 hex characters (16 bytes)",
			})
			return
		}

		// Set the channel on the node
		if err := meshClient.SetChannel(req.ChannelIdx, req.Name, secret); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		// Success - fetch updated channels and broadcast to all clients
		channels, err := meshClient.GetChannels()
		if err != nil {
			log.Printf("warning: failed to fetch channels after adding: %v", err)
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
		})

		// Broadcast updated channel list to all connected WebSocket clients
		if channels != nil {
			contacts, _ := meshClient.GetContacts()
			messages, _ := meshClient.GetRecentMessages()
			broadcastJSON(hub, OutgoingMessage{
				Type: "state",
				Payload: StatePayload{
					Contacts: contacts,
					Channels: channels,
					Messages: messages,
				},
			})
		}
	})

	http.HandleFunc("/api/clear-channel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ChannelIdx int `json:"channelIdx"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "invalid request",
			})
			return
		}

		// Clear the channel by setting it to an empty name and zeroed secret
		emptySecret := make([]byte, 16)
		if err := meshClient.SetChannel(req.ChannelIdx, "", emptySecret); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		// Clear messages for this channel from storage
		channelID := fmt.Sprintf("ch%d", req.ChannelIdx)
		if err := meshClient.ClearChannelMessages(channelID); err != nil {
			log.Printf("warning: failed to clear messages for %s: %v", channelID, err)
			// Don't fail the request if message clearing fails
		}

		// Success - fetch updated channels and broadcast to all clients
		channels, err := meshClient.GetChannels()
		if err != nil {
			log.Printf("warning: failed to fetch channels after clearing: %v", err)
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
		})

		// Broadcast updated channel list to all connected WebSocket clients
		if channels != nil {
			contacts, _ := meshClient.GetContacts()
			messages, _ := meshClient.GetRecentMessages()
			broadcastJSON(hub, OutgoingMessage{
				Type: "state",
				Payload: StatePayload{
					Contacts: contacts,
					Channels: channels,
					Messages: messages,
				},
			})
		}
	})

	log.Printf("meshcore GUI listening on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
